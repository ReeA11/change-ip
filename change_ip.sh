#!/bin/bash
#
# change_ip.sh — исходящие соединения с выбранного белого IPv4
#
# Зачем: чтобы исходящий трафик шёл с выбранного публичного IPv4, а не
# с первого адреса на карточке.
# Не делает: не заказывает IP у провайдера, не открывает порты,
# не трогает Docker и firewall.
# Из панели: IP, шлюз, маска (если есть). Префикс скрипт не угадывает.
#
# Запуск: sudo change_ip
# После смены: проверить сайт/API; для стойкости после reboot —
# sudo change_ip status
#
# Ядро (runtime / persist / lock / rollback) вызывается из мастера
# и старого CLI; интерфейс — слой сверху, не замена apply_*.
#

set -Eeuo pipefail

SCRIPT_NAME="$(basename "$0")"
SCRIPT_VERSION="2.6.0"
UI_LANG=""

# Exit / rollback state
EXIT_CODE=0
CLEANUP_DONE=0
ROLLBACK_ATTEMPTED=0
RUNTIME_CHANGED=0
PERSIST_CHANGED=0
VERIFY_FAILED=0
GW_HOST_ROUTE_ADDED=0

# Operation flags
DRY_RUN=0
RUNTIME_ONLY=0
ASSUME_YES=0
CHECK_EGRESS=0
ROLLBACK_MODE=0
ROLLBACK_SOURCE=""

# Network target
TARGET_SPEC=""
NEW_IP=""
PREFIX_ARG=""
GATEWAY_ARG=""
GATEWAY_FROM_PROFILE=0
INTERFACE_ARG=""
PROFILE_FILE="${CHANGE_IP_PROFILE:-/etc/change-ip-addresses.conf}"

# Runtime snapshot
INTERFACE=""
OLD_IP=""
OLD_MASK=""
OLD_SRC=""
OLD_GW=""
OLD_ONLINK=""
OLD_DEFAULT_METRIC=""
OLD_DEFAULT_TABLE=""
GW=""
MASK=""
ONLINK=0
NEW_ADDED=0

# Persistence
PERSISTENCE="none"
NETPLAN_KEY=""
NETPLAN_SECTION=""
NETPLAN_RENDERER=""
ROLLBACK_DIR=""
BACKUP_CREATED=0
NETPLAN_DROPIN=""
NETPLAN_MANAGED=0
INTERFACES_FILE="/etc/network/interfaces"
SYSTEMD_UNIT=""
SYSTEMD_SCRIPT=""

LOCKFILE="/run/change_ip.lock"
TMP_DIR=""

log()  { printf '[+] %s\n' "$*"; }
ok()   { printf '[OK] %s\n' "$*"; }
warn() { printf '[WARNING] %s\n' "$*" >&2; }

fail() {
    printf '[ERROR] %s\n' "$*" >&2
    EXIT_CODE=1
    return 1
}

die() {
    fail "$@"
    exit "${EXIT_CODE:-1}"
}

detect_ui_lang() {
    [[ -n "${UI_LANG:-}" ]] && return 0
    local loc="${LC_ALL:-${LC_MESSAGES:-${LANG:-}}}"
    if [[ "$loc" == ru* || "${LANGUAGE:-}" == ru* ]]; then
        UI_LANG=ru
    else
        UI_LANG=en
    fi
}

msg_no_default_route() {
    detect_ui_lang
    if [[ "$UI_LANG" == ru ]]; then
        printf '%s' "Нет default IPv4. Укажите интерфейс с белым IP: --interface eth0"
    else
        printf '%s' "No default IPv4 route. Name the interface with the public IP: --interface eth0"
    fi
}

msg_no_gateway() {
    local if="$1"
    detect_ui_lang
    if [[ "$UI_LANG" == ru ]]; then
        printf '%s' "На $if нет default IPv4-шлюза. Проверьте: ip -4 route show default"
    else
        printf '%s' "No default IPv4 gateway on $if. Check: ip -4 route show default"
    fi
}

msg_prefix_required() {
    local ip="$1"
    detect_ui_lang
    if [[ "$UI_LANG" == ru ]]; then
        printf '%s' "Для нового IP $ip нужен префикс (например $ip/24). Скрипт маску не угадывает."
    else
        printf '%s' "Prefix required for new IP $ip (example: $ip/24). The mask is never guessed."
    fi
}

msg_need_root() {
    detect_ui_lang
    if [[ "$UI_LANG" == ru ]]; then
        printf '%s' "Запустите: sudo $SCRIPT_NAME"
    else
        printf '%s' "Run: sudo $SCRIPT_NAME"
    fi
}

usage() {
    detect_ui_lang
    if [[ "$UI_LANG" == ru ]]; then
        cat <<EOF
$SCRIPT_NAME — исходящие соединения с выбранного белого IPv4

Зачем: сайт, API и curl идут с выбранного публичного IPv4.
Не делает: не заказывает IP, не открывает порты, не трогает Docker/firewall.
Из панели: IP, шлюз, маска если есть (пусто часто нормально).

Запуск:
  sudo $SCRIPT_NAME                      мастер (спросит только то, чего нет)
  sudo $SCRIPT_NAME status               текущий src, шлюз, persist
  sudo $SCRIPT_NAME doctor               почему reboot не применил смену
  sudo $SCRIPT_NAME rollback             список бэкапов, выбор
  sudo $SCRIPT_NAME NEW_IP[/PREFIX] [IFACE]
  sudo $SCRIPT_NAME NEW_IP --prefix N --gateway GW --interface IFACE
  sudo $SCRIPT_NAME --rollback DIR

После смены: проверьте сайт/API. Чтобы пережило reboot:
  sudo $SCRIPT_NAME status

Опции CLI (не нужны в мастере):
  --runtime-only   только ядро, без persist
  --dry-run        план, без изменений
  --yes, -y        без подтверждения (для скриптов, не для мастера)
  --check-egress   плюс 8.8.8.8 и ifconfig.me
  --profile FILE   реестр адресов (по умолчанию $PROFILE_FILE)

Префикс для нового IP обязателен, скрипт его не угадывает.
Существующие адреса на интерфейсе не удаляются.
Авто-выбор iface: default route, не docker/wg/tun.
На Debian и Ubuntu persist — сгенерированный systemd oneshot после network-online.

Профиль ($PROFILE_FILE):
  # IP/PREFIX          ШЛЮЗ
  176.96.136.246/25    176.96.136.129
EOF
    else
        cat <<EOF
$SCRIPT_NAME — send outbound connections from a chosen public IPv4

Why: websites, APIs and curl use the IPv4 you pick, not the first address
on the NIC.
Does not: order IPs from the provider, open ports, or touch Docker/firewall.
From the panel: IP, gateway, mask if shown (empty mask is common).

Run:
  sudo $SCRIPT_NAME                      wizard (asks only what the system lacks)
  sudo $SCRIPT_NAME status               current src, gateway, persist
  sudo $SCRIPT_NAME doctor               why reboot did not apply the change
  sudo $SCRIPT_NAME rollback             pick a /root/change-ip-backup.*
  sudo $SCRIPT_NAME NEW_IP[/PREFIX] [IFACE]
  sudo $SCRIPT_NAME NEW_IP --prefix N --gateway GW --interface IFACE
  sudo $SCRIPT_NAME --rollback DIR

After a change: check the site/API. To confirm it survives reboot:
  sudo $SCRIPT_NAME status

CLI options (not used by the wizard):
  --runtime-only   kernel routes only; skip persist
  --dry-run        print the plan; make no changes
  --yes, -y        skip confirmation (scripts only, not the wizard)
  --check-egress   also test 8.8.8.8 and ifconfig.me
  --profile FILE   address registry (default: $PROFILE_FILE)

Prefix is mandatory for a new IP; it is never guessed.
Existing addresses on the NIC are kept.
Auto-detected iface comes from the default route, never docker/wg/tun.
On Debian and Ubuntu, persist is a generated systemd oneshot after network-online.

Profile ($PROFILE_FILE):
  # IP/PREFIX          GATEWAY
  176.96.136.246/25    176.96.136.129
EOF
    fi
}

cleanup_tmp() {
    if [[ -n "${TMP_DIR:-}" && -d "${TMP_DIR:-}" ]]; then
        rm -rf "$TMP_DIR"
    fi
    return 0
}

release_lock() {
    [[ -n "${LOCK_FD:-}" ]] || return 0
    flock -u "$LOCK_FD" 2>/dev/null || true
}

cleanup() {
    [[ "$CLEANUP_DONE" == "1" ]] && return 0
    CLEANUP_DONE=1

    if [[ "$RUNTIME_CHANGED" == "1" &&
          "${EXIT_CODE:-0}" != "0" &&
          "$ROLLBACK_ATTEMPTED" != "1" ]]; then
        ROLLBACK_ATTEMPTED=1
        rollback_runtime || true
    fi

    release_lock
    cleanup_tmp
}

on_err() {
    local rc=$?
    [[ "$rc" -eq 0 ]] && return 0
    EXIT_CODE="$rc"
    warn "Script failed at line ${BASH_LINENO[0]} (exit code $rc)."
    exit "$rc"
}

on_int() {
    EXIT_CODE=130
    exit 130
}

on_hup() {
    EXIT_CODE=129
    exit 129
}

trap cleanup EXIT
trap on_err ERR
trap on_int INT
trap on_hup HUP

require_cmd() {
    command -v "$1" >/dev/null 2>&1 || die "Required command not found: $1"
}

is_ipv4() {
    local ip="$1"
    [[ "$ip" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]] || return 1

    local IFS=.
    read -r o1 o2 o3 o4 <<< "$ip"
    for o in "$o1" "$o2" "$o3" "$o4"; do
        [[ "$o" =~ ^[0-9]+$ ]] || return 1
        [[ "$o" =~ ^0[0-9]+$ ]] && return 1
        (( o >= 0 && o <= 255 )) || return 1
    done
}

is_usable_ipv4() {
    local ip="$1"
    is_ipv4 "$ip" || return 1

    local IFS=.
    read -r o1 o2 o3 o4 <<< "$ip"
    [[ "$o1.$o2.$o3.$o4" == "0.0.0.0" ]] && return 1
    [[ "$o1.$o2.$o3.$o4" == "255.255.255.255" ]] && return 1
    [[ "$o1" == "127" ]] && return 1
    (( o1 >= 224 && o1 <= 239 )) && return 1
    return 0
}

is_prefix() {
    local prefix="$1"
    [[ "$prefix" =~ ^[0-9]+$ ]] || return 1
    (( prefix >= 1 && prefix <= 32 ))
}

acquire_lock() {
    require_cmd flock
    exec {LOCK_FD}>"$LOCKFILE"
    flock -n "$LOCK_FD" || die "Another $SCRIPT_NAME instance is running (lock: $LOCKFILE)"
}

