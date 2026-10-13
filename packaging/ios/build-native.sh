#!/usr/bin/env bash
# Cross-compiles libopenconnect and OpenSSL as static libraries for the iPhone app.
# Usage: packaging/ios/build-native.sh SLICE OUT-DIR
#   SLICE: iphoneos-arm64, iphonesimulator-arm64 or iphonesimulator-x86_64
#
# Runs on macOS with Xcode, curl, perl, make and pkg-config. libxml2 and zlib come from the
# iOS SDK (the system has them), so only OpenSSL and openconnect are built, from the same
# pinned and checksummed sources as packaging/android/build-native.sh.
#
# OUT-DIR is the install prefix: OUT-DIR/lib/{libopenconnect,libssl,libcrypto}.a,
# OUT-DIR/include, OUT-DIR/lib/pkgconfig (relocatable, for cgo) and OUT-DIR/licenses/.
#
# Build trees and the download cache live in $UNI_VPN_NATIVE_BUILD_DIR
# (default ~/.cache/uni-vpn-ios-native); the per-slice tree is rebuilt from scratch on every
# run, downloads are reused after their checksum is verified again.
set -euo pipefail

usage() { echo "usage: $0 iphoneos-arm64|iphonesimulator-arm64|iphonesimulator-x86_64 OUT-DIR" >&2; exit 2; }
die() { echo "build-native: $*" >&2; exit 1; }
[ $# -eq 2 ] || usage
slice="$1"
min=17.0
case "$slice" in
  iphoneos-arm64)
    sdk=iphoneos arch=arm64 target="arm64-apple-ios$min" host=aarch64-apple-darwin
    openssl_target=ios64-xcrun openssl_min="-mios-version-min=$min" ;;
  iphonesimulator-arm64)
    sdk=iphonesimulator arch=arm64 target="arm64-apple-ios$min-simulator" host=aarch64-apple-darwin
    openssl_target=iossimulator-arm64-xcrun openssl_min="-mios-simulator-version-min=$min" ;;
  iphonesimulator-x86_64)
    sdk=iphonesimulator arch=x86_64 target="x86_64-apple-ios$min-simulator" host=x86_64-apple-darwin
    openssl_target=iossimulator-x86_64-xcrun openssl_min="-mios-simulator-version-min=$min" ;;
  *) usage ;;
esac
[ "$(uname -s)" = Darwin ] || die "needs macOS with Xcode"
for tool in xcrun curl perl make pkg-config shasum; do
  command -v "$tool" >/dev/null || die "missing host tool: $tool"
done
sysroot=$(xcrun --sdk "$sdk" --show-sdk-path)
clang=$(xcrun --sdk "$sdk" --find clang)
[ -f "$sysroot/usr/include/libxml2/libxml/parser.h" ] || die "no libxml2 headers in $sysroot"

out=$(mkdir -p "$2" && cd "$2" && pwd)
work="${UNI_VPN_NATIVE_BUILD_DIR:-$HOME/.cache/uni-vpn-ios-native}"
dl="$work/dl"
build="$work/$slice"
logs="$build/logs"
jobs=$(sysctl -n hw.ncpu 2>/dev/null || echo 2)

# Pinned sources, the same as the Android app's.
openssl_ver=3.5.9
openconnect_ver=9.21
sources=(
  "openssl-$openssl_ver.tar.gz|https://github.com/openssl/openssl/releases/download/openssl-$openssl_ver/openssl-$openssl_ver.tar.gz|603f5602e2eef00d77fbd429d34dcd5822bb301757a1bc9cdb24c670f1eb859a"
  "openconnect-$openconnect_ver.tar.gz|https://www.infradead.org/openconnect/download/openconnect-$openconnect_ver.tar.gz|5b32369467db6e5f317aa1ed12cfcbb81ed00bdbc765450b6bfcbdc300944a58"
)

# Runs a build step with its output in a log file, printing the log tail only on failure.
step=""
run() {
  if ! "$@" >>"$logs/$step.log" 2>&1; then
    echo "build-native: [$slice] $step failed: $*" >&2
    tail -n 80 "$logs/$step.log" >&2
    echo "build-native: full log: $logs/$step.log" >&2
    exit 1
  fi
}
begin() { step="$1"; echo "==> [$slice] $1"; }

mkdir -p "$dl"
for entry in "${sources[@]}"; do
  IFS='|' read -r file url sum <<<"$entry"
  if [ ! -f "$dl/$file" ] || ! (cd "$dl" && echo "$sum  $file" | shasum -a 256 -c --status); then
    echo "==> downloading $file"
    curl -fsSL --retry 3 -o "$dl/$file.part" "$url"
    mv "$dl/$file.part" "$dl/$file"
  fi
  (cd "$dl" && echo "$sum  $file" | shasum -a 256 -c --quiet) || die "checksum mismatch for $file"
