#!/usr/bin/env bash
# Builds the Android app: the Go core and openconnect/ocproxy for each ABI into jniLibs, then the
# APK with Gradle. Needs ANDROID_HOME (SDK with platform 36), ANDROID_NDK_HOME and Go.
# Usage: packaging/android/build-apk.sh COMMIT [GRADLE-TASK...]   (default: assembleRelease)
# NATIVE_DIR=<dir> reuses openconnect/ocproxy from an earlier build-native.sh run
# (<dir>/<abi>/libopenconnect.so, libocproxy.so) instead of building them.
set -euo pipefail
commit="$1"
shift
tasks=("$@")
[ ${#tasks[@]} -gt 0 ] || tasks=(assembleRelease)
root=$(cd "$(dirname "$0")/../.." && pwd)
app="$root/app/android"
[ -n "${ANDROID_NDK_HOME:-}" ] || { echo "ANDROID_NDK_HOME is not set" >&2; exit 1; }
toolchain=$(echo "$ANDROID_NDK_HOME"/toolchains/llvm/prebuilt/*/bin)
licenses="$app/app/src/main/assets/licenses"
rm -rf "$licenses"
mkdir -p "$licenses"
cp "$root/LICENSE" "$licenses/uni-vpn-LICENSE"

for abi in arm64-v8a x86_64; do
  case "$abi" in
    arm64-v8a) goarch=arm64 cc=aarch64-linux-android29-clang ;;
    x86_64) goarch=amd64 cc=x86_64-linux-android29-clang ;;
  esac
  libs="$app/app/src/main/jniLibs/$abi"
  mkdir -p "$libs"
  native="${NATIVE_DIR:-$app/build/native}/$abi"
  if [ ! -f "$native/libopenconnect.so" ]; then
    "$root/packaging/android/build-native.sh" "$abi" "$native"
  fi
  cp "$native/libopenconnect.so" "$native/libocproxy.so" "$libs/"
  # Settings, Open source licenses shows these.
  mkdir -p "$licenses"
  cp "$native"/licenses/* "$licenses/"
  # The core is a program too (lib*.so for the same reason). cgo, so names resolve through
  # Android's own resolver.
  (cd "$root" && CGO_ENABLED=1 GOOS=android GOARCH="$goarch" CC="$toolchain/$cc" \
    go build -trimpath -ldflags "-s -w -X github.com/DavidVinu/uni-vpn/internal/daemon.Commit=$commit${UNI_VPN_VERSION:+ -X github.com/DavidVinu/uni-vpn/internal/daemon.Version=$UNI_VPN_VERSION}" \
    -o "$libs/libunivpn.so" ./cmd/uni-vpn)
done
# And every Go module in the core, as on the iPhone (packaging/ios/build-core.sh).
(cd "$root" && CGO_ENABLED=1 GOOS=android GOARCH=arm64 go list -deps \
  -f '{{with .Module}}{{if not .Main}}{{.Path}}|{{.Dir}}{{end}}{{end}}' ./cmd/uni-vpn) | sort -u |
  while IFS='|' read -r path dir; do
    [ -n "$path" ] || continue
    found=""
    for file in "$dir"/LICENSE* "$dir"/COPYING* "$dir"/LICENCE*; do
      [ -f "$file" ] || continue
      cp "$file" "$licenses/$(echo "$path" | tr / _)-$(basename "$file")"
      found=1
    done
    [ -n "$found" ] || echo "build-apk: warning: no licence file in Go module $path ($dir)" >&2
  done
cd "$app"
./gradlew --no-daemon -q "${tasks[@]}"