load_address_profile() {
    local line spec host prefix gateway mode

    [[ -f "$PROFILE_FILE" ]] || return 0
    [[ -f "$PROFILE_FILE" && ! -d "$PROFILE_FILE" ]] ||
        die "Profile is not a regular file: $PROFILE_FILE"

    mode="$(stat -c '%a' "$PROFILE_FILE" 2>/dev/null || echo 0)"
    if (( (mode % 10) & 2 || (mode / 10 % 10) & 2 )); then
        die "Profile file is group/world writable: $PROFILE_FILE"
    fi

    line="$(
        awk -v target="$NEW_IP" '
            /^[[:space:]]*($|#)/ { next }
            {
                split($1, address, "/")
                if (address[1] == target) {
                    print
                    exit
                }
            }
        ' "$PROFILE_FILE"
    )"
    [[ -n "$line" ]] || return 0

    read -r spec gateway _ <<< "$line"
    host="${spec%/*}"
    [[ "$spec" == */* ]] ||
        die "Profile entry for $NEW_IP has no prefix: $line"
    prefix="${spec#*/}"

    [[ "$host" == "$NEW_IP" ]] ||
        die "Invalid profile entry for $NEW_IP: $line"
    is_prefix "$prefix" ||
        die "Invalid prefix in profile entry: $line"

    if [[ -z "$PREFIX_ARG" ]]; then
        PREFIX_ARG="$prefix"
        log "Profile prefix for $NEW_IP: /$PREFIX_ARG"
    elif [[ "$PREFIX_ARG" != "$prefix" ]]; then
        die "Requested /$PREFIX_ARG conflicts with profile /$prefix for $NEW_IP"
    fi

    if [[ -z "$GATEWAY_ARG" && -n "${gateway:-}" && "$gateway" != "-" ]]; then
        is_usable_ipv4 "$gateway" ||
            die "Invalid gateway in profile entry: $line"
        GATEWAY_ARG="$gateway"
        GATEWAY_FROM_PROFILE=1
        log "Profile gateway for $NEW_IP: $GATEWAY_ARG"
    fi
}

parse_args() {
    if [[ "${1:-}" == "--rollback" ]]; then
        ROLLBACK_MODE=1
        ROLLBACK_SOURCE="${2:-}"
        [[ -n "$ROLLBACK_SOURCE" ]] || die "--rollback requires a backup directory"
        shift 2
        (( $# == 0 )) || die "Unexpected argument after --rollback: $1"
        return 0
    fi

    while (( $# > 0 )) && [[ "$1" == -* ]]; do
        case "$1" in
            --runtime-only)
                RUNTIME_ONLY=1
                shift
                ;;
            --dry-run)
                DRY_RUN=1
                shift
                ;;
            --check-egress)
                CHECK_EGRESS=1
                shift
                ;;
            -y|--yes)
                ASSUME_YES=1
                shift
                ;;
            -p|--prefix)
                (( $# >= 2 )) || die "$1 requires a prefix length"
                if [[ -n "$PREFIX_ARG" && "$PREFIX_ARG" != "$2" ]]; then
                    die "Conflicting prefix lengths: /$PREFIX_ARG and /$2"
                fi
                PREFIX_ARG="$2"
                shift 2
                ;;
            --profile)
                (( $# >= 2 )) || die "$1 requires a file path"
                PROFILE_FILE="$2"
                shift 2
                ;;
            -h|--help)
                usage
                exit 0
                ;;
            --)
                shift
                break
                ;;
            *)
                break
                ;;
        esac
    done

    TARGET_SPEC="${1:-}"
    [[ -n "$TARGET_SPEC" ]] || {
        usage
        exit 1
    }
    shift

    if [[ "$TARGET_SPEC" == */* ]]; then
        NEW_IP="${TARGET_SPEC%/*}"
        if [[ -n "$PREFIX_ARG" && "$PREFIX_ARG" != "${TARGET_SPEC#*/}" ]]; then
            die "Conflicting prefix lengths: /$PREFIX_ARG and /${TARGET_SPEC#*/}"
        fi
        PREFIX_ARG="${PREFIX_ARG:-${TARGET_SPEC#*/}}"
    else
        NEW_IP="$TARGET_SPEC"
    fi

    while (( $# > 0 )); do
        case "$1" in
            -p|--prefix)
                (( $# >= 2 )) || die "$1 requires a prefix length"
                if [[ -n "$PREFIX_ARG" && "$PREFIX_ARG" != "$2" ]]; then
                    die "Conflicting prefix lengths: /$PREFIX_ARG and /$2"
                fi
                PREFIX_ARG="$2"
                shift 2
                ;;
            -g|--gateway)
                (( $# >= 2 )) || die "$1 requires an IPv4 gateway"
                GATEWAY_ARG="$2"
                shift 2
                ;;
            -i|--interface)
                (( $# >= 2 )) || die "$1 requires an interface"
                INTERFACE_ARG="$2"
                shift 2
                ;;
            --profile)
                (( $# >= 2 )) || die "$1 requires a file path"
                PROFILE_FILE="$2"
                shift 2
                ;;
            --runtime-only|--dry-run|--check-egress|-y|--yes)
                die "Option $1 must appear before NEW_IP"
                ;;
            -h|--help)
                usage
                exit 0
                ;;
            --)
                shift
                break
                ;;
            -*)
                die "Unknown option: $1"
                ;;
            *)
                [[ -z "$INTERFACE_ARG" ]] ||
                    die "Unexpected positional argument: $1"
                INTERFACE_ARG="$1"
                shift
                ;;
        esac
    done

    (( $# == 0 )) || die "Unexpected argument: $1"

    is_usable_ipv4 "$NEW_IP" || die "Invalid or unusable IPv4 address: $NEW_IP"
    load_address_profile
    [[ -z "$PREFIX_ARG" ]] ||
        is_prefix "$PREFIX_ARG" ||
        die "Invalid IPv4 prefix length: $PREFIX_ARG (expected 1-32)"
    [[ -z "$GATEWAY_ARG" ]] ||
        is_usable_ipv4 "$GATEWAY_ARG" ||
        die "Invalid IPv4 gateway: $GATEWAY_ARG"
    [[ -z "$GATEWAY_ARG" || "$GATEWAY_ARG" != "$NEW_IP" ]] ||
        die "NEW_IP and gateway must differ: $NEW_IP"
}

iface_has_ip() {
    local ip="$1"
    ip -4 -o addr show dev "$INTERFACE" 2>/dev/null |
        awk '{print $4}' | cut -d/ -f1 | grep -Fxq "$ip"
}

get_ip_mask() {
    local ip="$1"
    [[ -n "$ip" ]] || return 1
    ip -4 -o addr show dev "$INTERFACE" scope global 2>/dev/null |
        awk -v ip="$ip" '
            {
                split($4, a, "/")
                if (a[1] == ip) {
                    print a[2]
                    exit
                }
            }
        '
}

get_primary_ip() {
    ip -4 -o addr show dev "$INTERFACE" scope global 2>/dev/null |
        awk '{print $4}' | cut -d/ -f1 | head -1
}

get_default_gateway() {
    ip -4 route show default dev "$INTERFACE" 2>/dev/null |
        awk '{
            for (i = 1; i <= NF; i++)
                if ($i == "via") {
                    print $(i+1)
                    exit
                }
        }'
}

get_default_src() {
    ip -4 route show default dev "$INTERFACE" 2>/dev/null |
        awk '{
            for (i = 1; i <= NF; i++)
                if ($i == "src") {
                    print $(i+1)
                    exit
                }
        }'
}

get_default_metric() {
    ip -4 route show default dev "$INTERFACE" 2>/dev/null |
        awk '{
            for (i = 1; i <= NF; i++)
                if ($i == "metric") {
                    print $(i+1)
                    exit
                }
        }'
}

get_default_table() {
    ip -4 route show default dev "$INTERFACE" 2>/dev/null |
        awk '{
            for (i = 1; i <= NF; i++)
                if ($i == "table") {
                    print $(i+1)
                    exit
                }
        }'
}

detect_interface() {
    ip -4 route show default 2>/dev/null |
        awk '{
            for (i = 1; i <= NF; i++)
                if ($i == "dev") {
                    print $(i+1)
                    exit
                }
        }'
}

interface_devtype() {
    local if="$1"
    local uevent="/sys/class/net/$if/uevent"
    [[ -f "$uevent" ]] || return 0
    awk -F= '$1 == "DEVTYPE" { print $2; exit }' "$uevent"
}

is_virtual_interface_name() {
    local if="$1"
    [[ "$if" =~ ^(wg|tun|tap|ppp|utun) ]]
}

validate_interface() {
    local if="$1"
    local iftype devtype

    ip link show "$if" >/dev/null 2>&1 ||
        die "Interface not found: $if"

    iftype="$(cat "/sys/class/net/$if/type" 2>/dev/null || echo 0)"
    [[ "$iftype" != "772" ]] ||
        die "Refusing loopback interface: $if"

    devtype="$(interface_devtype "$if")"
    case "$devtype" in
        ""|ether|bond|bridge|vlan)
            ;;
        *)
            if [[ -z "$INTERFACE_ARG" ]]; then
                die "Interface '$if' (DEVTYPE=${devtype:-unknown}) requires explicit -i"
            fi
            ;;
    esac

    if [[ -z "$INTERFACE_ARG" ]] && is_virtual_interface_name "$if"; then
        die "Refusing auto-detected virtual interface '$if'. Specify -i explicitly."
    fi
}

ipv4_to_int() {
    local ip="$1" a b c d
    local IFS=.
    read -r a b c d <<< "$ip"
    printf '%u\n' "$(( (a << 24) | (b << 16) | (c << 8) | d ))"
}

prefix_contains() {
    local ip="$1" cidr="$2"
    local network="${cidr%/*}" prefix="${cidr#*/}"
    local ip_int network_int mask

    is_ipv4 "$network" || return 1
    is_prefix "$prefix" || return 1

    ip_int="$(ipv4_to_int "$ip")"
    network_int="$(ipv4_to_int "$network")"

    if (( prefix == 0 )); then
        mask=0
    else
        mask=$(( (0xFFFFFFFF << (32 - prefix)) & 0xFFFFFFFF ))
    fi

    (( (ip_int & mask) == (network_int & mask) ))
}

