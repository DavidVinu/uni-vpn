#!/usr/bin/env bash
# openconnect.exe for Windows with every DLL it loads and Wintun, into OUT-DIR.
# OpenConnect-GUI's installer has no openconnect.exe, so uni-vpn brings its own.
# Runs in MSYS2's UCRT64 shell with mingw-w64-ucrt-x86_64-openconnect installed (see ci.yml).
# Usage: packaging/windows/build-openconnect.sh OUT-DIR
set -euo pipefail
out="$1"
mkdir -p "$out"
cp /ucrt64/bin/openconnect.exe "$out/"
# Its DLLs from MSYS2; Windows' own ones stay out.
ldd "$out/openconnect.exe" | awk '$3 ~ /^\/ucrt64\// { print $3 }' | sort -u | while read -r dll; do cp "$dll" "$out/"; done
# Wintun, signed by WireGuard LLC, checked against the SHA-256 of the release.
curl -fsSL -o /tmp/wintun.zip https://www.wintun.net/builds/wintun-0.14.1.zip
echo "07c256185d6ee3652e09fa55c0b673e2624b565e02c4b9091c79ca7d2f24ef51  /tmp/wintun.zip" | sha256sum -c -
unzip -o -j -q /tmp/wintun.zip wintun/bin/amd64/wintun.dll -d "$out"
cp /ucrt64/share/licenses/openconnect/* "$out/" 2>/dev/null || true
ls -l "$out"
