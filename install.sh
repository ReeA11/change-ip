#!/bin/sh
# Install or upgrade a prebuilt ChangeIP Go binary. Never changes the network.
set -eu

PREFIX=${PREFIX:-/usr/local}
REPOSITORY=${CHANGE_IP_REPOSITORY:-ReeA11/change-ip}
VERSION=${CHANGE_IP_VERSION:-latest}
MAN_FILE=${CHANGE_IP_MAN_FILE:-}
tmp=
new_binary=

log() { printf '[change-ip installer] %s\n' "$*"; }
die() { printf '[change-ip installer] ERROR: %s\n' "$*" >&2; exit 1; }
cleanup() { [ -z "$new_binary" ] || rm -f -- "$new_binary"; [ -z "$tmp" ] || rm -rf -- "$tmp"; }
trap cleanup EXIT HUP INT TERM

if [ "$PREFIX" = /usr/local ] && [ "$(id -u)" -ne 0 ]; then
  die "Run as root: sudo ./install.sh"
fi

case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) die "Unsupported architecture: $(uname -m)" ;;
esac

artifact="change-ip-linux-$arch"
root=$(CDPATH= cd -- "$(dirname -- "$0")" 2>/dev/null && pwd || true)
source_binary=${1:-}
if [ -z "$source_binary" ]; then
  for candidate in "$root/$artifact" "$root/dist/$artifact" "$root/change-ip"; do
    if [ -f "$candidate" ]; then source_binary=$candidate; break; fi
  done
fi

checksum_value() {
  awk -v f="$1" '$2 == f || $2 == "*" f { print $1; exit }' "$2"
}

if [ -z "$source_binary" ]; then
  command -v curl >/dev/null 2>&1 || die "Required command not found: curl"
  tmp=$(mktemp -d)
  if [ "$VERSION" = latest ]; then
    base="https://github.com/$REPOSITORY/releases/latest/download"
  else
    base="https://github.com/$REPOSITORY/releases/download/$VERSION"
  fi
  log "Downloading $artifact from $base"
  curl -fL "$base/$artifact" -o "$tmp/$artifact"
  curl -fL "$base/checksums.txt" -o "$tmp/checksums.txt"
  expected=$(checksum_value "$artifact" "$tmp/checksums.txt")
  [ -n "$expected" ] || die "No checksum for $artifact"
  actual=$(sha256sum "$tmp/$artifact" | awk '{print $1}')
  [ "$actual" = "$expected" ] || die "Checksum mismatch for $artifact"
  source_binary="$tmp/$artifact"

  if curl -fL "$base/change-ip.8" -o "$tmp/change-ip.8"; then
    expected_man=$(checksum_value change-ip.8 "$tmp/checksums.txt")
    actual_man=$(sha256sum "$tmp/change-ip.8" | awk '{print $1}')
    if [ -n "$expected_man" ] && [ "$actual_man" = "$expected_man" ]; then
      MAN_FILE="$tmp/change-ip.8"
    else
      log "WARNING: man page checksum unavailable or invalid"
    fi
  fi
fi

[ -f "$source_binary" ] || die "Binary not found: $source_binary"
magic=$(od -An -tx1 -N4 "$source_binary" 2>/dev/null | tr -d ' \n')
[ "$magic" = 7f454c46 ] || die "$source_binary is not an ELF binary"
chmod 0755 "$source_binary"
candidate_version=$("$source_binary" --version 2>/dev/null || true)
candidate_major=$(printf '%s\n' "$candidate_version" | sed -n 's/^[^0-9]*\([0-9][0-9]*\)\..*/\1/p')
[ -n "$candidate_major" ] && [ "$candidate_major" -ge 3 ] || die "Expected ChangeIP >=3.0, got: ${candidate_version:-unknown}"

install_dir="$PREFIX/bin"
canonical="$install_dir/change-ip"
install -d -m 0755 "$PREFIX/bin" "$PREFIX/sbin" "$PREFIX/share/man/man8"
new_binary="$install_dir/.change-ip.new.$$"
install -m 0755 "$source_binary" "$new_binary"
[ "$("$new_binary" --version)" = "$candidate_version" ] || die "Installed candidate failed version verification"
mv -f -- "$new_binary" "$canonical"
new_binary=
log "Atomically installed ChangeIP $candidate_version: $canonical"

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
    log "Removing legacy ChangeIP command: $legacy"
    rm -f -- "$legacy"
  elif [ -e "$legacy" ] || [ -L "$legacy" ]; then
    case "$legacy" in
      "$PREFIX/bin/change_ip"|"$PREFIX/sbin/change-ip"|"$PREFIX/sbin/change_ip") rm -f -- "$legacy" ;;
      *) log "WARNING: unrelated or unrecognized file left untouched: $legacy" ;;
    esac
  fi
}

for legacy in "$PREFIX/sbin/change-ip" "$PREFIX/bin/change_ip" "$PREFIX/sbin/change_ip"; do
  migrate_legacy_path "$legacy"
done
if [ "$PREFIX" = /usr/local ]; then
  for legacy in /usr/bin/change-ip /usr/sbin/change-ip /usr/bin/change_ip /usr/sbin/change_ip; do
    migrate_legacy_path "$legacy"
  done
fi

ln -s change-ip "$PREFIX/bin/change_ip"
ln -s ../bin/change-ip "$PREFIX/sbin/change-ip"
ln -s ../bin/change-ip "$PREFIX/sbin/change_ip"

if [ -z "$MAN_FILE" ] && [ -f "$root/man/change_ip.8" ]; then MAN_FILE="$root/man/change_ip.8"; fi
if [ -n "$MAN_FILE" ] && [ -f "$MAN_FILE" ]; then install -m 0644 "$MAN_FILE" "$PREFIX/share/man/man8/change-ip.8"; fi

"$canonical" --version >/dev/null
log "Commands: change-ip, change_ip"
log "Network was not changed. Run: sudo change-ip"