gateway_needs_onlink() {
    if ip -4 route show default dev "$INTERFACE" 2>/dev/null |
        grep -Eq '(^|[[:space:]])onlink([[:space:]]|$)'; then
        return 0
    fi

    local destination _
    while read -r destination _; do
        [[ -n "$destination" ]] || continue
        if [[ "${destination%/*}" == "$GW" ]]; then
            return 1
        fi
        if [[ "$destination" == */* ]] &&
           prefix_contains "$GW" "$destination"; then
            return 1
        fi
    done < <(ip -4 route show dev "$INTERFACE" scope link 2>/dev/null)

    return 0
}

detect_persistence_backend() {
    if command -v systemctl >/dev/null 2>&1 &&
       [[ -d /run/systemd/system ]]; then
        PERSISTENCE="systemd"
        return 0
    fi

    PERSISTENCE="none"
}




preflight_persistence() {
    detect_persistence_backend

    if [[ "$RUNTIME_ONLY" == "1" ]]; then
        log "Persistence preflight skipped (--runtime-only)"
        return 0
    fi

    [[ "$PERSISTENCE" == "systemd" ]] ||
        die "Active systemd is required for reboot persistence. Use --runtime-only only for a temporary change."
    require_cmd systemctl
}
resolve_network_target() {
    INTERFACE="$INTERFACE_ARG"
    if [[ -z "$INTERFACE" ]]; then
        INTERFACE="$(detect_interface)"
        [[ -n "$INTERFACE" ]] ||
            die "$(msg_no_default_route)"
        log "Auto-detected interface: $INTERFACE"
    fi

    validate_interface "$INTERFACE"

    OLD_GW="$(get_default_gateway)"
    [[ -n "$OLD_GW" ]] || die "$(msg_no_gateway "$INTERFACE")"
    GW="${GATEWAY_ARG:-$OLD_GW}"
    [[ "$NEW_IP" != "$GW" ]] || die "NEW_IP and gateway must differ: $NEW_IP"

    OLD_DEFAULT_METRIC="$(get_default_metric)"
    OLD_DEFAULT_TABLE="$(get_default_table)"

    MASK=""
    if iface_has_ip "$NEW_IP"; then
        MASK="$(get_ip_mask "$NEW_IP")"
        if [[ -n "$PREFIX_ARG" && "$PREFIX_ARG" != "$MASK" ]]; then
            die "$NEW_IP is already configured as /$MASK, not /$PREFIX_ARG"
        fi
    fi

    if [[ -z "$MASK" ]]; then
        [[ -n "$PREFIX_ARG" ]] ||
            die "$(msg_prefix_required "$NEW_IP")"
        MASK="$PREFIX_ARG"
    fi

    OLD_IP="$(get_primary_ip)"
    OLD_SRC="$(get_default_src)"
    OLD_MASK=""
    if [[ -n "$OLD_IP" ]]; then
        OLD_MASK="$(get_ip_mask "$OLD_IP")"
    fi
    [[ -n "$OLD_MASK" ]] || OLD_MASK="$MASK"

    ONLINK=0
    if gateway_needs_onlink; then
        ONLINK=1
    fi

    OLD_ONLINK=""
    if ip -4 route show default dev "$INTERFACE" 2>/dev/null |
        grep -Eq '(^|[[:space:]])onlink([[:space:]]|$)'; then
        OLD_ONLINK=1
    fi
}

collect_runtime_addresses_v4() {
    ip -4 -o addr show dev "$INTERFACE" scope global 2>/dev/null |
        awk '$0 !~ / dynamic / { print $4 }'
}

collect_runtime_addresses_v6() {
    ip -6 -o addr show dev "$INTERFACE" scope global 2>/dev/null |
        awk '$0 !~ / dynamic / && $0 !~ / tentative / && $0 !~ / temporary / { print $4 }'
}

init_tmp_dir() {
    [[ -n "$TMP_DIR" && -d "$TMP_DIR" ]] || TMP_DIR="$(mktemp -d)"
}

route_get_src() {
    local dest="$1"
    ip -4 route get "$dest" oif "$INTERFACE" 2>/dev/null |
        awk '{
            for (i = 1; i <= NF; i++)
                if ($i == "src") {
                    print $(i+1)
                    exit
                }
        }'
}

print_plan() {
    local gateway_label

    if [[ "$GATEWAY_FROM_PROFILE" == "1" ]]; then
        gateway_label="profile"
    elif [[ -n "$GATEWAY_ARG" ]]; then
        gateway_label="explicit"
    else
        gateway_label="preserved"
    fi

    echo
    echo "=============================="
    echo " change_ip v$SCRIPT_VERSION"
    echo "=============================="
    echo " Interface : $INTERFACE"
    echo " Old IP    : ${OLD_IP:-none}/${OLD_MASK:-?}"
    echo " Old source: ${OLD_SRC:-auto}"
    echo " Gateway   : $GW ($gateway_label)"
    echo " New IP    : $NEW_IP/$MASK"
    echo " on-link   : $([[ "$ONLINK" == "1" ]] && echo yes || echo no)"
    echo " Metric    : ${OLD_DEFAULT_METRIC:-default}"
    echo " Persist   : $([[ "$RUNTIME_ONLY" == "1" ]] && echo skipped || echo "$PERSISTENCE")"
    if [[ "$RUNTIME_ONLY" != "1" && "$PERSISTENCE" == "systemd" ]]; then
        echo " Persist how: systemd oneshot after cloud-init/network-online"
        echo " Reboot src : explicit $NEW_IP"
        echo " Reboot GW  : explicit $GW"
    fi
    echo " Mode      : $([[ "$DRY_RUN" == "1" ]] && echo dry-run || echo apply)"
    echo "=============================="
    echo
}

confirm_apply() {
    [[ "$DRY_RUN" == "1" || "$ASSUME_YES" == "1" ]] && return 0

    if [[ ! -t 0 ]]; then
        die "No TTY and --yes not given; refusing to apply network changes non-interactively."
    fi

    local answer
    printf 'Apply network change? [y/N] ' >&2
    read -r answer
    [[ "$answer" =~ ^[Yy]$ ]]
}

gateway_is_preserved() {
    [[ -n "$OLD_GW" && "$GW" == "$OLD_GW" ]]
}

create_backups() {
    ROLLBACK_DIR="$(mktemp -d /root/change-ip-backup.XXXXXX)" ||
        die "Could not create backup directory"

    case "$PERSISTENCE" in
        systemd)
            mkdir -p "$ROLLBACK_DIR/systemd"
            if [[ -n "$SYSTEMD_UNIT" && -f "$SYSTEMD_UNIT" ]]; then
                cp -a "$SYSTEMD_UNIT" "$ROLLBACK_DIR/systemd/"
            fi
            if [[ -n "$SYSTEMD_SCRIPT" && -f "$SYSTEMD_SCRIPT" ]]; then
                cp -a "$SYSTEMD_SCRIPT" "$ROLLBACK_DIR/systemd/"
            fi
            ;;
    esac

    BACKUP_CREATED=1
    write_backup_manifest
    log "Backup directory: $ROLLBACK_DIR"
}

load_backup_manifest() {
    local dir="$1" line key value
    local manifest="$dir/manifest"

    [[ -f "$manifest" ]] || die "Backup manifest not found: $manifest"
    NETPLAN_MANAGED=""

    while IFS= read -r line || [[ -n "$line" ]]; do
        [[ -z "$line" || "$line" =~ ^# ]] && continue
        key="${line%%=*}"
        value="${line#*=}"
        case "$key" in
            persistence) PERSISTENCE="$value" ;;
            interface) INTERFACE="$value" ;;
            netplan_key) NETPLAN_KEY="$value" ;;
            netplan_section) NETPLAN_SECTION="$value" ;;
            netplan_dropin) NETPLAN_DROPIN="$value" ;;
            netplan_managed) NETPLAN_MANAGED="$value" ;;
            systemd_unit) SYSTEMD_UNIT="$value" ;;
            systemd_script) SYSTEMD_SCRIPT="$value" ;;
            old_ip) OLD_IP="$value" ;;
            old_mask) OLD_MASK="$value" ;;
            old_src) OLD_SRC="$value" ;;
            old_gw) OLD_GW="$value" ;;
            old_onlink) OLD_ONLINK="$value" ;;
            old_default_metric) OLD_DEFAULT_METRIC="$value" ;;
            old_default_table) OLD_DEFAULT_TABLE="$value" ;;
            new_added) NEW_ADDED="$value" ;;
            gw_host_route_added) GW_HOST_ROUTE_ADDED="$value" ;;
            new_ip) NEW_IP="$value" ;;
            mask) MASK="$value" ;;
            gateway) GW="$value" ;;
        esac
    done < "$manifest"
}

restore_systemd_from_backup() {
    local dir="$1" f unit script

    unit="${SYSTEMD_UNIT:-/etc/systemd/system/change-ip-${INTERFACE}.service}"
    script="${SYSTEMD_SCRIPT:-/usr/local/lib/change-ip/apply-${INTERFACE}.sh}"

    disable_systemd_persist "$unit" "$script"

    if [[ -d "$dir/systemd" ]]; then
        mkdir -p /usr/local/lib/change-ip /etc/systemd/system
        for f in "$dir/systemd"/*; do
            [[ -f "$f" ]] || continue
            case "$(basename "$f")" in
                *.service) cp -a "$f" "/etc/systemd/system/$(basename "$f")" ;;
                *) cp -a "$f" "/usr/local/lib/change-ip/$(basename "$f")" ;;
            esac
        done
    fi

    if command -v systemctl >/dev/null 2>&1; then
        systemctl daemon-reload || true
        if [[ -f "$unit" ]]; then
            systemctl enable "$(basename "$unit")" >/dev/null 2>&1 || true
        fi
    fi
}

remove_legacy_networkd_dropin() {
    local legacy_dir="/etc/systemd/network/10-netplan-${NETPLAN_KEY}.network.d"
    [[ -d "$legacy_dir" ]] || return 0
    rm -f "$legacy_dir/preferred-source.conf"
    rmdir "$legacy_dir" 2>/dev/null || true
    warn "Removed legacy networkd PreferredSource drop-in: $legacy_dir"
}

restore_netplan_from_backup() {
    local dir="$1"
    local f base legacy_dir

    if [[ -f "$NETPLAN_DROPIN" ]]; then
        rm -f "$NETPLAN_DROPIN"
    else
        rm -f /etc/netplan/zz-change-ip-*.yaml
    fi

    # Legacy v2.2 backups may list a specific networkd drop-in dir in manifest.
    legacy_dir="$(awk -F= '$1 == "networkd_dropin_dir" { print $2; exit }' "$dir/manifest" 2>/dev/null || true)"
    if [[ -n "$legacy_dir" && -d "$legacy_dir" ]]; then
        rm -f "$legacy_dir/preferred-source.conf"
        rmdir "$legacy_dir" 2>/dev/null || true
    fi

    for f in "$dir/netplan"/*; do
        [[ -f "$f" ]] || continue
        base="$(basename "$f")"
        cp -a "$f" "/etc/netplan/$base"
    done

    if command -v netplan >/dev/null 2>&1; then
        netplan generate
        netplan apply
    fi

    command -v networkctl >/dev/null 2>&1 &&
        networkctl reload >/dev/null 2>&1 || true

    restore_systemd_from_backup "$dir"
}

rollback_persistent() {
    [[ "$PERSIST_CHANGED" == "1" && "$BACKUP_CREATED" == "1" ]] || return 0

    warn "Rolling back persistent network configuration..."

    case "$PERSISTENCE" in
        systemd)
            load_backup_manifest "$ROLLBACK_DIR"
            restore_systemd_from_backup "$ROLLBACK_DIR"
            ;;
        netplan)
            load_backup_manifest "$ROLLBACK_DIR"
            if [[ "$NETPLAN_MANAGED" == "1" ||
                  ( -z "$NETPLAN_MANAGED" && -d "$ROLLBACK_DIR/netplan" ) ]]; then
                restore_netplan_from_backup "$ROLLBACK_DIR"
            else
                restore_systemd_from_backup "$ROLLBACK_DIR"
            fi
            ;;
        interfaces)
            load_backup_manifest "$ROLLBACK_DIR"
            restore_interfaces_persist "$ROLLBACK_DIR"
            ;;
    esac
}
rollback_runtime() {
    warn "Attempting runtime network rollback..."

    if [[ -n "${OLD_GW:-}" ]]; then
        local -a del_args=(default via "$OLD_GW" dev "$INTERFACE")
        [[ -n "$OLD_DEFAULT_METRIC" ]] && del_args+=(metric "$OLD_DEFAULT_METRIC")
        [[ -n "$OLD_DEFAULT_TABLE" ]] && del_args+=(table "$OLD_DEFAULT_TABLE")
        ip -4 route del "${del_args[@]}" 2>/dev/null || true
    fi

    if [[ -n "${OLD_IP:-}" && -n "${OLD_MASK:-}" ]]; then
        if ! iface_has_ip "$OLD_IP"; then
            ip -4 addr add "$OLD_IP/$OLD_MASK" dev "$INTERFACE" 2>/dev/null || true
        fi
        local -a restore_args=(default via "$OLD_GW" dev "$INTERFACE" src "${OLD_SRC:-$OLD_IP}")
        [[ -n "$OLD_DEFAULT_METRIC" ]] && restore_args+=(metric "$OLD_DEFAULT_METRIC")
        [[ -n "$OLD_DEFAULT_TABLE" ]] && restore_args+=(table "$OLD_DEFAULT_TABLE")
        [[ -n "$OLD_ONLINK" ]] && restore_args+=(onlink)
        ip -4 route replace "${restore_args[@]}" 2>/dev/null || true
    fi

    if [[ "${NEW_ADDED:-0}" == "1" && -n "${NEW_IP:-}" ]]; then
        ip -4 addr del "$NEW_IP/$MASK" dev "$INTERFACE" 2>/dev/null || true
    fi

    if [[ "$GW_HOST_ROUTE_ADDED" == "1" ]]; then
        ip -4 route del "$GW/32" dev "$INTERFACE" scope link 2>/dev/null || true
    fi

    rollback_persistent
}

ensure_gateway_reachable() {
    local route_to_gw
    route_to_gw="$(ip -4 route get "$GW" oif "$INTERFACE" 2>/dev/null || true)"

    if [[ "$route_to_gw" == *"dev $INTERFACE"* &&
          "$route_to_gw" != *" via "* ]]; then
        return 0
    fi

    if ip -4 route show dev "$INTERFACE" scope link 2>/dev/null |
        grep -Fq "$GW/32"; then
        return 0
    fi

    ip -4 route replace "$GW/32" dev "$INTERFACE" scope link
    GW_HOST_ROUTE_ADDED=1
}

ensure_default_src() {
    local src="$1"
    local -a extra=() add_args=() del_args=()

    ensure_gateway_reachable

    if gateway_needs_onlink; then
        extra+=(onlink)
    fi

    add_args=(default via "$GW" dev "$INTERFACE" src "$src")
    [[ -n "$OLD_DEFAULT_METRIC" ]] && add_args+=(metric "$OLD_DEFAULT_METRIC")
    [[ -n "$OLD_DEFAULT_TABLE" ]] && add_args+=(table "$OLD_DEFAULT_TABLE")
    add_args+=("${extra[@]}")

    if [[ "$GW" != "$OLD_GW" && -n "$OLD_GW" ]]; then
        del_args=(default via "$OLD_GW" dev "$INTERFACE")
        [[ -n "$OLD_DEFAULT_METRIC" ]] && del_args+=(metric "$OLD_DEFAULT_METRIC")
        [[ -n "$OLD_DEFAULT_TABLE" ]] && del_args+=(table "$OLD_DEFAULT_TABLE")
        ip -4 route del "${del_args[@]}" 2>/dev/null || true
    fi

    ip -4 route replace "${add_args[@]}"
}

apply_runtime() {
    log "Applying runtime address and route..."

    NEW_ADDED=0
    if ! iface_has_ip "$NEW_IP"; then
        ip -4 addr add "$NEW_IP/$MASK" dev "$INTERFACE"
        NEW_ADDED=1
        log "Added $NEW_IP/$MASK"
    else
        log "$NEW_IP already exists on $INTERFACE"
    fi

    RUNTIME_CHANGED=1

    ONLINK=0
    if gateway_needs_onlink; then
        ONLINK=1
    fi

    log "Keeping all existing addresses and their individual prefixes"
    log "Preparing gateway reachability..."
    ensure_gateway_reachable

    log "Setting default route source to $NEW_IP..."
    ensure_default_src "$NEW_IP"
}


persist_systemd() {
    SYSTEMD_SCRIPT="/usr/local/lib/change-ip/apply-${INTERFACE}.sh"
    SYSTEMD_UNIT="/etc/systemd/system/change-ip-${INTERFACE}.service"

    PERSISTENCE="systemd"
    require_cmd systemctl
    create_backups

    PERSIST_CHANGED=1
    write_systemd_apply_script
    write_systemd_unit

    systemctl daemon-reload
    systemctl enable "$(basename "$SYSTEMD_UNIT")"

    write_backup_manifest
    ensure_default_src "$NEW_IP"
    ok "Persistence: $SYSTEMD_UNIT (enabled for next boot)"
}

write_systemd_apply_script() {
    local addr host prefix onlink_flag=""
    mkdir -p "$(dirname "$SYSTEMD_SCRIPT")"

    [[ "$ONLINK" == "1" ]] && onlink_flag=" onlink"
    local metric_flag="" table_flag=""
    [[ -n "$OLD_DEFAULT_METRIC" ]] && metric_flag=" metric $OLD_DEFAULT_METRIC"
    [[ -n "$OLD_DEFAULT_TABLE" ]] && table_flag=" table $OLD_DEFAULT_TABLE"

    cat > "$SYSTEMD_SCRIPT" <<EOF
#!/bin/bash
# Generated by $SCRIPT_NAME — do not edit.
set -euo pipefail
IFACE=$(printf '%q' "$INTERFACE")
SRC=$(printf '%q' "$NEW_IP")
GW=$(printf '%q' "$GW")

for _ in \$(seq 1 30); do
    ip link show "\$IFACE" 2>/dev/null | grep -q 'state UP' && break
    sleep 1
done
EOF

    while IFS= read -r addr; do
        [[ -n "$addr" ]] || continue
        host="${addr%/*}"
        prefix="${addr#*/}"
        printf 'ip -4 addr replace %q dev "$IFACE" || true\n' "${host}/${prefix}" >> "$SYSTEMD_SCRIPT"
    done < <(collect_runtime_addresses_v4)

    cat >> "$SYSTEMD_SCRIPT" <<EOF
ip -4 route replace "\$GW/32" dev "\$IFACE" scope link
ip -4 route replace default via "\$GW" dev "\$IFACE" src "\$SRC"${onlink_flag}${metric_flag}${table_flag}
EOF

    chmod 755 "$SYSTEMD_SCRIPT"
    log "Systemd apply script: $SYSTEMD_SCRIPT"
}

write_systemd_unit() {
    cat > "$SYSTEMD_UNIT" <<EOF
[Unit]
Description=change_ip preferred source IP on ${INTERFACE}
DefaultDependencies=yes
After=network-online.target networking.service cloud-init.service
Wants=network-online.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=${SYSTEMD_SCRIPT}

[Install]
WantedBy=multi-user.target
EOF
    log "Systemd unit: $SYSTEMD_UNIT"
}

disable_systemd_persist() {
    local unit="${1:-$SYSTEMD_UNIT}"
    local script="${2:-$SYSTEMD_SCRIPT}"
    local name

    if [[ -n "$unit" ]]; then
        name="$(basename "$unit")"
        if command -v systemctl >/dev/null 2>&1; then
            systemctl disable --now "$name" >/dev/null 2>&1 || true
        fi
        rm -f "$unit"
    fi
    [[ -n "$script" ]] && rm -f "$script"
}

restore_interfaces_persist() {
    local dir="$1"
    local f dest

    for f in "$dir/interfaces"/*; do
        [[ -f "$f" ]] || continue
        if [[ "$(basename "$f")" == "interfaces" ]]; then
            dest="$INTERFACES_FILE"
        else
            dest="/etc/network/interfaces.d/$(basename "$f")"
            mkdir -p /etc/network/interfaces.d
        fi
        cp -a "$f" "$dest"
    done

    restore_systemd_from_backup "$dir"
}



apply_persistence() {
    [[ "$RUNTIME_ONLY" == "1" ]] && {
        warn "Skipping persistence (--runtime-only)"
        return 0
    }

    case "$PERSISTENCE" in
        systemd)
            persist_systemd
            ;;
        *)
            die "No supported persistence backend. Use --runtime-only only for a temporary change."
            ;;
    esac
}

verify_network() {
    local route_src_1111 route_src_gw def_src ext_ip gw_lookup

    VERIFY_FAILED=0

    echo
    echo "=============================="
    echo " Verification"
    echo "=============================="
    echo

    echo "IPv4 on $INTERFACE:"
    ip -4 addr show dev "$INTERFACE"
    echo

    echo "Default route:"
    ip -4 route show default dev "$INTERFACE" || true
    echo

    echo "Route to 1.1.1.1:"
    ip -4 route get 1.1.1.1 oif "$INTERFACE" || true
    echo

    echo "Route to gateway $GW:"
    ip -4 route get "$GW" oif "$INTERFACE" || true
    echo

    route_src_1111="$(route_get_src 1.1.1.1)"
    route_src_gw="$(route_get_src "$GW")"
    def_src="$(get_default_src)"

    if ! iface_has_ip "$NEW_IP"; then
        warn "NEW_IP $NEW_IP is not configured on $INTERFACE"
        VERIFY_FAILED=1
    else
        ok "New IP is configured"
    fi

    if [[ "$route_src_1111" != "$NEW_IP" ]]; then
        warn "ip route get 1.1.1.1 source is '${route_src_1111:-unknown}', expected $NEW_IP"
        VERIFY_FAILED=1
    else
        ok "ip route get 1.1.1.1 → src $NEW_IP"
    fi

    if [[ "$route_src_gw" != "$NEW_IP" ]]; then
        gw_lookup="$(ip -4 route get "$GW" oif "$INTERFACE" 2>/dev/null || true)"
        if [[ "$gw_lookup" == *" via "* ]]; then
            warn "ip route get $GW source is '${route_src_gw:-unknown}', expected $NEW_IP"
            VERIFY_FAILED=1
        else
            # On-link gateway: kernel picks primary / longest local prefix
            # (e.g. 62.192.153.104/27 beats .105/24 for .97). Outbound uses default src.
            warn "ip route get $GW src is '${route_src_gw:-unknown}' (on-link; kernel may keep the primary/more-specific address). Outbound 1.1.1.1 already uses $NEW_IP — not a failure."
        fi
    else
        ok "ip route get $GW → src $NEW_IP"
    fi

    if [[ -n "$def_src" && "$def_src" != "$NEW_IP" ]]; then
        warn "Default route src is '$def_src' (expected $NEW_IP); kernel may omit src when unambiguous"
    elif [[ -z "$def_src" ]]; then
        warn "Default route has no explicit src (may be normal when only one address matches)"
    else
        ok "Default route src is $NEW_IP"
    fi

    if [[ "$CHECK_EGRESS" == "1" ]]; then
        echo
        echo "Egress checks (--check-egress):"
        echo "Route to 8.8.8.8:"
        ip -4 route get 8.8.8.8 oif "$INTERFACE" || true

        if ping -c 2 -W 2 -I "$NEW_IP" "$GW" >/dev/null 2>&1 ||
           ping -c 2 -W 2 "$GW" >/dev/null 2>&1; then
            ok "Gateway $GW reachable"
        else
            warn "Gateway $GW does not respond to ping"
        fi

        ext_ip=""
        if command -v curl >/dev/null 2>&1; then
            ext_ip="$(
                curl -4 -fsS --interface "$NEW_IP" --max-time 10 \
                    https://ifconfig.me 2>/dev/null || true
            )"
        fi

        if [[ -n "$ext_ip" ]]; then
            ok "External IP: $ext_ip"
            if [[ "$ext_ip" != "$NEW_IP" ]]; then
                warn "External IP differs from NEW_IP (NAT/provider policy?)"
            fi
        else
            warn "Could not determine external IP"
        fi
    fi
}

cmd_rollback() {
    local dir="$1"

    [[ -d "$dir" ]] || die "Backup directory not found: $dir"
    load_backup_manifest "$dir"

    log "Restoring from backup: $dir (persistence=$PERSISTENCE)"

    case "$PERSISTENCE" in
        systemd)
            restore_systemd_from_backup "$dir"
            ok "Systemd persistence restored from backup"
            ;;
        netplan)
            if [[ "$NETPLAN_MANAGED" == "1" ||
                  ( -z "$NETPLAN_MANAGED" && -d "$dir/netplan" ) ]]; then
                require_cmd netplan
                restore_netplan_from_backup "$dir"
                ok "Netplan restored from backup"
            else
                restore_systemd_from_backup "$dir"
                ok "Systemd persistence restored from backup"
            fi
            ;;
        interfaces)
            [[ -d "$dir/interfaces" ]] || die "Interfaces backup missing in $dir"
            restore_interfaces_persist "$dir"
            if command -v systemctl >/dev/null 2>&1; then
                systemctl daemon-reload || true
            fi
            ok "Interfaces/systemd persist restored from backup"
            ;;
        *)
            die "Unknown or missing persistence in manifest: ${PERSISTENCE:-unset}"
            ;;
    esac
    if [[ -n "$OLD_GW" && -n "$OLD_IP" ]]; then
        PERSIST_CHANGED=0
        RUNTIME_CHANGED=1
        rollback_runtime
        ok "Current runtime network restored"
    else
        warn "Backup has no runtime snapshot; persistent files were restored, but current network was not changed."
    fi

    ok "Rollback completed from $dir"
}

write_backup_manifest() {
    local gp=no
    [[ -n "$ROLLBACK_DIR" && -d "$ROLLBACK_DIR" ]] || return 0
    gateway_is_preserved && gp=yes
    cat > "$ROLLBACK_DIR/manifest" <<EOF
# change_ip backup manifest
version=1
created=$(date -Iseconds)
persistence=$PERSISTENCE
interface=$INTERFACE
netplan_key=${NETPLAN_KEY:-}
netplan_section=${NETPLAN_SECTION:-}
netplan_dropin=${NETPLAN_DROPIN:-}
netplan_managed=${NETPLAN_MANAGED:-0}
new_ip=$NEW_IP
mask=$MASK
gateway=$GW
gateway_preserved=$gp
old_ip=${OLD_IP:-}
old_mask=${OLD_MASK:-}
old_src=${OLD_SRC:-}
old_gw=${OLD_GW:-}
old_onlink=${OLD_ONLINK:-}
old_default_metric=${OLD_DEFAULT_METRIC:-}
old_default_table=${OLD_DEFAULT_TABLE:-}
new_added=${NEW_ADDED:-0}
gw_host_route_added=${GW_HOST_ROUTE_ADDED:-0}
persist_strategy=systemd_oneshot
systemd_unit=${SYSTEMD_UNIT:-}
systemd_script=${SYSTEMD_SCRIPT:-}
script_version=$SCRIPT_VERSION
EOF
}

########################################################################
# User interface (wizard / status / doctor / rollback menu).
# Collects arguments and calls the same core functions. Does not replace
# apply_runtime / persist_* / rollback / lock.
########################################################################

CHANGE_IP_REEXEC_ARGS=()

require_root_ui() {
    [[ "$(id -u)" -eq 0 ]] || die "$(msg_need_root)"
}

ui_str() {
    local key="$1"
    if [[ "$UI_LANG" == ru ]]; then
        case "$key" in
            wizard_need_tty) printf '%s' "Нет TTY. Запустите sudo $SCRIPT_NAME из терминала или укажите IP: sudo $SCRIPT_NAME NEW_IP/PREFIX --gateway GW" ;;
            eof_abort) printf '%s' "Ввод прерван." ;;
            no_systemd_run) printf '%s' "systemd-run не найден. SSH может отвалиться при смене маршрута. Продолжаем без обёртки." ;;
            iface_pick) printf '%s' "Номер интерфейса или Enter" ;;
            iface_invalid) printf '%s' "Нет такого пункта. Введите номер из списка." ;;
            ask_which_ip) printf '%s' "Какой IPv4 сделать исходящим?" ;;
            current_src) printf '%s' "сейчас исходящий" ;;
            add_from_panel) printf '%s' "Добавить IP из панели" ;;
            pick_prompt) printf '%s' "Номер: " ;;
            add_ip_prompt) printf '%s' "IPv4 из панели: " ;;
            ip_invalid) printf '%s' "Нужен обычный публичный IPv4, без маски. Пример: 193.42.11.28" ;;
            prefix_prompt) printf '%s' "Префикс (24, 25 или 32): " ;;
            prefix_invalid) printf '%s' "Префикс — число от 1 до 32. Примеры: 24, 25, 32." ;;
            gateway_prompt) printf '%s' "Шлюз из панели: " ;;
            gateway_empty) printf '%s' "Пустой ввод нельзя: текущий шлюз не из этой подсети." ;;
            gateway_invalid) printf '%s' "Нужен IPv4 шлюза из панели. Пример: 193.42.11.1" ;;
            gateway_same) printf '%s' "Шлюз и NEW_IP не должны совпадать." ;;
            confirm_apply) printf '%s' "Применить? [y/N]" ;;
            aborted) printf '%s' "Отменено." ;;
            verify_failed) printf '%s' "Проверка не прошла; runtime и persist откатил." ;;
            reboot_now) printf '%s' "Перезагрузить сейчас? [y/N]" ;;
            reboot_skipped) printf '%s' "Reboot отложен. Persist применится после следующей перезагрузки." ;;
            rollback_pick) printf '%s' "Какой бэкап восстановить?" ;;
            no_backups) printf '%s' "Нет копий в /root/change-ip-backup.*. Сначала выполните смену IP." ;;
            see_doctor) printf '%s' "сервис не стартовал, см. sudo $SCRIPT_NAME doctor" ;;
            on_nic_yes) printf '%s' "да" ;;
            on_nic_no) printf '%s' "нет" ;;
            *) printf '%s' "$key" ;;
        esac
    else
        case "$key" in
            wizard_need_tty) printf '%s' "No TTY. Run sudo $SCRIPT_NAME from a terminal, or pass an IP: sudo $SCRIPT_NAME NEW_IP/PREFIX --gateway GW" ;;
            eof_abort) printf '%s' "Input ended." ;;
            no_systemd_run) printf '%s' "systemd-run not found. SSH may drop when the route changes. Continuing without a wrapper." ;;
            iface_pick) printf '%s' "Interface number or Enter" ;;
            iface_invalid) printf '%s' "Not in the list. Enter a listed number." ;;
            ask_which_ip) printf '%s' "Which IPv4 should outbound traffic use?" ;;
            current_src) printf '%s' "current source" ;;
            add_from_panel) printf '%s' "Add an IP from the panel" ;;
            pick_prompt) printf '%s' "Number: " ;;
            add_ip_prompt) printf '%s' "IPv4 from the panel: " ;;
            ip_invalid) printf '%s' "Need a unicast IPv4 host address, no mask. Example: 193.42.11.28" ;;
            prefix_prompt) printf '%s' "Prefix (24, 25 or 32): " ;;
            prefix_invalid) printf '%s' "Prefix is 1-32. Examples: 24, 25, 32." ;;
            gateway_prompt) printf '%s' "Gateway from the panel: " ;;
            gateway_empty) printf '%s' "Cannot press Enter: the current gateway is not in this subnet." ;;
            gateway_invalid) printf '%s' "Need the gateway IPv4 from the panel. Example: 193.42.11.1" ;;
            gateway_same) printf '%s' "Gateway and NEW_IP must differ." ;;
            confirm_apply) printf '%s' "Apply? [y/N]" ;;
            aborted) printf '%s' "Aborted." ;;
            verify_failed) printf '%s' "Verification failed; runtime and persist were rolled back." ;;
            reboot_now) printf '%s' "Reboot now? [y/N]" ;;
            reboot_skipped) printf '%s' "Reboot skipped. Persist applies on the next reboot." ;;
            rollback_pick) printf '%s' "Which backup should be restored?" ;;
            no_backups) printf '%s' "No copies in /root/change-ip-backup.*. Change an IP first." ;;
            see_doctor) printf '%s' "unit did not start, see sudo $SCRIPT_NAME doctor" ;;
            on_nic_yes) printf '%s' "yes" ;;
            on_nic_no) printf '%s' "no" ;;
            *) printf '%s' "$key" ;;
        esac
    fi
}

ui() {
    local key="$1"
    shift
    local fmt
    fmt="$(ui_str "$key")"
    printf '%s\n' "$fmt"
}

script_self_path() {
    local src="${BASH_SOURCE[0]:-$0}"
    if command -v readlink >/dev/null 2>&1; then
        readlink -f "$src" 2>/dev/null && return 0
    fi
    local dir
    dir="$(cd "$(dirname "$src")" && pwd)"
    printf '%s/%s\n' "$dir" "$(basename "$src")"
}

maybe_reexec_systemd_run() {
    [[ "${CHANGE_IP_SYSTEMD_RUN:-}" == 1 ]] && return 0
    [[ -n "${INVOCATION_ID:-}" ]] && return 0
    [[ -t 0 && -t 1 ]] || return 0

    if ! command -v systemd-run >/dev/null 2>&1; then
        warn "$(ui_str no_systemd_run)"
        return 0
    fi

    local self
    self="$(script_self_path)"
    export CHANGE_IP_SYSTEMD_RUN=1
    if (( ${#CHANGE_IP_REEXEC_ARGS[@]} > 0 )); then
        exec systemd-run --wait --pty --same-dir \
            --setenv=CHANGE_IP_SYSTEMD_RUN=1 \
            "$self" "${CHANGE_IP_REEXEC_ARGS[@]}"
    fi
    exec systemd-run --wait --pty --same-dir \
        --setenv=CHANGE_IP_SYSTEMD_RUN=1 \
        "$self"
}

wizard_require_tty() {
    [[ -t 0 && -t 1 ]] || die "$(ui_str wizard_need_tty)"
}

wizard_read() {
    local _w
    if ! IFS= read -r _w; then
        die "$(ui_str eof_abort)"
    fi
    REPLY="$_w"
}

wizard_yes_no() {
    local prompt="$1"
    printf '%s ' "$prompt" >&2
    wizard_read
    case "$REPLY" in
        y|Y|yes|YES|да|Да|д|Д) return 0 ;;
        *) return 1 ;;
    esac
}

iface_is_offered() {
    local if="$1" iftype
    [[ -n "$if" ]] || return 1
    [[ "$if" == lo ]] && return 1
    is_virtual_interface_name "$if" && return 1
    case "$if" in
        docker*|br-*|veth*|virbr*|cni*|flannel*|cbr*|kube*|nerdctl*)
            return 1
            ;;
    esac
    iftype="$(cat "/sys/class/net/$if/type" 2>/dev/null || echo 0)"
    [[ "$iftype" != "772" ]] || return 1
    return 0
}

list_offered_ifaces() {
    local path if
    for path in /sys/class/net/*; do
        if="$(basename "$path")"
        iface_is_offered "$if" || continue
        if ! ip -4 -o addr show dev "$if" scope global 2>/dev/null | grep -q .; then
            continue
        fi
        printf '%s\n' "$if"
    done
}

list_iface_addrs_v4() {
    ip -4 -o addr show dev "$INTERFACE" scope global 2>/dev/null |
        awk '{ print $4 }'
}

list_backup_dirs() {
    find /root -maxdepth 1 -mindepth 1 -type d -name 'change-ip-backup.*' \
        -printf '%T@\t%p\n' 2>/dev/null | sort -nr | cut -f2-
}

route_get_via() {
    local dest="$1"
    ip -4 route get "$dest" oif "$INTERFACE" 2>/dev/null |
        awk '{
            for (i = 1; i <= NF; i++)
                if ($i == "via") {
                    print $(i+1)
                    exit
                }
        }'
}

parse_apply_script_src() {
    local f="$1"
    [[ -f "$f" ]] || return 1
    awk -F= '/^SRC=/ {
        gsub(/['\''"]/, "", $2)
        print $2
        exit
    }' "$f"
}

parse_apply_script_gw() {
    local f="$1"
    [[ -f "$f" ]] || return 1
    awk -F= '/^GW=/ {
        gsub(/['\''"]/, "", $2)
        print $2
        exit
    }' "$f"
}

netplan_dropin_first_addr() {
    local f="$1" line
    [[ -f "$f" ]] || return 1
    while IFS= read -r line; do
        [[ "$line" =~ ^[[:space:]]*-[[:space:]]*([0-9]+\.[0-9]+\.[0-9]+\.[0-9]+/[0-9]+) ]] || continue
        printf '%s\n' "${BASH_REMATCH[1]}"
        return 0
    done < "$f"
    return 1
}

unit_has_ordering_cycle() {
    local name="$1"
    if journalctl -b -u "$name" --no-pager 2>/dev/null |
        grep -qiE 'ordering cycle|transaction order is cyclic'; then
        return 0
    fi
    if journalctl -b --no-pager 2>/dev/null |
        grep -F "$name" |
        grep -qiE 'ordering cycle|transaction order is cyclic'; then
        return 0
    fi
    return 1
}

require_common_cmds() {
    require_cmd awk
    require_cmd grep
    require_cmd find
    require_cmd sed
    require_cmd ip
}

wizard_pick_interface() {
    local detected="" choice i
    local -a candidates=()

    if [[ -n "$INTERFACE_ARG" ]]; then
        INTERFACE="$INTERFACE_ARG"
        validate_interface "$INTERFACE"
        return 0
    fi

    detected="$(detect_interface || true)"
    while IFS= read -r i; do
        [[ -n "$i" ]] || continue
        candidates+=("$i")
    done < <(list_offered_ifaces)

    if [[ -n "$detected" ]] && iface_is_offered "$detected"; then
        echo
        if [[ "$UI_LANG" == ru ]]; then
            printf 'Интерфейс исходящего трафика: %s (default route).\n' "$detected"
            printf 'docker/wg/tun в список не входят.\n'
        else
            printf 'Outbound interface: %s (default route).\n' "$detected"
            printf 'docker/wg/tun are not offered.\n'
        fi
        for i in "${!candidates[@]}"; do
            printf '  %d) %s' "$((i + 1))" "${candidates[i]}"
            [[ "${candidates[i]}" == "$detected" ]] && printf ' *'
            printf '\n'
        done
        printf '%s (= %s): ' "$(ui_str iface_pick)" "$detected"
        while true; do
            wizard_read
            choice="$REPLY"
            if [[ -z "$choice" ]]; then
                INTERFACE="$detected"
                break
            elif [[ "$choice" =~ ^[1-9][0-9]*$ ]] &&
                 (( choice >= 1 && choice <= ${#candidates[@]} )); then
                INTERFACE="${candidates[choice-1]}"
                break
            elif iface_is_offered "$choice" &&
                 ip link show "$choice" >/dev/null 2>&1; then
                INTERFACE="$choice"
                break
            else
                ui iface_invalid
                printf '%s (= %s): ' "$(ui_str iface_pick)" "$detected"
            fi
        done
    else
        if (( ${#candidates[@]} == 0 )); then
            die "$(msg_no_default_route)"
        fi
        if (( ${#candidates[@]} == 1 )); then
            INTERFACE="${candidates[0]}"
            echo
            if [[ "$UI_LANG" == ru ]]; then
                printf 'Интерфейс: %s\n' "$INTERFACE"
            else
                printf 'Interface: %s\n' "$INTERFACE"
            fi
        else
            echo
            if [[ "$UI_LANG" == ru ]]; then
                printf 'Нет однозначного uplink. Выберите интерфейс с белым IP:\n'
            else
                printf 'Uplink is ambiguous. Pick the interface with the public IP:\n'
            fi
            for i in "${!candidates[@]}"; do
                printf '  %d) %s\n' "$((i + 1))" "${candidates[i]}"
            done
            printf '%s' "$(ui_str pick_prompt)"
            while true; do
                wizard_read
                choice="$REPLY"
                if [[ "$choice" =~ ^[1-9][0-9]*$ ]] &&
                   (( choice >= 1 && choice <= ${#candidates[@]} )); then
                    INTERFACE="${candidates[choice-1]}"
                    break
                fi
                ui iface_invalid
                printf '%s' "$(ui_str pick_prompt)"
            done
        fi
    fi

    INTERFACE_ARG="$INTERFACE"
    validate_interface "$INTERFACE"
}

wizard_pick_ip() {
    local -a addrs=()
    local line host i choice add_n cur_src

    while IFS= read -r line; do
        [[ -n "$line" ]] || continue
        addrs+=("$line")
    done < <(list_iface_addrs_v4)

    cur_src="$(get_default_src)"
    [[ -n "$cur_src" ]] || cur_src="$(route_get_src 1.1.1.1 || true)"

    echo
    ui ask_which_ip
    i=1
    for line in "${addrs[@]}"; do
        host="${line%/*}"
        printf '  %d) %s' "$i" "$line"
        [[ "$host" == "$cur_src" ]] && printf '  (%s)' "$(ui_str current_src)"
        printf '\n'
        i=$((i + 1))
    done
    add_n=$i
    printf '  %d) %s\n' "$add_n" "$(ui_str add_from_panel)"
    while true; do
        printf '%s' "$(ui_str pick_prompt)"
        wizard_read
        choice="$REPLY"

        if [[ "$choice" == "$add_n" ]]; then
            while true; do
                printf '%s' "$(ui_str add_ip_prompt)"
                wizard_read
                NEW_IP="$REPLY"
                is_usable_ipv4 "$NEW_IP" && return 0
                ui ip_invalid
            done
        fi

        if [[ "$choice" =~ ^[1-9][0-9]*$ ]] &&
           (( choice >= 1 && choice < add_n )); then
            line="${addrs[choice-1]}"
            NEW_IP="${line%/*}"
            PREFIX_ARG="${line#*/}"
            return 0
        fi

        ui iface_invalid
    done
}

