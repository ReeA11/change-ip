#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmp=$(mktemp -d)
cleanup() { rm -rf -- "$tmp"; }
trap cleanup EXIT HUP INT TERM
prefix="$tmp/prefix"
mkdir -p "$prefix/sbin"
printf '%s\n' '#!/bin/sh' 'SCRIPT_VERSION="2.6.0"' > "$prefix/sbin/change_ip"
chmod 0755 "$prefix/sbin/change_ip"
language_file="$tmp/language"
PREFIX="$prefix" CHANGE_IP_LANGUAGE_FILE="$language_file" "$root/install.sh" --language ru "$root/dist/change-ip-linux-amd64"
test "$("$prefix/bin/change-ip" --version)" = "$("$root/dist/change-ip-linux-amd64" --version)"
test -L "$prefix/sbin/change-ip"
test ! -e "$prefix/bin/change_ip"
test ! -L "$prefix/bin/change_ip"
test ! -e "$prefix/sbin/change_ip"
test ! -L "$prefix/sbin/change_ip"
test "$(readlink -f "$prefix/sbin/change-ip")" = "$prefix/bin/change-ip"
test "$(cat "$language_file")" = ru
PREFIX="$prefix" CHANGE_IP_LANGUAGE_FILE="$language_file" "$root/install.sh" --language en "$root/dist/change-ip-linux-amd64"
test "$(cat "$language_file")" = en
echo "installer migration test: OK"
