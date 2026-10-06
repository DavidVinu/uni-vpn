#!/usr/bin/env bash
# Builds the Go core for every platform as the zips and core-manifest.json the updaters read
# (docs/go-switch.md). Usage: packaging/build-core.sh COMMIT OUT-DIR
set -euo pipefail
commit="$1"
repo=$(cd "$(dirname "$0")/.." && pwd)
out=$(mkdir -p "$2" && cd "$2" && pwd)
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
cd "$repo"
for target in linux/amd64 linux/arm64 darwin/arm64 darwin/amd64 windows/amd64; do
  goos=${target%/*} goarch=${target#*/}
  exe=uni-vpn-core
  [ "$goos" = windows ] && exe=uni-vpn-core.exe
  rm -rf "$stage/bin" && mkdir -p "$stage/bin"
  CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch go build -trimpath \
    -ldflags "-s -w -X github.com/DavidVinu/uni-vpn/internal/daemon.Commit=$commit" -o "$stage/bin/$exe" ./cmd/uni-vpn
  rm -f "$out/uni-vpn-core-$goos-$goarch.zip"
  (cd "$stage" && zip -q -X "$out/uni-vpn-core-$goos-$goarch.zip" "bin/$exe")
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
