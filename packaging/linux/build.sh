#!/usr/bin/env bash
# Builds uni-vpn.deb and uni-vpn.rpm (amd64) and uni-vpn-arm64.deb and uni-vpn-arm64.rpm.
# Needs nfpm (https://nfpm.goreleaser.com) and Go.
# Usage: packaging/linux/build.sh VERSION OUT-DIR
set -euo pipefail
version="$1"
out=$(mkdir -p "$2" && cd "$2" && pwd)
here=$(cd "$(dirname "$0")" && pwd)
export VERSION="$version"
for arch in amd64 arm64; do
  stage=$(mktemp -d)
  "$here/../stage-app.sh" --go "linux/$arch" "$stage/app"
  cp "$here/../uni-vpn-open" "$here/de.davidvinu.UniVPN.desktop" "$here/de.davidvinu.UniVPN.metainfo.xml" "$here/../../app/icon.svg" "$stage/"
  suffix=""
  [ "$arch" = amd64 ] || suffix="-$arch"
  export ARCH="$arch"
  (cd "$stage" &&
    nfpm package --config "$here/nfpm.yaml" --packager deb --target "$out/uni-vpn$suffix.deb" &&
    nfpm package --config "$here/nfpm.yaml" --packager rpm --target "$out/uni-vpn$suffix.rpm")
  rm -rf "$stage"
done
ls -l "$out"
