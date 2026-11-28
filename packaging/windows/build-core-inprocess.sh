#!/usr/bin/env bash
# The Go core for Windows with libopenconnect linked in (build tag libopenconnect): its tunnel
# runs inside the core (internal/libtunnel, as on iOS) and needs no Wintun adapter and no
# administrator. Runs after build-openconnect.sh in the same Fedora container, which installed
# the patched MinGW libopenconnect; the core needs the DLLs build-openconnect.sh collected.
# Usage: packaging/windows/build-core-inprocess.sh OUT-FILE
set -euo pipefail
out=$(realpath -m "$1")
cd "$(dirname "$0")/../.."
CGO_ENABLED=1 GOOS=windows GOARCH=amd64 CC=x86_64-w64-mingw32-gcc PKG_CONFIG=x86_64-w64-mingw32-pkg-config \
  go build -trimpath -buildvcs=false -tags libopenconnect \
  -ldflags "-s -w -X github.com/DavidVinu/uni-vpn/internal/daemon.Commit=${GITHUB_SHA:-dev}${UNI_VPN_VERSION:+ -X github.com/DavidVinu/uni-vpn/internal/daemon.Version=$UNI_VPN_VERSION}" \
  -o "$out" ./cmd/uni-vpn
# Every DLL it loads is either Windows' own or one build-openconnect.sh put next to it.
x86_64-w64-mingw32-objdump -p "$out" | awk '/DLL Name:/ { print $3 }'