wizard_ask_prefix() {
    local p
    echo
    if [[ "$UI_LANG" == ru ]]; then
        cat <<EOF
Префикс (маска) из панели. В панели часто пусто. Соседний IP с маской
255.255.255.0 → /24. Шлюз .129 при адресах .128–.255 часто /25.
Если не уверены — у провайдера. Скрипт маску не угадывает.
EOF
    else
        cat <<EOF
Prefix (mask) from the panel. The panel often leaves it blank. A neighbour
IP with mask 255.255.255.0 → /24. Gateway .129 with addresses .128–.255 is
often /25. If unsure, ask the provider. This script never guesses the mask.
EOF
    fi
    while true; do
        printf '%s' "$(ui_str prefix_prompt)"
        wizard_read
        p="${REPLY#/}"
        if is_prefix "$p"; then
            PREFIX_ARG="$p"
            return 0
        fi
        ui prefix_invalid
    done
}

wizard_ask_gateway_if_needed() {
    local old_gw="${OLD_GW:-}"

    if [[ -n "$old_gw" ]] && prefix_contains "$old_gw" "$NEW_IP/$MASK"; then
        return 0
    fi

    echo
    if [[ "$UI_LANG" == ru ]]; then
        cat <<EOF
Новая подсеть: шлюз текущего default (${old_gw:-нет}) не в $NEW_IP/$MASK.
Нужен шлюз из панели. Иначе пакеты не выйдут.
Enter = текущий шлюз здесь нельзя.
EOF
    else
        cat <<EOF
New subnet: the current default gateway (${old_gw:-none}) is not in
$NEW_IP/$MASK. Enter the gateway from the panel. Otherwise packets will
not leave. Pressing Enter to keep the current gateway is not allowed.
EOF
    fi

    while true; do
        printf '%s' "$(ui_str gateway_prompt)"
        wizard_read
        if [[ -z "$REPLY" ]]; then
            ui gateway_empty
            continue
        fi
        if ! is_usable_ipv4 "$REPLY"; then
            ui gateway_invalid
            continue
        fi
        if [[ "$REPLY" == "$NEW_IP" ]]; then
            ui gateway_same
            continue
        fi
        GATEWAY_ARG="$REPLY"
        return 0
    done
}

