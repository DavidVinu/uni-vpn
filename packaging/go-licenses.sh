#!/bin/sh
# Copies the licences of Go (its runtime and standard library are in the core) and of every Go
# module in the core built for GOOS/GOARCH into DIR, as the phone builds do.
# Usage: packaging/go-licenses.sh GOOS GOARCH DIR
set -eu
repo=$(cd "$(dirname "$0")/.." && pwd)
mkdir -p "$3"
dir=$(cd "$3" && pwd)
cp "$(cd "$repo" && go env GOROOT)/LICENSE" "$dir/go-LICENSE"
chmod 644 "$dir/go-LICENSE"
(cd "$repo" && CGO_ENABLED=0 GOOS="$1" GOARCH="$2" go list -deps \
  -f '{{with .Module}}{{if not .Main}}{{.Path}}|{{.Dir}}{{end}}{{end}}' ./cmd/uni-vpn) | sort -u |
  while IFS='|' read -r path mod; do
    [ -n "$path" ] || continue
    found=""
    for file in "$mod"/LICENSE* "$mod"/COPYING* "$mod"/LICENCE*; do
      [ -f "$file" ] || continue
      target="$dir/$(echo "$path" | tr / _)-$(basename "$file")"
      cp "$file" "$target"
      chmod 644 "$target"
      found=1
    done
    [ -n "$found" ] || echo "go-licenses: warning: no licence file in Go module $path ($mod)" >&2
  done
