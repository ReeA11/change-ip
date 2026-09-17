#!/bin/sh
set -eu

tmp=$(mktemp -d)
cleanup() { rm -rf -- "$tmp"; }
trap cleanup EXIT HUP INT TERM

curl -fsSL https://raw.githubusercontent.com/ReeA11/change-ip/master/install.sh -o "$tmp/install.sh"
sh "$tmp/install.sh" --language en
