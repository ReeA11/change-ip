#!/bin/sh
# Install or upgrade a prebuilt ChangeIP Go binary. Never changes the network.
set -eu

PREFIX=${PREFIX:-/usr/local}
REPOSITORY=${CHANGE_IP_REPOSITORY:-ReeA11/change-ip}
VERSION=${CHANGE_IP_VERSION:-latest}
MAN_FILE=${CHANGE_IP_MAN_FILE:-}
LANGUAGE=${CHANGE_IP_LANGUAGE:-}
LANGUAGE_FILE=${CHANGE_IP_LANGUAGE_FILE:-/etc/change-ip/language}
tmp=
new_binary=
new_language=

log() { printf '%s %s\n' "${INSTALLER_LABEL:-[change-ip installer]}" "$*"; }
die() { printf '%s %s: %s\n' "${INSTALLER_LABEL:-[change-ip installer]}" "${ERROR_LABEL:-ERROR}" "$*" >&2; exit 1; }
message() { if [ "$LANGUAGE" = ru ]; then printf '%s' "$2"; else printf '%s' "$1"; fi; }
cleanup() { [ -z "$new_binary" ] || rm -f -- "$new_binary"; [ -z "$new_language" ] || rm -f -- "$new_language"; [ -z "$tmp" ] || rm -rf -- "$tmp"; }
trap cleanup EXIT HUP INT TERM

source_binary=
while [ "$#" -gt 0 ]; do
  case "$1" in
    --language)
      [ "$#" -ge 2 ] || die "$(message '--language requires en or ru' 'для --language требуется en или ru')"
      LANGUAGE=$2
      shift 2
      ;;
    --language=*)
      LANGUAGE=${1#*=}
      shift
      ;;
    *)
      [ -z "$source_binary" ] || die "$(message 'Unexpected argument' 'Неожиданный аргумент'): $1"
      source_binary=$1
      shift
      ;;
  esac
done

case "$LANGUAGE" in
  ""|en) ;;
  ru)
    INSTALLER_LABEL='[установщик change-ip]'
    ERROR_LABEL='ОШИБКА'
    ;;
  *) die "Unsupported language: $LANGUAGE" ;;
esac

if [ "$PREFIX" = /usr/local ] && [ "$(id -u)" -ne 0 ]; then
  die "$(message 'Run as root: sudo ./install.sh' 'Запустите от root: sudo ./install.sh')"
fi

case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) die "$(message 'Unsupported architecture' 'Неподдерживаемая архитектура'): $(uname -m)" ;;
esac

artifact="change-ip-linux-$arch"
root=$(CDPATH= cd -- "$(dirname -- "$0")" 2>/dev/null && pwd || true)
if [ -z "$source_binary" ]; then
  for candidate in "$root/$artifact" "$root/dist/$artifact" "$root/change-ip"; do
    if [ -f "$candidate" ]; then source_binary=$candidate; break; fi
  done
fi

checksum_value() {
  awk -v f="$1" '$2 == f || $2 == "*" f { print $1; exit }' "$2"
}

if [ -z "$source_binary" ]; then
  command -v curl >/dev/null 2>&1 || die "$(message 'Required command not found: curl' 'Не найдена обязательная команда: curl')"
  tmp=$(mktemp -d)
  if [ "$VERSION" = latest ]; then
    base="https://github.com/$REPOSITORY/releases/latest/download"
  else
    base="https://github.com/$REPOSITORY/releases/download/$VERSION"
  fi
  log "$(message 'Downloading' 'Загрузка') $artifact $(message 'from' 'из') $base"
  curl -fL "$base/$artifact" -o "$tmp/$artifact"
  curl -fL "$base/checksums.txt" -o "$tmp/checksums.txt"
  expected=$(checksum_value "$artifact" "$tmp/checksums.txt")
  [ -n "$expected" ] || die "$(message 'No checksum for' 'Нет контрольной суммы для') $artifact"
  actual=$(sha256sum "$tmp/$artifact" | awk '{print $1}')
  [ "$actual" = "$expected" ] || die "$(message 'Checksum mismatch for' 'Контрольная сумма не совпадает для') $artifact"
  source_binary="$tmp/$artifact"

  if curl -fL "$base/change-ip.8" -o "$tmp/change-ip.8"; then
    expected_man=$(checksum_value change-ip.8 "$tmp/checksums.txt")
    actual_man=$(sha256sum "$tmp/change-ip.8" | awk '{print $1}')
    if [ -n "$expected_man" ] && [ "$actual_man" = "$expected_man" ]; then
      MAN_FILE="$tmp/change-ip.8"
    else
      log "$(message 'WARNING: man page checksum unavailable or invalid' 'ПРЕДУПРЕЖДЕНИЕ: контрольная сумма man-страницы отсутствует или неверна')"
    fi
  fi
fi

