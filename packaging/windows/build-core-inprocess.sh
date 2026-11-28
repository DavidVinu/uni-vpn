#!/usr/bin/env bash
# The Go core for Windows with libopenconnect linked in (build tag libopenconnect): its tunnel
# runs inside the core (internal/libtunnel, as on iOS) and needs no Wintun adapter and no
# administrator. Runs after build-openconnect.sh in the same Fedora container, which installed
# the patched MinGW libopenconnect; the core needs the DLLs build-openconnect.sh collected.
# Into OUT-DIR: uni-vpn-core.exe (a console program, for the tests), uni-vpn-core-gui.exe (the
# same without a console window, for the MSIX package) and the Go licences in licenses.
# Usage: packaging/windows/build-core-inprocess.sh OUT-DIR
set -euo pipefail
mkdir -p "$1"
out=$(cd "$1" && pwd)
cd "$(dirname "$0")/../.."
ldflags="-s -w -X github.com/DavidVinu/uni-vpn/internal/daemon.Commit=${GITHUB_SHA:-dev}${UNI_VPN_VERSION:+ -X github.com/DavidVinu/uni-vpn/internal/daemon.Version=$UNI_VPN_VERSION}"
build() {
  CGO_ENABLED=1 GOOS=windows GOARCH=amd64 CC=x86_64-w64-mingw32-gcc PKG_CONFIG=x86_64-w64-mingw32-pkg-config \
    go build -trimpath -buildvcs=false -tags libopenconnect -ldflags "$ldflags $1" -o "$2" ./cmd/uni-vpn
}
build "" "$out/uni-vpn-core.exe"
build "-H windowsgui" "$out/uni-vpn-core-gui.exe"
packaging/go-licenses.sh windows amd64 "$out/licenses"
# go-licenses.sh lists the build without cgo, which has no network stack.
cp "$(go list -m -f '{{.Dir}}' gvisor.dev/gvisor)/LICENSE" "$out/licenses/gvisor.dev_gvisor-LICENSE"
# Every DLL it loads is either Windows' own or one build-openconnect.sh put next to it.
x86_64-w64-mingw32-objdump -p "$out/uni-vpn-core.exe" | awk '/DLL Name:/ { print $3 }'