print_human_plan() {
    echo
    echo "=============================="
    if [[ "$UI_LANG" == ru ]]; then
        printf 'Исходящий IP станет %s.\n' "$NEW_IP"
        if [[ "$GW" != "$OLD_GW" ]]; then
            printf 'Шлюз %s (сейчас %s).\n' "$GW" "$OLD_GW"
            printf 'Риск: смена шлюза. Если GW из панели неверный, сеть может пропасть;\n'
            printf 'при ошибке проверки скрипт откатит runtime.\n'
        else
            printf 'Шлюз останется %s.\n' "$GW"
        fi
        printf 'Старые адреса на %s останутся.\n' "$INTERFACE"
        case "$PERSISTENCE" in
            systemd)
                printf 'Persist: systemd после загрузки выставит src=%s и gateway=%s.\n' "$NEW_IP" "$GW"
                ;;
            *)
                printf 'Persist не найден: после reboot исходящий IP может вернуться.\n'
                ;;
        esac
        if [[ -n "${INVOCATION_ID:-}" || "${CHANGE_IP_SYSTEMD_RUN:-}" == 1 ]]; then
            printf 'SSH не должен отвалиться (запуск в systemd-run).\n'
        else
            printf 'SSH может отвалиться: нет обёртки systemd-run.\n'
        fi
    else
        printf 'Outbound IP will be %s.\n' "$NEW_IP"
        if [[ "$GW" != "$OLD_GW" ]]; then
            printf 'Gateway %s (currently %s).\n' "$GW" "$OLD_GW"
            printf 'Risk: gateway change. A wrong panel GW can drop the network;\n'
            printf 'failed verification rolls runtime back.\n'
        else
            printf 'Gateway stays %s.\n' "$GW"
        fi
        printf 'Existing addresses on %s stay.\n' "$INTERFACE"
        case "$PERSISTENCE" in
            systemd)
                printf 'Persist: after boot, systemd sets src=%s and gateway=%s.\n' "$NEW_IP" "$GW"
                ;;
            *)
                printf 'No persist backend: outbound IP may revert after reboot.\n'
                ;;
        esac
        if [[ -n "${INVOCATION_ID:-}" || "${CHANGE_IP_SYSTEMD_RUN:-}" == 1 ]]; then
            printf 'SSH should stay up (running inside systemd-run).\n'
        else
            printf 'SSH may drop: systemd-run wrapper is not active.\n'
        fi
    fi
    echo "=============================="
    echo
}

