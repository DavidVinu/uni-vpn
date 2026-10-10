#!/bin/sh
# Copies the program files the installers ship into DIR, with .commit for the automatic
# updates when GITHUB_SHA is set.
# Usage: packaging/stage-app.sh [--go TARGET] DIR
# --go: the Go core without Python (docs/go-switch.md): bin/uni-vpn-core built for TARGET
# (linux/amd64, linux/arm64, windows/amd64, or darwin/universal for both Mac processors in one
# file), its helpers in bin, the app windows' sources and icons in app. Needs Go.
# Without --go: the Python program (uni_vpn/updater.py).
set -eu
repo=$(cd "$(dirname "$0")/.." && pwd)
target=""
if [ "${1:-}" = --go ]; then
  target="$2"
  shift 2
fi
mkdir -p "$1"
out=$(cd "$1" && pwd)

# build GOOS GOARCH FILE: the core as the release zips have it, with the commit it updates from.
build() {
  (cd "$repo" && CGO_ENABLED=0 GOOS="$1" GOARCH="$2" go build -trimpath \
    -ldflags "-X github.com/DavidVinu/uni-vpn/internal/daemon.Commit=${GITHUB_SHA:-}" -o "$3" ./cmd/uni-vpn)
}

if [ -z "$target" ]; then
  for item in uni_vpn bin app launchd systemd install.sh install.ps1 install.cmd LICENSE README.md; do
    cp -R "$repo/$item" "$out/"
  done
  find "$out" -name __pycache__ -type d -prune -exec rm -rf {} +
else
  mkdir -p "$out/bin"
  cp "$repo/bin/uni-vpn-vpnc.js" "$repo/bin/uni-vpn-ocproxy" "$out/bin/"
  cp -R "$repo/app" "$repo/LICENSE" "$repo/README.md" "$out/"
  case "$target" in
    darwin/universal)
      tmp=$(mktemp -d)
      build darwin arm64 "$tmp/arm64"
      build darwin amd64 "$tmp/amd64"
      lipo -create -output "$out/bin/uni-vpn-core" "$tmp/arm64" "$tmp/amd64"
      rm -rf "$tmp"
      # Ad hoc, like openconnect and ocproxy; the release signs it again when it can.
      codesign --force -s - "$out/bin/uni-vpn-core"
      ;;
    windows/*) build windows "${target#*/}" "$out/bin/uni-vpn-core.exe" ;;
    */*) build "${target%/*}" "${target#*/}" "$out/bin/uni-vpn-core" ;;
    *) echo "unknown target $target" >&2; exit 2 ;;
  esac
fi
if [ -n "${GITHUB_SHA:-}" ]; then echo "$GITHUB_SHA" > "$out/.commit"; fi