[ -f "$source_binary" ] || die "$(message 'Binary not found' 'Бинарный файл не найден'): $source_binary"
magic=$(od -An -tx1 -N4 "$source_binary" 2>/dev/null | tr -d ' \n')
[ "$magic" = 7f454c46 ] || die "$source_binary $(message 'is not an ELF binary' 'не является ELF-бинарником')"
chmod 0755 "$source_binary"
candidate_version=$("$source_binary" --version 2>/dev/null || true)
candidate_major=$(printf '%s\n' "$candidate_version" | sed -n 's/^[^0-9]*\([0-9][0-9]*\)\..*/\1/p')
[ -n "$candidate_major" ] && [ "$candidate_major" -ge 3 ] || die "$(message 'Expected ChangeIP >=3.0, got' 'Ожидался ChangeIP >=3.0, получено'): ${candidate_version:-unknown}"

install_dir="$PREFIX/bin"
canonical="$install_dir/change-ip"
install -d -m 0755 "$PREFIX/bin" "$PREFIX/sbin" "$PREFIX/share/man/man8"
new_binary="$install_dir/.change-ip.new.$$"
install -m 0755 "$source_binary" "$new_binary"
[ "$("$new_binary" --version)" = "$candidate_version" ] || die "$(message 'Installed candidate failed version verification' 'Установочный файл не прошёл проверку версии')"
mv -f -- "$new_binary" "$canonical"
new_binary=
log "$(message 'Atomically installed' 'Атомарно установлен') ChangeIP $candidate_version: $canonical"

if [ -n "$LANGUAGE" ]; then
  language_dir=${LANGUAGE_FILE%/*}
  install -d -m 0755 "$language_dir"
  new_language="$language_dir/.language.new.$$"
  printf '%s\n' "$LANGUAGE" > "$new_language"
  chmod 0644 "$new_language"
  mv -f -- "$new_language" "$LANGUAGE_FILE"
  new_language=
fi

is_legacy_change_ip() {
  path=$1
  [ -e "$path" ] || [ -L "$path" ] || return 1
  if [ -L "$path" ]; then
    resolved=$(readlink -f "$path" 2>/dev/null || true)
    [ -n "$resolved" ] && [ -f "$resolved" ] || return 1
    path=$resolved
  fi
  file_magic=$(od -An -tx1 -N4 "$path" 2>/dev/null | tr -d ' \n')
  if [ "$file_magic" != 7f454c46 ]; then
    head -n 8 "$path" 2>/dev/null | grep -Eq 'change_ip|ChangeIP|SCRIPT_VERSION' && return 0
    return 1
  fi
  old_version=$("$path" --version 2>/dev/null || true)
  old_major=$(printf '%s\n' "$old_version" | sed -n 's/^[^0-9]*\([0-9][0-9]*\)\..*/\1/p')
  [ -n "$old_major" ] && [ "$old_major" -lt 3 ]
}

migrate_legacy_path() {
  legacy=$1
  [ "$legacy" = "$canonical" ] && return 0
  if is_legacy_change_ip "$legacy"; then
    log "$(message 'Removing legacy ChangeIP command' 'Удаление старой команды ChangeIP'): $legacy"
    rm -f -- "$legacy"
  elif [ -e "$legacy" ] || [ -L "$legacy" ]; then
    case "$legacy" in
      "$PREFIX/bin/change_ip"|"$PREFIX/sbin/change-ip"|"$PREFIX/sbin/change_ip") rm -f -- "$legacy" ;;
      *) log "$(message 'WARNING: unrelated or unrecognized file left untouched' 'ПРЕДУПРЕЖДЕНИЕ: посторонний или неизвестный файл оставлен без изменений'): $legacy" ;;
    esac
  fi
}

for legacy in "$PREFIX/bin/change_ip" "$PREFIX/sbin/change_ip"; do
  if [ -e "$legacy" ] || [ -L "$legacy" ]; then
    log "$(message 'Removing deprecated ChangeIP command' 'Удаление устаревшей команды ChangeIP'): $legacy"
    rm -f -- "$legacy"
  fi
done
migrate_legacy_path "$PREFIX/sbin/change-ip"
if [ "$PREFIX" = /usr/local ]; then
  for legacy in /usr/bin/change_ip /usr/sbin/change_ip; do
    if [ -e "$legacy" ] || [ -L "$legacy" ]; then
      log "$(message 'Removing deprecated ChangeIP command' 'Удаление устаревшей команды ChangeIP'): $legacy"
      rm -f -- "$legacy"
    fi
  done
  for legacy in /usr/bin/change-ip /usr/sbin/change-ip; do
    migrate_legacy_path "$legacy"
  done
fi

ln -s ../bin/change-ip "$PREFIX/sbin/change-ip"

if [ -z "$MAN_FILE" ] && [ -f "$root/man/change_ip.8" ]; then MAN_FILE="$root/man/change_ip.8"; fi
if [ -n "$MAN_FILE" ] && [ -f "$MAN_FILE" ]; then install -m 0644 "$MAN_FILE" "$PREFIX/share/man/man8/change-ip.8"; fi

"$canonical" --version >/dev/null
log "$(message 'Command: change-ip' 'Команда: change-ip')"
log "$(message 'Network was not changed. Run: sudo change-ip' 'Сеть не изменялась. Запустите: sudo change-ip')"
