#!/usr/bin/env bash
# One-line install for Linux and macOS:
#   curl -fsSL https://raw.githubusercontent.com/DavidVinu/uni-vpn/main/get.sh | bash
# Downloads uni-vpn (no git needed) and runs install.sh. Arguments after "bash -s --" are passed on.
set -euo pipefail

REF="${UNI_VPN_REF:-main}"
case "$(uname -s)" in
  Darwin) APP="$HOME/Library/Application Support/uni-vpn/app" ;;
  Linux) APP="${XDG_DATA_HOME:-$HOME/.local/share}/uni-vpn/app" ;;
  *) echo "Not supported: $(uname -s). On Windows use get.ps1."; exit 1 ;;
esac

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
echo "-> downloading uni-vpn ($REF)"
curl -fsSL "https://codeload.github.com/DavidVinu/uni-vpn/tar.gz/$REF" | tar -xz -C "$tmp"
src=$(find "$tmp" -mindepth 1 -maxdepth 1 -type d | head -n 1)
[ -f "$src/uni_vpn/__init__.py" ] || { echo "Download is incomplete"; exit 1; }
mkdir -p "$APP"
cp -R "$src/." "$APP/"
echo "-> installed to $APP"
# exec below replaces this shell, so the EXIT trap would never run.
rm -rf "$tmp"
trap - EXIT

# "curl | bash" uses stdin for the script; questions must come from the terminal.
if { true </dev/tty; } 2>/dev/null; then
  exec "$APP/install.sh" "$@" </dev/tty
fi
exec "$APP/install.sh" "$@"
