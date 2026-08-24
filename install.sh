#!/bin/bash
# Install change_ip to PREFIX (default /usr/local). Does not change network.
# Local use: sudo ./install.sh
# Piped use: curl -fsSL https://raw.githubusercontent.com/ReeA11/change-ip/master/install.sh | sudo bash
set -euo pipefail

PREFIX="${PREFIX:-/usr/local}"
# It can still be overridden by CHANGE_IP_SOURCE_URL or the first
# argument in a piped install.
DEFAULT_REMOTE_SCRIPT_URL="https://raw.githubusercontent.com/ReeA11/change-ip/master/change_ip.sh"
REMOTE_SCRIPT_URL="${CHANGE_IP_SOURCE_URL:-${1:-$DEFAULT_REMOTE_SCRIPT_URL}}"
MAN_URL="${CHANGE_IP_MAN_URL:-}"
SOURCE_PATH="${BASH_SOURCE[0]:-}"
TMP_DIR=""

if [[ "$(id -u)" -ne 0 ]]; then
    printf 'Run as root, for example: sudo %s\n' "$0" >&2
    exit 1
fi

cleanup() {
    if [[ -n "$TMP_DIR" && -d "$TMP_DIR" ]]; then
        rm -rf "$TMP_DIR"
    fi
}
trap cleanup EXIT

SCRIPT_PATH=""
if [[ -n "$SOURCE_PATH" && -f "$SOURCE_PATH" ]]; then
    LOCAL_ROOT="$(cd "$(dirname "$SOURCE_PATH")" && pwd)"
    if [[ -f "$LOCAL_ROOT/change_ip.sh" ]]; then
        SCRIPT_PATH="$LOCAL_ROOT/change_ip.sh"
        if [[ -z "$MAN_URL" && -f "$LOCAL_ROOT/man/change_ip.8" ]]; then
            MAN_URL="file:$LOCAL_ROOT/man/change_ip.8"
        fi
    fi
fi

if [[ -z "$SCRIPT_PATH" ]]; then
    if [[ -z "$REMOTE_SCRIPT_URL" ]]; then
        printf 'Piped installation needs the raw URL of change_ip.sh.\n' >&2
        printf 'Pass it as the first argument or set CHANGE_IP_SOURCE_URL.\n' >&2
        exit 2
    fi
    case "$REMOTE_SCRIPT_URL" in
        https://*/change_ip.sh|file://*/change_ip.sh) ;;
        *)
            printf 'The source URL must point to change_ip.sh: %s\n' "$REMOTE_SCRIPT_URL" >&2
            exit 2
            ;;
    esac
    if [[ -z "$MAN_URL" ]]; then
        MAN_URL="${REMOTE_SCRIPT_URL%/change_ip.sh}/man/change_ip.8"
    fi
    command -v curl >/dev/null 2>&1 || {
        printf 'Required command not found: curl\n' >&2
        exit 1
    }
    TMP_DIR="$(mktemp -d)"
    SCRIPT_PATH="$TMP_DIR/change_ip.sh"
    curl -fsSL "$REMOTE_SCRIPT_URL" -o "$SCRIPT_PATH"
fi

bash -n "$SCRIPT_PATH"
install -d "$PREFIX/sbin" "$PREFIX/share/man/man8"
install -m 0755 "$SCRIPT_PATH" "$PREFIX/sbin/change_ip"

if [[ -n "$MAN_URL" ]]; then
    if [[ "$MAN_URL" == file:* ]]; then
        MAN_PATH="${MAN_URL#file:}"
        [[ -f "$MAN_PATH" ]] && install -m 0644 "$MAN_PATH" "$PREFIX/share/man/man8/change_ip.8"
    else
        if [[ -z "$TMP_DIR" ]]; then
            TMP_DIR="$(mktemp -d)"
        fi
        MAN_PATH="$TMP_DIR/change_ip.8"
        if curl -fsSL "$MAN_URL" -o "$MAN_PATH"; then
            install -m 0644 "$MAN_PATH" "$PREFIX/share/man/man8/change_ip.8"
        else
            printf 'Warning: man page was not installed: %s\n' "$MAN_URL" >&2
        fi
    fi
fi

printf 'Installed %s/sbin/change_ip\n' "$PREFIX"
printf 'Network was not changed. Run: sudo change_ip\n'
