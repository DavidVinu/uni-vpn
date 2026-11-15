#!/bin/sh
# Copies the program files the installers ship into DIR, with .commit for the automatic
# updates when GITHUB_SHA is set.
# Usage: packaging/stage-app.sh [--go TARGET | --support] DIR
# --go: the Go core without Python (docs/go-switch.md): bin/uni-vpn-core built for TARGET
# (linux/amd64, linux/arm64, windows/amd64, darwin/universal for both Mac processors in one
# file, or windows/both: amd64 in bin and arm64 in bin/arm64, the installer picks one), the
# files of --support, the licences of Go and the core's Go modules in licenses. Needs Go.
# --support: only what goes next to the core, in the installers and in every update
# (packaging/build-core.sh): its helpers in bin, the app windows' sources and icons in app (not
# the phone apps), LICENSE and README.md.
# Without either: the Python program (uni_vpn/updater.py).
set -eu
repo=$(cd "$(dirname "$0")/.." && pwd)
target=""
case "${1:-}" in
  --go)
    target="$2"
    shift 2
    ;;
  --support)
    target=support
    shift
    ;;
esac
mkdir -p "$1"
out=$(cd "$1" && pwd)

support() {
  mkdir -p "$out/bin" "$out/app"
  cp "$repo/bin/uni-vpn-vpnc.js" "$repo/bin/uni-vpn-ocproxy" "$out/bin/"
  for item in "$repo"/app/*; do
    case "${item##*/}" in
      android | ios) ;;
      *) cp -R "$item" "$out/app/" ;;
    esac
  done
  cp "$repo/LICENSE" "$repo/README.md" "$out/"
}
if [ "$target" = support ]; then
  support
  exit 0
fi

# build GOOS GOARCH FILE: the core as the release zips have it, with the commit it updates from.
build() {
  (cd "$repo" && CGO_ENABLED=0 GOOS="$1" GOARCH="$2" go build -trimpath \
    -ldflags "-X github.com/DavidVinu/uni-vpn/internal/daemon.Commit=${GITHUB_SHA:-}${UNI_VPN_VERSION:+ -X github.com/DavidVinu/uni-vpn/internal/daemon.Version=$UNI_VPN_VERSION}" -o "$3" ./cmd/uni-vpn)
}

if [ -z "$target" ]; then
  for item in uni_vpn bin app launchd systemd install.sh install.ps1 install.cmd LICENSE README.md; do
    cp -R "$repo/$item" "$out/"
  done
  find "$out" -name __pycache__ -type d -prune -exec rm -rf {} +
else
  support
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
    windows/both)
      build windows amd64 "$out/bin/uni-vpn-core.exe"
      build windows arm64 "$out/bin/arm64/uni-vpn-core.exe"
      ;;
    windows/*) build windows "${target#*/}" "$out/bin/uni-vpn-core.exe" ;;
    */*) build "${target%/*}" "${target#*/}" "$out/bin/uni-vpn-core" ;;
    *) echo "unknown target $target" >&2; exit 2 ;;
  esac
  # The same modules for both processors.
  case "$target" in
    darwin/universal) "$repo/packaging/go-licenses.sh" darwin arm64 "$out/licenses" ;;
    windows/both) "$repo/packaging/go-licenses.sh" windows amd64 "$out/licenses" ;;
    *) "$repo/packaging/go-licenses.sh" "${target%/*}" "${target#*/}" "$out/licenses" ;;
  esac
fi
if [ -n "${GITHUB_SHA:-}" ]; then echo "$GITHUB_SHA" > "$out/.commit"; fi
