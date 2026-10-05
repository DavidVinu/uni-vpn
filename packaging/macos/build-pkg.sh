#!/usr/bin/env bash
# Builds uni-vpn.pkg from the per-processor folders of build-runtime.sh.
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
"$repo/packaging/stage-app.sh" "$resources/app"
cp "$repo/packaging/uni-vpn-open" "$resources/"
cat > "$app/Contents/MacOS/Uni VPN" <<'SCRIPT'
#!/bin/sh
exec "$(dirname "$0")/../Resources/uni-vpn-open" "$@"
SCRIPT
chmod 755 "$app/Contents/MacOS/Uni VPN" "$resources/uni-vpn-open"
sed "s/@VERSION@/$version/g" "$repo/packaging/macos/Info.plist" > "$app/Contents/Info.plist"
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
productbuild --distribution "$work/distribution.xml" --package-path "$work" "${sign[@]}" "$output"

if [ -n "${MACOS_NOTARY_PROFILE:-}" ]; then
  xcrun notarytool submit "$output" --keychain-profile "$MACOS_NOTARY_PROFILE" --wait
  xcrun stapler staple "$output"
fi
ls -lh "$output"
