#!/usr/bin/env bash
# Builds uni-vpn.deb and uni-vpn.rpm. Needs nfpm (https://nfpm.goreleaser.com).
# Usage: packaging/linux/build.sh VERSION OUT-DIR
set -euo pipefail
version="$1"
out=$(mkdir -p "$2" && cd "$2" && pwd)
here=$(cd "$(dirname "$0")" && pwd)
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
"$here/../stage-app.sh" "$stage/app"
cp "$here/../uni-vpn-open" "$here/uni-vpn.desktop" "$here/de.davidvinu.uni_vpn.metainfo.xml" "$stage/"
cd "$stage"
export VERSION="$version"
nfpm package --config "$here/nfpm.yaml" --packager deb --target "$out/uni-vpn.deb"
nfpm package --config "$here/nfpm.yaml" --packager rpm --target "$out/uni-vpn.rpm"
ls -l "$out"
