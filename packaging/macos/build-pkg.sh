#!/usr/bin/env bash
# Builds uni-vpn.pkg from the per-processor folders of build-runtime.sh and the Go core.
# Needs Go and Python 3 (only here, for the Info.plist; the Macs it installs on need neither).
# Usage: packaging/macos/build-pkg.sh RUNTIME-DIR VERSION OUT.pkg
# Signs and notarizes when MACOS_APP_IDENTITY, MACOS_INSTALLER_IDENTITY and
# MACOS_NOTARY_PROFILE are set (see .github/workflows/ci.yml); unsigned otherwise.
set -euo pipefail
runtime=$(cd "$1" && pwd)
version="$2"
output="$3"
repo=$(cd "$(dirname "$0")/../.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

app="$work/root/Applications/Uni VPN.app"
resources="$app/Contents/Resources"
mkdir -p "$app/Contents/MacOS" "$resources/app"
for arch in arm64 x86_64; do
  [ -d "$runtime/$arch" ] || { echo "missing $runtime/$arch"; exit 1; }
  cp -R "$runtime/$arch" "$resources/"
done
"$repo/packaging/stage-app.sh" --go darwin/universal "$resources/app"
cp "$repo/packaging/uni-vpn-open" "$resources/"
chmod 755 "$resources/uni-vpn-open"

# The app window and menu bar item (app/macos), for both processors in one file.
for target in arm64-apple-macos11 x86_64-apple-macos11; do
  xcrun swiftc -O -target "$target" -o "$work/app-$target" "$repo/app/macos/main.swift"
done
lipo -create -output "$app/Contents/MacOS/Uni VPN" "$work"/app-*
# Info.plist and icon exactly as uni_vpn/desktop.py makes them for a self-built app.
(cd "$repo" && python3 - "$app" "$version" <<'PY'
import plistlib, sys
from uni_vpn import desktop
info = desktop.info_plist(1081)
info["CFBundleShortVersionString"] = info["CFBundleVersion"] = sys.argv[2]
with open(sys.argv[1] + "/Contents/Info.plist", "wb") as handle:
    plistlib.dump(info, handle)
PY
)
iconset="$work/AppIcon.iconset"
mkdir -p "$iconset"
for size in 16 32 128 256 512; do
  sips -z $size $size "$repo/app/icon.png" --out "$iconset/icon_${size}x${size}.png" >/dev/null
  sips -z $((size * 2)) $((size * 2)) "$repo/app/icon.png" --out "$iconset/icon_${size}x${size}@2x.png" >/dev/null
done
iconutil -c icns "$iconset" -o "$resources/AppIcon.icns"
plutil -lint "$app/Contents/Info.plist"

if [ -n "${MACOS_APP_IDENTITY:-}" ]; then
  # Inside out: libraries and programs first, the bundle last.
  find "$resources" -type f \( -perm -u+x -o -name '*.dylib' -o -name '*.so' \) -print0 |
    while IFS= read -r -d '' f; do
      file -b "$f" | grep -q Mach-O || continue
      codesign --force --timestamp --options runtime -s "$MACOS_APP_IDENTITY" "$f"
    done
  codesign --force --timestamp --options runtime -s "$MACOS_APP_IDENTITY" "$app"
fi

# Installed where it was built for, also when someone moved an older copy elsewhere.
pkgbuild --analyze --root "$work/root" "$work/components.plist"
plutil -replace 0.BundleIsRelocatable -bool NO "$work/components.plist"
pkgbuild --root "$work/root" --component-plist "$work/components.plist" \
  --scripts "$repo/packaging/macos/scripts" --identifier de.davidvinu.uni-vpn \
  --version "$version" --install-location / "$work/uni-vpn-component.pkg"
sed "s/@VERSION@/$version/g" "$repo/packaging/macos/distribution.xml" > "$work/distribution.xml"
sign=()
[ -n "${MACOS_INSTALLER_IDENTITY:-}" ] && sign=(--sign "$MACOS_INSTALLER_IDENTITY" --timestamp)
productbuild --distribution "$work/distribution.xml" --package-path "$work" ${sign[@]+"${sign[@]}"} "$output"

if [ -n "${MACOS_NOTARY_PROFILE:-}" ]; then
  xcrun notarytool submit "$output" --keychain-profile "$MACOS_NOTARY_PROFILE" --wait
  xcrun stapler staple "$output"
fi
ls -lh "$output"