done

rm -rf "$build" "${out:?}/lib" "$out/include" "$out/licenses"
mkdir -p "$build/src" "$logs" "$out/lib/pkgconfig" "$out/include"
for entry in "${sources[@]}"; do
  tar -xf "$dl/${entry%%|*}" -C "$build/src"
done
src="$build/src"

# The SDK's libxml2 and zlib have no pkg-config files; openconnect's configure and cgo look
# for them there.
cat >"$out/lib/pkgconfig/libxml-2.0.pc" <<EOF
Name: libxml-2.0
Description: libxml2 of the iOS SDK
Version: 2.9.0
Cflags: -I$sysroot/usr/include/libxml2
Libs: -lxml2
EOF
cat >"$out/lib/pkgconfig/zlib.pc" <<EOF
Name: zlib
Description: zlib of the iOS SDK
Version: 1.2.0
Cflags:
Libs: -lz
EOF

begin openssl
cd "$src/openssl-$openssl_ver"
# The ios targets pick the compiler and SDK through xcrun themselves.
run env -u CC -u CFLAGS -u CPPFLAGS -u LDFLAGS ./Configure "$openssl_target" no-shared no-tests \
  no-docs no-apps no-module no-engine no-async "$openssl_min" --prefix="$out" --libdir=lib
run make -j"$jobs" build_libs
run make install_dev

begin openconnect
cd "$src/openconnect-$openconnect_ver"
export CC="$clang -target $target -isysroot $sysroot"
export CFLAGS="-O2 -fPIC" CPPFLAGS="-I$out/include" LDFLAGS="-L$out/lib"
export PKG_CONFIG_LIBDIR="$out/lib/pkgconfig" PKG_CONFIG_PATH=""
export PKG_CONFIG="pkg-config --static"
# No program, only the library: the core drives it through cgo (internal/octunnel). The SDK has
# no net/if_utun.h, so tun.c builds without its own tun device; the tunnel goes to a socket
# the core hands over (openconnect_setup_tun_fd). No external browser (configure would find the
# build Mac's /usr/bin/open) and no iconv (it lives in a separate library on iOS; login forms
# are UTF-8). No statfs: only the command line's key password from the file system ID uses it,
# and App Store Connect refuses a build that calls a disk space API without a reason (CI checks
# the binaries).
run ./configure --host="$host" --prefix="$out" --disable-shared --enable-static --with-pic \
  --with-openssl --without-gnutls --disable-nls --disable-docs --without-libpskc --without-stoken \
  --without-libpcsclite --without-gssapi --without-libproxy --without-lz4 --with-builtin-json \
  --with-vpnc-script=/dev/null --without-java --without-external-browser am_cv_func_iconv=no \
  ac_cv_func_statfs=no
run make -j"$jobs" libopenconnect.la
run make install-libLTLIBRARIES install-includeHEADERS install-pkgconfigDATA
unset CC CFLAGS CPPFLAGS LDFLAGS PKG_CONFIG_LIBDIR PKG_CONFIG_PATH PKG_CONFIG

begin install
# Relocatable pkg-config files, so a restored cache in another checkout or with another Xcode
# still works. openconnect.h does not include libxml2, so cgo needs no SDK path.
for pc in "$out"/lib/pkgconfig/*.pc; do
  OUT="$out" perl -pi -e 's/^prefix=\Q$ENV{OUT}\E$/prefix=\${pcfiledir}\/..\/../' "$pc"
done
perl -pi -e 's/^Cflags:.*$/Cflags:/' "$out/lib/pkgconfig/libxml-2.0.pc"
# configure names the Mac's PCSC framework for every Darwin build, even without libpcsclite.
perl -pi -e 's/-Wl,-framework -Wl,PCSC//' "$out/lib/pkgconfig/openconnect.pc"
rm -f "$out"/lib/*.la
rm -rf "${out:?}/lib/cmake" "${out:?}/share" "${out:?}/bin"
mkdir -p "$out/licenses"
cp "$src/openconnect-$openconnect_ver/COPYING.LGPL" "$out/licenses/openconnect-COPYING.LGPL"
cp "$src/openssl-$openssl_ver/LICENSE.txt" "$out/licenses/openssl-LICENSE.txt"

for lib in libopenconnect libssl libcrypto; do
  [ -f "$out/lib/$lib.a" ] || die "$lib.a was not built"
  archs=$(lipo -archs "$out/lib/$lib.a")
  [ "$archs" = "$arch" ] || die "$lib.a is for $archs, not $arch"
  echo "$lib.a: $(wc -c <"$out/lib/$lib.a") bytes"
done
PKG_CONFIG_LIBDIR="$out/lib/pkgconfig" pkg-config --static --libs openconnect >/dev/null \
  || die "openconnect.pc does not resolve"
