#!/usr/bin/env bash
# Builds the Go core for every platform as the zips and core-manifest.json the updaters read
# (docs/go-switch.md), with the app window, so that updates bring it along (internal/desktop).
# Usage: packaging/build-core.sh COMMIT OUT-DIR [APP-DIR]
# APP-DIR holds the app windows built on their own systems, as the update installs them:
# darwin/desktop/Uni VPN.app and windows/desktop/ (CI: macos-pkg, windows-installer). Without
# it the macOS and Windows zips have the core alone. Linux's is a script from the repository.
set -euo pipefail
commit="$1"
repo=$(cd "$(dirname "$0")/.." && pwd)
out=$(mkdir -p "$2" && cd "$2" && pwd)
apps=""
if [ -n "${3:-}" ]; then
  apps=$(cd "$3" && pwd)
  for f in "darwin/desktop/Uni VPN.app/Contents/MacOS/Uni VPN" "windows/desktop/Uni VPN.exe"; do
    # A release without them would remove the app the last one installed.
    [ -f "$apps/$f" ] || { echo "missing $apps/$f" >&2; exit 1; }
  done
fi
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
cd "$repo"
for target in linux/amd64 linux/arm64 darwin/arm64 darwin/amd64 windows/amd64; do
  goos=${target%/*} goarch=${target#*/}
  exe=uni-vpn-core
  [ "$goos" = windows ] && exe=uni-vpn-core.exe
  rm -rf "${stage:?}"/* && mkdir -p "$stage/bin"
  CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch go build -trimpath \
    -ldflags "-s -w -X github.com/DavidVinu/uni-vpn/internal/daemon.Commit=$commit${UNI_VPN_VERSION:+ -X github.com/DavidVinu/uni-vpn/internal/daemon.Version=$UNI_VPN_VERSION}" -o "$stage/bin/$exe" ./cmd/uni-vpn
  case "$goos" in
    linux) mkdir -p "$stage/app/linux" && cp app/linux/uni-vpn-app.py "$stage/app/linux/" ;;
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