print_post_reboot_checks() {
    echo
    if [[ "$UI_LANG" == ru ]]; then
        echo "После reboot проверьте:"
    else
        echo "After reboot, check:"
    fi
    printf '  ip -4 route get 1.1.1.1\n'
    printf '  ip -4 addr show dev %s\n' "$INTERFACE"
    printf '  sudo %s status\n' "$SCRIPT_NAME"
    echo
}

print_wizard_success() {
    echo
    echo "======================================"
    if [[ "$UI_LANG" == ru ]]; then
        ok "Сейчас исходящий уже $NEW_IP"
        printf 'Интерфейс : %s\n' "$INTERFACE"
        printf 'Шлюз      : %s\n' "$GW"
        printf 'Persist   : %s\n' "$PERSISTENCE"
        [[ -n "$ROLLBACK_DIR" ]] && printf 'Бэкап     : %s\n' "$ROLLBACK_DIR"
    else
        ok "Outbound source is already $NEW_IP"
        printf 'Interface : %s\n' "$INTERFACE"
        printf 'Gateway   : %s\n' "$GW"
        printf 'Persist   : %s\n' "$PERSISTENCE"
        [[ -n "$ROLLBACK_DIR" ]] && printf 'Backup    : %s\n' "$ROLLBACK_DIR"
    fi
    echo "======================================"
}

