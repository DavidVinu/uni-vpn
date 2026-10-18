#!/usr/bin/env bash
# Builds the Go core for every platform as the zips and core-manifest.json the updaters read
# (docs/go-switch.md), with the app window, so that updates bring it along (internal/desktop).
# Usage: packaging/build-core.sh COMMIT OUT-DIR [APP-DIR]
# APP-DIR holds the app windows built on their own systems, as the update installs them:
# darwin/desktop/Uni VPN.app and windows/desktop/, the installers' openconnect
# (darwin/runtime/<arch> with ocproxy, windows/openconnect), and the signed cores windows/bin
# and windows-arm64/bin when there are signing secrets (CI: macos-pkg, windows-installer).
# Without it the macOS and Windows zips have the core alone. Linux's app window is a script from
# the repository, and its openconnect comes from the distribution.
set -euo pipefail
commit="$1"
repo=$(cd "$(dirname "$0")/.." && pwd)
out=$(mkdir -p "$2" && cd "$2" && pwd)
apps=""
if [ -n "${3:-}" ]; then
  apps=$(cd "$3" && pwd)
  for f in "darwin/desktop/Uni VPN.app/Contents/MacOS/Uni VPN" "windows/desktop/Uni VPN.exe" \
    darwin/runtime/arm64/bin/openconnect darwin/runtime/arm64/bin/ocproxy \
    darwin/runtime/x86_64/bin/openconnect darwin/runtime/x86_64/bin/ocproxy \
    windows/openconnect/openconnect.exe; do
    # A release without them would remove what the last one installed.
    [ -f "$apps/$f" ] || { echo "missing $apps/$f" >&2; exit 1; }
  done
fi
stage=$(mktemp -d)
mac=$(mktemp -d)
trap 'rm -rf "$stage" "$mac"' EXIT
cd "$repo"
# build GOOS GOARCH FILE
build() {
  CGO_ENABLED=0 GOOS=$1 GOARCH=$2 go build -trimpath \
    -ldflags "-s -w -X github.com/DavidVinu/uni-vpn/internal/daemon.Commit=$commit${UNI_VPN_VERSION:+ -X github.com/DavidVinu/uni-vpn/internal/daemon.Version=$UNI_VPN_VERSION}" -o "$3" ./cmd/uni-vpn
}
# Both Mac zips have the core for both processors, as the installer does (stage-app.sh): a Mac
# moved from Intel to Apple Silicon with Migration Assistant keeps the core it updated to, and
# launchd does not start an Intel program without Rosetta.
build darwin arm64 "$mac/arm64"
build darwin amd64 "$mac/amd64"
go run ./packaging/lipo "$mac/uni-vpn-core" "$mac/arm64" "$mac/amd64"
for target in linux/amd64 linux/arm64 darwin/arm64 darwin/amd64 windows/amd64 windows/arm64; do
  goos=${target%/*} goarch=${target#*/}
  exe=uni-vpn-core
  [ "$goos" = windows ] && exe=uni-vpn-core.exe
  rm -rf "${stage:?}"/* && mkdir -p "$stage/bin"
  if [ "$goos" = darwin ]; then
    cp "$mac/uni-vpn-core" "$stage/bin/"
  else
    build "$goos" "$goarch" "$stage/bin/$exe"
  fi
  case "$target" in
    linux/*) mkdir -p "$stage/app/linux" && cp app/linux/uni-vpn-app.py "$stage/app/linux/" ;;
    # The same x64 app window and openconnect as windows/amd64; the signed ARM core is in
    # windows-arm64/bin.
    windows/arm64)
      if [ -n "$apps" ]; then
        cp -R "$apps/windows/desktop" "$apps/windows/openconnect" "$stage/"
        if [ -d "$apps/windows-arm64" ]; then cp -R "$apps/windows-arm64/." "$stage/"; fi
      fi
      ;;
    # After the build: the core windows-installer signed (windows/bin, with the signing secrets)
    # replaces this one.
    *) if [ -n "$apps" ]; then cp -R "$apps/$goos/." "$stage/"; fi ;;
  esac
  rm -f "$out/uni-vpn-core-$goos-$goarch.zip"
  (cd "$stage" && zip -q -X -D -r "$out/uni-vpn-core-$goos-$goarch.zip" .)
done
handover=false
[ -e packaging/handover ] && handover=true
python3 - "$out" "$commit" "$handover" <<'PY'
import hashlib, json, pathlib, sys
out, commit, handover = pathlib.Path(sys.argv[1]), sys.argv[2], sys.argv[3] == "true"
files = {p.name: {"sha256": hashlib.sha256(p.read_bytes()).hexdigest(), "size": p.stat().st_size}
         for p in sorted(out.glob("uni-vpn-core-*.zip"))}
(out / "core-manifest.json").write_text(json.dumps({"commit": commit, "handover": handover, "files": files}, indent=1) + "\n")
PY
ls -l "$out"
