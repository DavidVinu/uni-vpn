#!/usr/bin/env bash
# Checks the update zips of build-core.sh against the installers: each zip has the core for its
# system and every file the installers put next to the core (stage-app.sh --support), so that an
# update brings each of them along. The manifest names each zip with its size and SHA-256.
# Usage: packaging/check-core-zips.sh OUT-DIR
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
out=$(cd "$1" && pwd)
support=$(mktemp -d)
trap 'rm -rf "$support"' EXIT
"$here/stage-app.sh" --support "$support" >/dev/null
status=0
for target in linux-amd64 linux-arm64 darwin-arm64 darwin-amd64 windows-amd64 windows-arm64; do
  zip="uni-vpn-core-$target.zip"
  exe=bin/uni-vpn-core
  [ "${target%-*}" = windows ] && exe=bin/uni-vpn-core.exe
  if [ ! -f "$out/$zip" ]; then
    echo "$zip is missing"
    status=1
    continue
  fi
  missing=$(comm -23 <( (cd "$support" && find . -type f | sed 's|^\./||'; echo "$exe") | sort -u) \
    <(unzip -Z1 "$out/$zip" | sort -u))
  if [ -n "$missing" ]; then
    echo "$zip lacks what the installers ship:"
    echo "$missing" | sed 's/^/  /'
    status=1
  fi
  python3 - "$out" "$zip" <<'PY' || status=1
import hashlib, json, pathlib, sys
out, name = pathlib.Path(sys.argv[1]), sys.argv[2]
entry = json.loads((out / "core-manifest.json").read_text())["files"].get(name)
data = (out / name).read_bytes()
if entry != {"sha256": hashlib.sha256(data).hexdigest(), "size": len(data)}:
    sys.exit(f"core-manifest.json does not match {name}")
PY
done
[ "$status" = 0 ] && echo "the update zips have everything the installers ship next to the core"
exit "$status"