wizard_offer_reboot() {
    echo
    if [[ "$PERSISTENCE" == "none" ]]; then
        if [[ "$UI_LANG" == ru ]]; then
            echo "Persist не найден: после reboot исходящий IP может вернуться."
        else
            echo "No persistence backend: outbound IP may revert after reboot."
        fi
        return 0
    fi

    if [[ "$UI_LANG" == ru ]]; then
        echo "Настройки сохранены. Скрипт не выполняет reboot автоматически."
        echo "После ручного reboot проверьте: sudo $SCRIPT_NAME status"
    else
        echo "Settings are saved. The script never reboots automatically."
        echo "After a manual reboot, check: sudo $SCRIPT_NAME status"
    fi
}
cmd_wizard() {
    detect_ui_lang
    require_root_ui
    require_common_cmds
    wizard_require_tty
    maybe_reexec_systemd_run
    acquire_lock

    wizard_pick_interface

    OLD_GW="$(get_default_gateway)"
    [[ -n "$OLD_GW" ]] || die "$(msg_no_gateway "$INTERFACE")"

    wizard_pick_ip

    if iface_has_ip "$NEW_IP"; then
        MASK="$(get_ip_mask "$NEW_IP")"
        PREFIX_ARG="${PREFIX_ARG:-$MASK}"
        echo
        if [[ "$UI_LANG" == ru ]]; then
            printf '%s уже на %s как /%s — префикс не спрашиваю.\n' \
                "$NEW_IP" "$INTERFACE" "$MASK"
        else
            printf '%s is already on %s as /%s — not asking for a prefix.\n' \
                "$NEW_IP" "$INTERFACE" "$MASK"
        fi
    else
        load_address_profile
        if [[ -z "$PREFIX_ARG" ]]; then
            wizard_ask_prefix
        fi
        MASK="$PREFIX_ARG"
    fi

    if [[ -z "$GATEWAY_ARG" ]]; then
        wizard_ask_gateway_if_needed
    fi

    resolve_network_target
    preflight_persistence
    print_human_plan

    wizard_yes_no "$(ui_str confirm_apply)" || die "$(ui_str aborted)"

    apply_runtime
    apply_persistence

    log "Reasserting runtime source route..."
    ensure_default_src "$NEW_IP"

    verify_network

    if [[ "$VERIFY_FAILED" == "1" ]]; then
        ROLLBACK_ATTEMPTED=1
        rollback_runtime || true
        die "$(ui_str verify_failed)"
    fi

    print_wizard_success
    print_post_reboot_checks
    wizard_offer_reboot
}

cmd_status() {
    local src via on_nic mask_now unit_file unit_name script_file dropin
    local enabled active sub desired_src first_addr cycle_note persist_txt
    local expect_txt

    detect_ui_lang
    require_root_ui
    require_cmd ip

    INTERFACE="${1:-}"
    if [[ -n "$INTERFACE" ]]; then
        INTERFACE_ARG="$INTERFACE"
    else
        INTERFACE="$(detect_interface || true)"
    fi
    [[ -n "$INTERFACE" ]] || die "$(msg_no_default_route)"
    validate_interface "$INTERFACE"

    src="$(route_get_src 1.1.1.1 || true)"
    via="$(route_get_via 1.1.1.1 || true)"
    [[ -n "$via" ]] || via="$(get_default_gateway || true)"

    on_nic="$(ui_str on_nic_no)"
    mask_now=""
    if [[ -n "$src" ]] && iface_has_ip "$src"; then
        on_nic="$(ui_str on_nic_yes)"
        mask_now="$(get_ip_mask "$src")"
    fi

    detect_persistence_backend
    unit_file="/etc/systemd/system/change-ip-${INTERFACE}.service"
    unit_name="change-ip-${INTERFACE}.service"
    script_file="/usr/local/lib/change-ip/apply-${INTERFACE}.sh"
    dropin="/etc/netplan/zz-change-ip-${INTERFACE}.yaml"
    SYSTEMD_UNIT="$unit_file"
    SYSTEMD_SCRIPT="$script_file"
    NETPLAN_DROPIN="$dropin"

    enabled=""
    active=""
    sub=""
    cycle_note=""
    if [[ -f "$unit_file" ]]; then
        enabled="$(systemctl is-enabled "$unit_name" 2>/dev/null || true)"
        active="$(systemctl is-active "$unit_name" 2>/dev/null || true)"
        sub="$(systemctl show -p SubState --value "$unit_name" 2>/dev/null || true)"
        if [[ "$enabled" == enabled && "$active" == inactive ]] &&
           unit_has_ordering_cycle "$unit_name"; then
            cycle_note="$(ui_str see_doctor)"
        fi
    fi

    desired_src="$(parse_apply_script_src "$script_file" || true)"
    first_addr="$(netplan_dropin_first_addr "$dropin" || true)"

    persist_txt=""
    if [[ -f "$dropin" ]]; then
        persist_txt="netplan $dropin"
    fi
    if [[ -f "$unit_file" ]]; then
        if [[ -n "$persist_txt" ]]; then
            persist_txt="$persist_txt; $unit_name ${enabled:-?} / ${active:-?} ${sub:+($sub)}"
        else
            persist_txt="$unit_name ${enabled:-?} / ${active:-?} ${sub:+($sub)}"
        fi
    fi
    if [[ -z "$persist_txt" ]]; then
        persist_txt="$PERSISTENCE (файлов change_ip нет)"
        [[ "$UI_LANG" == ru ]] || persist_txt="$PERSISTENCE (no change_ip persist files)"
    fi

    if [[ -n "$desired_src" ]]; then
        if [[ "$UI_LANG" == ru ]]; then
            expect_txt="systemd $unit_name выставит src=$desired_src"
        else
            expect_txt="systemd $unit_name should set src=$desired_src"
        fi
    elif [[ -n "$first_addr" ]]; then
        if [[ "$UI_LANG" == ru ]]; then
            expect_txt="legacy drop-in address ($first_addr); current source route needs systemd unit"
        else
            expect_txt="legacy drop-in address ($first_addr); current source route needs systemd unit"
        fi
    else
        if [[ "$UI_LANG" == ru ]]; then
            expect_txt="как в cloud-init / текущей сетевой конфигурации"
        else
            expect_txt="whatever cloud-init / current network config provides"
        fi
    fi

    echo
    echo "=============================="
    echo " change_ip status"
    echo "=============================="
    if [[ "$UI_LANG" == ru ]]; then
        printf 'Интерфейс     : %s\n' "$INTERFACE"
        printf 'Исходящий src : %s\n' "${src:-неизвестно}"
        printf 'На %s        : %s%s\n' "$INTERFACE" "$on_nic" \
            "${mask_now:+ (/ $mask_now)}"
        printf 'route 1.1.1.1 : src %s via %s\n' "${src:-?}" "${via:-?}"
        printf 'Шлюз          : %s\n' "${via:-$(get_default_gateway || true)}"
        printf 'Persist       : %s\n' "$persist_txt"
        printf 'После reboot  : %s\n' "$expect_txt"
    else
        printf 'Interface     : %s\n' "$INTERFACE"
        printf 'Outbound src  : %s\n' "${src:-unknown}"
        printf 'On %s        : %s%s\n' "$INTERFACE" "$on_nic" \
            "${mask_now:+ (/ $mask_now)}"
        printf 'route 1.1.1.1 : src %s via %s\n' "${src:-?}" "${via:-?}"
        printf 'Gateway       : %s\n' "${via:-$(get_default_gateway || true)}"
        printf 'Persist       : %s\n' "$persist_txt"
        printf 'After reboot  : %s\n' "$expect_txt"
    fi
    [[ -n "$cycle_note" ]] && printf 'Внимание      : %s\n' "$cycle_note"
    echo "=============================="
    echo
}

