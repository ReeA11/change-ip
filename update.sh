#!/bin/sh
# Bootstrap/update ChangeIP from the latest checksum-verified GitHub release.
set -eu

REPOSITORY=${CHANGE_IP_REPOSITORY:-ReeA11/change-ip}
VERSION=${CHANGE_IP_VERSION:-latest}
tmp=$(mktemp -d)
cleanup() { rm -rf -- "$tmp"; }
trap cleanup EXIT HUP INT TERM

if [ "$(id -u)" -ne 0 ]; then
  echo "Run as root: sudo ./update.sh" >&2
  exit 1
fi
command -v curl >/dev/null 2>&1 || { echo "Required command not found: curl" >&2; exit 1; }

case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo "Unsupported architecture: $(uname -m)" >&2; exit 2 ;;
esac

if [ "$VERSION" = latest ]; then
  base="https://github.com/$REPOSITORY/releases/latest/download"
else
  base="https://github.com/$REPOSITORY/releases/download/$VERSION"
fi
artifact="change-ip-linux-$arch"

echo "[change-ip updater] Downloading the latest verified release..."
curl -fL "$base/checksums.txt" -o "$tmp/checksums.txt"
for file in "$artifact" install.sh change-ip.8; do curl -fL "$base/$file" -o "$tmp/$file"; done

for file in "$artifact" install.sh change-ip.8; do
  expected=$(awk -v f="$file" '$2 == f || $2 == "*" f { print $1; exit }' "$tmp/checksums.txt")
  [ -n "$expected" ] || { echo "Missing checksum for $file" >&2; exit 1; }
  actual=$(sha256sum "$tmp/$file" | awk '{print $1}')
  [ "$actual" = "$expected" ] || { echo "Checksum mismatch for $file" >&2; exit 1; }
done

chmod 0755 "$tmp/$artifact" "$tmp/install.sh"
CHANGE_IP_MAN_FILE="$tmp/change-ip.8" sh "$tmp/install.sh" "$tmp/$artifact"

# Keep this post-condition in the updater too. It also cleans underscore aliases
# when this updater is used to bootstrap from an older release installer.
target_prefix=${PREFIX:-/usr/local}
for deprecated in "$target_prefix/bin/change_ip" "$target_prefix/sbin/change_ip"; do
  if [ -e "$deprecated" ] || [ -L "$deprecated" ]; then
    echo "[change-ip updater] Removing deprecated ChangeIP command: $deprecated"
    rm -f -- "$deprecated"
  fi
done
if [ "$target_prefix" = /usr/local ]; then
  for deprecated in /usr/bin/change_ip /usr/sbin/change_ip; do
    if [ -e "$deprecated" ] || [ -L "$deprecated" ]; then
      echo "[change-ip updater] Removing deprecated ChangeIP command: $deprecated"
      rm -f -- "$deprecated"
    fi
  done
fi
