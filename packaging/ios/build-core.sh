#!/usr/bin/env bash
# Builds the Go core with libopenconnect for the iPhone app: one static library per slice
# (core, openconnect, OpenSSL), the simulator slices joined, all in
# app/ios/build/UniVPNCore.xcframework. Also the licence texts (app/ios/build/licenses) and the
# camera prompt in every language (app/ios/build/Resources/<lang>.lproj/InfoPlist.strings).
# Usage: packaging/ios/build-core.sh COMMIT
# NATIVE_DIR=<dir> reuses openconnect and OpenSSL from an earlier run of the same build-native.sh
# (<dir>/<slice>/lib/...) instead of building them. Needs macOS with Xcode, Go and python3.
set -euo pipefail
[ $# -eq 1 ] || { echo "usage: $0 COMMIT" >&2; exit 2; }
commit="$1"
die() { echo "build-core: $*" >&2; exit 1; }
root=$(cd "$(dirname "$0")/../.." && pwd)
app="$root/app/ios"
build="$app/build"
native_dir="${NATIVE_DIR:-$build/native}"
work="$build/core"
min=17.0
rm -rf "$work" "$build/UniVPNCore.xcframework" "$build/licenses" "$build/Resources"
mkdir -p "$build/licenses"

ldflags="-s -w -X github.com/DavidVinu/uni-vpn/internal/daemon.Commit=$commit"
[ -z "${UNI_VPN_VERSION:-}" ] || ldflags="$ldflags -X github.com/DavidVinu/uni-vpn/internal/daemon.Version=$UNI_VPN_VERSION"

for slice in iphoneos-arm64 iphonesimulator-arm64 iphonesimulator-x86_64; do
  case "$slice" in
    iphoneos-arm64) sdk=iphoneos goarch=arm64 target="arm64-apple-ios$min" ;;
    iphonesimulator-arm64) sdk=iphonesimulator goarch=arm64 target="arm64-apple-ios$min-simulator" ;;
    iphonesimulator-x86_64) sdk=iphonesimulator goarch=amd64 target="x86_64-apple-ios$min-simulator" ;;
  esac
  native="$native_dir/$slice"
  # Built again when build-native.sh changed since (it stamps its hash, CI's cache key).
  stamp=$(shasum -a 256 "$root/packaging/ios/build-native.sh" | cut -d' ' -f1)
  if [ ! -f "$native/lib/libopenconnect.a" ] || [ "$(cat "$native/.stamp" 2>/dev/null)" != "$stamp" ]; then
    "$root/packaging/ios/build-native.sh" "$slice" "$native"
  fi
  echo "==> [$slice] core"
  sysroot=$(xcrun --sdk "$sdk" --show-sdk-path)
  flags="-target $target -isysroot $sysroot"
  out="$work/$slice"
  mkdir -p "$out"
  # The simulator is told apart from the device by the target of the C objects, as gomobile
  # does: Go copies the platform from them.
  (cd "$root" && CGO_ENABLED=1 GOOS=ios GOARCH="$goarch" CC="$(xcrun --sdk "$sdk" --find clang)" \
    CGO_CFLAGS="$flags -O2 -I$native/include" \
    CGO_LDFLAGS="$flags -L$native/lib -lopenconnect -lssl -lcrypto -lxml2 -lz -framework Security -framework CoreFoundation" \
    PKG_CONFIG_LIBDIR="$native/lib/pkgconfig" PKG_CONFIG_PATH="" \
    go build -tags libopenconnect -buildmode=c-archive -trimpath -ldflags "$ldflags" \
    -o "$out/libcore.a" ./cmd/uni-vpn-ios)
  # The app declares the functions itself (app/ios/Core/UniVPNCore.h); fail here, not at link
  # time, when the Go side changed them.
  for fn in 'extern int UniVPNStart\(char\* configHome, char\* stateHome, char\* apiToken, char\* secretsKeyHex, char\* deviceName, char\* lockWaitMs\);' \
    'extern void UniVPNStop\((void)?\);' 'extern int UniVPNRunning\((void)?\);'; do
    grep -qE "$fn" "$out/libcore.h" || die "libcore.h does not match: $fn"
  done
  # One library per slice, so the app links a single file.
  libtool -static -o "$out/libunivpn.a" "$out/libcore.a" \
    "$native/lib/libopenconnect.a" "$native/lib/libssl.a" "$native/lib/libcrypto.a" 2>"$out/libtool.log" \
    || { cat "$out/libtool.log" >&2; die "libtool failed for $slice"; }
done

mkdir -p "$work/iphonesimulator"
lipo -create "$work/iphonesimulator-arm64/libunivpn.a" "$work/iphonesimulator-x86_64/libunivpn.a" \
  -output "$work/iphonesimulator/libunivpn.a"
# Code only: the module UniVPNCore is defined once, by app/ios/Core/module.modulemap
# (SWIFT_INCLUDE_PATHS in project.yml). A second module map in the xcframework would land in the
# build's include folder too and clash with it.
xcodebuild -create-xcframework \
  -library "$work/iphoneos-arm64/libunivpn.a" \
  -library "$work/iphonesimulator/libunivpn.a" \
  -output "$build/UniVPNCore.xcframework"

# Settings, Open source licenses shows these: Uni VPN, openconnect, OpenSSL, Go (its runtime and
# standard library are in the core) and every Go module in the core. libxml2 and zlib are part
# of iOS.
cp "$root/LICENSE" "$build/licenses/uni-vpn-LICENSE"
cp "$(cd "$root" && go env GOROOT)/LICENSE" "$build/licenses/go-LICENSE"
cp "$native_dir/iphoneos-arm64/licenses/"* "$build/licenses/"
(cd "$root" && CGO_ENABLED=1 GOOS=ios GOARCH=arm64 go list -tags libopenconnect -deps \
  -f '{{with .Module}}{{if not .Main}}{{.Path}}|{{.Dir}}{{end}}{{end}}' ./cmd/uni-vpn-ios) | sort -u |
  while IFS='|' read -r path dir; do
    [ -n "$path" ] || continue
    found=""
    for file in "$dir"/LICENSE* "$dir"/COPYING* "$dir"/LICENCE*; do
      [ -f "$file" ] || continue
      cp "$file" "$build/licenses/$(echo "$path" | tr / _)-$(basename "$file")"
      found=1
    done
    [ -n "$found" ] || echo "build-core: warning: no licence file in Go module $path ($dir)" >&2
  done

# The camera prompt is the system's: it reads InfoPlist.strings, so the text comes from the
# same catalogs as the rest (phone.camera_use).
python3 - "$root/uni_vpn/locales" "$build/Resources" <<'EOF'
import json, pathlib, sys
src, dst = pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2])
for catalog in sorted(src.glob("*.json")):
    text = json.loads(catalog.read_text(encoding="utf-8"))["phone.camera_use"]
    folder = dst / f"{catalog.stem}.lproj"
    folder.mkdir(parents=True, exist_ok=True)
    quoted = text.replace("\\", "\\\\").replace('"', '\\"')
    (folder / "InfoPlist.strings").write_text(f'"NSCameraUsageDescription" = "{quoted}";\n', encoding="utf-8")
EOF
ls -l "$build/UniVPNCore.xcframework" "$build/licenses"