cmd_doctor() {
    local unit_file unit_name script_file dropin enabled active sub src desired gw_script
    local -a units=()
    local u name already x

    detect_ui_lang
    require_root_ui
    require_cmd ip

    INTERFACE="${1:-}"
    if [[ -z "$INTERFACE" ]]; then
        INTERFACE="$(detect_interface || true)"
    fi

    echo
    echo "=============================="
    echo " change_ip doctor"
    echo "=============================="

    if [[ -n "$INTERFACE" && -f "/etc/systemd/system/change-ip-${INTERFACE}.service" ]]; then
        units+=("/etc/systemd/system/change-ip-${INTERFACE}.service")
    fi

    for u in /etc/systemd/system/change-ip-*.service; do
        [[ -f "$u" ]] || continue
        already=0
        for x in "${units[@]}"; do
            [[ "$x" == "$u" ]] && already=1
        done
        if (( already == 1 )); then
            continue
        fi
        units+=("$u")
    done

    if (( ${#units[@]} == 0 )); then
        if [[ -z "$INTERFACE" ]]; then
            echo "$(msg_no_default_route)"
        fi
        if [[ "$UI_LANG" == ru ]]; then
            echo "Нет unit change-ip-*.service."
            echo "Текущая версия сохраняет IP и маршрут через systemd на Debian и Ubuntu."
            echo "Запустите sudo $SCRIPT_NAME снова, чтобы создать persist."
        else
            echo "No change-ip-*.service unit."
            echo "The current version saves IP and route through systemd on Debian and Ubuntu."
            echo "Run sudo $SCRIPT_NAME again to create persistence."
        fi
    fi

    for unit_file in "${units[@]}"; do
        [[ -f "$unit_file" ]] || continue
        unit_name="$(basename "$unit_file")"
        name="${unit_name#change-ip-}"
        name="${name%.service}"
        script_file="/usr/local/lib/change-ip/apply-${name}.sh"
        dropin="/etc/netplan/zz-change-ip-${name}.yaml"

        enabled="$(systemctl is-enabled "$unit_name" 2>/dev/null || echo unknown)"
        active="$(systemctl is-active "$unit_name" 2>/dev/null || echo unknown)"
        sub="$(systemctl show -p SubState --value "$unit_name" 2>/dev/null || true)"
        desired="$(parse_apply_script_src "$script_file" || true)"
        gw_script="$(parse_apply_script_gw "$script_file" || true)"

        INTERFACE="$name"
        src="$(route_get_src 1.1.1.1 || true)"

        echo
        printf 'unit          : %s\n' "$unit_name"
        printf 'is-enabled    : %s\n' "$enabled"
        printf 'is-active     : %s\n' "$active"
        printf 'SubState      : %s\n' "${sub:-?}"
        if unit_has_ordering_cycle "$unit_name"; then
            if [[ "$UI_LANG" == ru ]]; then
                echo "ordering cycle: да — сервис не стартовал."
                echo "  Не ставьте After=cloud-final вместе с WantedBy=multi-user (цикл)."
                echo "  Текущий unit должен быть After=network-online.target networking.service cloud-init.service"
            else
                echo "ordering cycle: yes — the unit did not start."
                echo "  Do not use After=cloud-final with WantedBy=multi-user (cycle)."
                echo "  This unit should use After=network-online.target networking.service cloud-init.service"
            fi
        else
            echo "ordering cycle: no"
        fi

        if [[ -f "$script_file" ]]; then
            printf 'apply script  : %s (src=%s gw=%s)\n' \
                "$script_file" "${desired:-?}" "${gw_script:-?}"
            if [[ -n "$desired" ]]; then
                INTERFACE="$name"
                if iface_has_ip "$desired"; then
                    if [[ "$UI_LANG" == ru ]]; then
                        printf 'IP на iface   : %s есть на %s\n' "$desired" "$name"
                    else
                        printf 'IP on iface   : %s is on %s\n' "$desired" "$name"
                    fi
                else
                    if [[ "$UI_LANG" == ru ]]; then
                        printf 'IP на iface   : %s НЕТ на %s — добавьте в панели и снова sudo %s\n' \
                            "$desired" "$name" "$SCRIPT_NAME"
                    else
                        printf 'IP on iface   : %s NOT on %s — add it in the panel and run sudo %s again\n' \
                            "$desired" "$name" "$SCRIPT_NAME"
                    fi
                fi
                if [[ -n "$src" && "$src" != "$desired" ]]; then
                    if [[ "$UI_LANG" == ru ]]; then
                        printf 'сейчас src    : %s (скрипт хочет %s) — persist после reboot не совпал.\n' \
                            "$src" "$desired"
                    else
                        printf 'current src   : %s (script wants %s) — persist after reboot did not match.\n' \
                            "$src" "$desired"
                    fi
                elif [[ "$src" == "$desired" ]]; then
                    if [[ "$UI_LANG" == ru ]]; then
                        printf 'сейчас src    : %s — совпадает со скриптом.\n' "$src"
                    else
                        printf 'current src   : %s — matches the apply script.\n' "$src"
                    fi
                fi
            fi
        else
            if [[ "$UI_LANG" == ru ]]; then
                printf 'apply script  : нет (%s) — запустите мастер снова.\n' "$script_file"
            else
                printf 'apply script  : missing (%s) — run the wizard again.\n' "$script_file"
            fi
        fi

        echo
        echo "journalctl -u $unit_name -b:"
        journalctl -u "$unit_name" -b --no-pager -n 40 2>/dev/null ||
            echo "(journal empty or unavailable)"
    done

    dropin="/etc/netplan/zz-change-ip-${INTERFACE:-*}.yaml"
    echo
    if compgen -G '/etc/netplan/zz-change-ip-*.yaml' >/dev/null 2>&1; then
        echo "netplan drop-ins:"
        ls -l /etc/netplan/zz-change-ip-*.yaml 2>/dev/null || true
        for dropin in /etc/netplan/zz-change-ip-*.yaml; do
            [[ -f "$dropin" ]] || continue
            printf '  %s first addr: %s\n' "$dropin" \
                "$(netplan_dropin_first_addr "$dropin" || echo '?')"
        done
        if [[ "$UI_LANG" == ru ]]; then
            echo "Legacy Netplan drop-in found; current source route is applied by systemd unit."
        else
            echo "Legacy Netplan drop-in found; current source route is applied by the systemd unit."
        fi
    fi
    echo "=============================="
    echo
}

cmd_rollback_menu() {
    local -a dirs=()
    local d i choice created nip

    detect_ui_lang
    require_root_ui
    require_cmd ip
    require_cmd find

    if [[ -n "${1:-}" ]]; then
        maybe_reexec_systemd_run
        acquire_lock
        cmd_rollback "$1"
        return 0
    fi

    wizard_require_tty
    maybe_reexec_systemd_run
    acquire_lock

    while IFS= read -r d; do
        [[ -n "$d" ]] || continue
        dirs+=("$d")
    done < <(list_backup_dirs)

    if (( ${#dirs[@]} == 0 )); then
        die "$(ui_str no_backups)"
    fi

    echo
    ui rollback_pick
    i=1
    for d in "${dirs[@]}"; do
        created=""
        nip=""
        if [[ -f "$d/manifest" ]]; then
            created="$(awk -F= '$1 == "created" { print $2; exit }' "$d/manifest")"
            nip="$(awk -F= '$1 == "new_ip" { print $2; exit }' "$d/manifest")"
        fi
        printf '  %d) %s' "$i" "$d"
        [[ -n "$created" ]] && printf '  %s' "$created"
        [[ -n "$nip" ]] && printf '  IP %s' "$nip"
        printf '\n'
        i=$((i + 1))
    done
    printf '%s' "$(ui_str pick_prompt)"
    while true; do
        wizard_read
        choice="$REPLY"
        if [[ "$choice" =~ ^[1-9][0-9]*$ ]] &&
           (( choice >= 1 && choice <= ${#dirs[@]} )); then
            cmd_rollback "${dirs[choice-1]}"
            return 0
        fi
        ui iface_invalid
        printf '%s' "$(ui_str pick_prompt)"
    done
}

print_apply_success() {
    echo
    echo "======================================"
    ok "IP change completed successfully"
    echo "======================================"
    echo "Primary source IP : $NEW_IP"
    echo "Interface         : $INTERFACE"
    echo "Gateway           : $GW"
    echo "Persistence       : $PERSISTENCE"
    [[ -n "$ROLLBACK_DIR" ]] && echo "Backup            : $ROLLBACK_DIR"
    echo
}

cmd_apply_cli() {
    parse_args "$@"
    require_common_cmds
    require_root_ui

    if [[ "$ROLLBACK_MODE" == "1" ]]; then
        maybe_reexec_systemd_run
        acquire_lock
        cmd_rollback "$ROLLBACK_SOURCE"
        exit 0
    fi

    if [[ "$DRY_RUN" != "1" ]]; then
        maybe_reexec_systemd_run
    fi

    acquire_lock

    resolve_network_target
    preflight_persistence
    print_plan

    if [[ "$DRY_RUN" == "1" ]]; then
        ok "Dry-run complete; no changes applied."
        exit 0
    fi

    confirm_apply || die "Aborted by user."

    apply_runtime
    apply_persistence

    log "Reasserting runtime source route..."
    ensure_default_src "$NEW_IP"

    verify_network

    if [[ "$VERIFY_FAILED" == "1" ]]; then
        ROLLBACK_ATTEMPTED=1
        rollback_runtime || true
        die "Verification failed; runtime and persistent changes were rolled back."
    fi

    print_apply_success
}

main() {
    detect_ui_lang
    CHANGE_IP_REEXEC_ARGS=("$@")

    case "${1:-}" in
        ""|wizard)
            if [[ "${1:-}" == wizard ]]; then
                shift
            fi
            cmd_wizard "$@"
            ;;
        status)
            shift
            cmd_status "$@"
            ;;
        doctor)
            shift
            cmd_doctor "$@"
            ;;
        rollback)
            shift
            cmd_rollback_menu "$@"
            ;;
        apply)
            shift
            cmd_apply_cli "$@"
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        --tui)
            shift
            if [[ "$UI_LANG" == ru ]]; then
                echo "TUI пока не включён — текстовый мастер."
            else
                echo "TUI is not enabled yet — using the text wizard."
            fi
            cmd_wizard "$@"
            ;;
        *)
            cmd_apply_cli "$@"
            ;;
    esac
}

main "$@"
