#!/bin/sh
# Copies the program files the installers ship into DIR, with .commit for the automatic
# updates (uni_vpn/updater.py) when GITHUB_SHA is set. Usage: packaging/stage-app.sh DIR
set -eu
repo=$(cd "$(dirname "$0")/.." && pwd)
mkdir -p "$1"
for item in uni_vpn bin launchd systemd install.sh install.ps1 install.cmd LICENSE README.md; do
  cp -R "$repo/$item" "$1/"
done
find "$1" -name __pycache__ -type d -prune -exec rm -rf {} +
if [ -n "${GITHUB_SHA:-}" ]; then echo "$GITHUB_SHA" > "$1/.commit"; fi
