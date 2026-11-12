#!/usr/bin/env bash
# Compiles app/AppIcon.icon (Icon Composer: the light, dark, tinted and clear looks of macOS 26)
# into OUT-DIR/Assets.car with Xcode 26's actool; macOS before 26 show AppIcon.icns instead
# (build-pkg.sh). actool needs macOS 26 itself: on macOS 15 its asset agent crashes.
# With PREVIEW-DIR also PNGs of each look, to look at without a Mac.
# Usage: packaging/macos/build-icon.sh OUT-DIR [PREVIEW-DIR]
set -euo pipefail
repo=$(cd "$(dirname "$0")/../.." && pwd)
out=$(mkdir -p "$1" && cd "$1" && pwd)
# The newest Xcode 26 or later: CI runners keep several, the selected one is not always it.
developer=""
for xcode in $(ls -d /Applications/Xcode*.app 2>/dev/null | sort -V -r); do
  version=$(defaults read "$xcode/Contents/Info" CFBundleShortVersionString 2>/dev/null || echo 0)
  if [ "${version%%.*}" -ge 26 ]; then developer="$xcode/Contents/Developer"; break; fi
done
[ -n "$developer" ] || { echo "build-icon.sh needs Xcode 26 or later" >&2; exit 1; }
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
DEVELOPER_DIR="$developer" xcrun actool "$repo/app/AppIcon.icon" --compile "$work" --platform macosx \
  --minimum-deployment-target 11.0 --app-icon AppIcon --output-partial-info-plist "$work/partial.plist" \
  --errors --warnings >/dev/null
# Each look must be there, or macOS 26 quietly shows the light one in dark mode.
info=$(DEVELOPER_DIR="$developer" xcrun assetutil --info "$work/Assets.car")
for look in NSAppearanceNameDarkAqua ISAppearanceTintable; do
  grep -q "\"$look\"" <<<"$info" || { echo "Assets.car has no $look look" >&2; exit 1; }
done
cp "$work/Assets.car" "$out/"
if [ -n "${2:-}" ]; then
  mkdir -p "$2"
  ictool="$developer/../Applications/Icon Composer.app/Contents/Executables/ictool"
  for look in Default Dark TintedLight TintedDark ClearLight ClearDark; do
    "$ictool" "$repo/app/AppIcon.icon" --export-image --output-file "$2/$look.png" --platform macOS \
      --rendition "$look" --width 512 --height 512 --scale 1
  done
fi
ls -l "$out/Assets.car"
