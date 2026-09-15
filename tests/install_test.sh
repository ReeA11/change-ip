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
PREFIX="$prefix" "$root/install.sh" "$root/dist/change-ip-linux-amd64"
test "$("$prefix/bin/change-ip" --version)" = "$("$root/dist/change-ip-linux-amd64" --version)"
test -L "$prefix/bin/change_ip"
test -L "$prefix/sbin/change-ip"
test -L "$prefix/sbin/change_ip"
test "$(readlink -f "$prefix/sbin/change_ip")" = "$prefix/bin/change-ip"
echo "installer migration test: OK"
