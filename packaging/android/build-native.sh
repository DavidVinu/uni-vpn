#!/usr/bin/env bash
# Cross-compiles openconnect and ocproxy as Android executables for the Android app.
# Usage: packaging/android/build-native.sh ABI OUT-DIR   (ABI: arm64-v8a or x86_64)
#
# Needs $ANDROID_NDK_HOME (NDK r28 tested), curl, perl, make, pkg-config and autoconf,
# automake, libtool (ocproxy ships no configure script).
#
# The outputs OUT-DIR/libopenconnect.so and OUT-DIR/libocproxy.so are NOT shared libraries:
# they are static PIE executables named lib*.so so the app can ship them in jniLibs and
# exec them from nativeLibraryDir (Android only allows exec of files the package manager
# extracted there). Everything is linked statically except Android system libraries.
# OUT-DIR/licenses/ gets the license texts of everything linked in.
#
# Build trees and the download cache live in $UNI_VPN_NATIVE_BUILD_DIR
# (default ~/.cache/uni-vpn-android-native); the per-ABI tree is rebuilt from scratch on
# every run, downloads are reused after their checksum is verified again.
set -euo pipefail

usage() { echo "usage: $0 arm64-v8a|x86_64 OUT-DIR" >&2; exit 2; }
die() { echo "build-native: $*" >&2; exit 1; }
[ $# -eq 2 ] || usage
abi="$1"
case "$abi" in
  arm64-v8a) triple=aarch64-linux-android openssl_target=android-arm64 ;;
  x86_64) triple=x86_64-linux-android openssl_target=android-x86_64 ;;
  *) usage ;;
esac
api=29
[ -n "${ANDROID_NDK_HOME:-}" ] || die "ANDROID_NDK_HOME is not set (point it at an Android NDK, e.g. \$ANDROID_HOME/ndk/28.2.13676358)"
[ -d "$ANDROID_NDK_HOME/toolchains/llvm/prebuilt" ] || die "ANDROID_NDK_HOME=$ANDROID_NDK_HOME does not look like an NDK"
for tool in curl perl make pkg-config autoreconf aclocal automake libtoolize sha256sum; do
  command -v "$tool" >/dev/null || die "missing host tool: $tool"
done

case "$(uname -s)" in
  Linux) host_tag=linux-x86_64 ;;
  Darwin) host_tag=darwin-x86_64 ;;
  *) die "unsupported build host $(uname -s)" ;;
esac
toolchain="$ANDROID_NDK_HOME/toolchains/llvm/prebuilt/$host_tag"
[ -x "$toolchain/bin/$triple$api-clang" ] || die "no $triple$api-clang in $toolchain"

out=$(mkdir -p "$2" && cd "$2" && pwd)
work="${UNI_VPN_NATIVE_BUILD_DIR:-$HOME/.cache/uni-vpn-android-native}"
dl="$work/dl"
build="$work/$abi"
prefix="$build/prefix"
logs="$build/logs"
jobs=$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 2)

# Pinned sources. ocproxy has no release tarball of its own; Debian's orig tarball is
# byte-identical in content to the upstream git tag v1.60 (commit 88b1087f).
openssl_ver=3.5.9
libxml2_ver=2.14.6
libevent_ver=2.1.12-stable
openconnect_ver=9.21
ocproxy_ver=1.60
sources=(
  "openssl-$openssl_ver.tar.gz|https://github.com/openssl/openssl/releases/download/openssl-$openssl_ver/openssl-$openssl_ver.tar.gz|603f5602e2eef00d77fbd429d34dcd5822bb301757a1bc9cdb24c670f1eb859a"
  "libxml2-$libxml2_ver.tar.xz|https://download.gnome.org/sources/libxml2/${libxml2_ver%.*}/libxml2-$libxml2_ver.tar.xz|7ce458a0affeb83f0b55f1f4f9e0e55735dbfc1a9de124ee86fb4a66b597203a"
  "libevent-$libevent_ver.tar.gz|https://github.com/libevent/libevent/releases/download/release-$libevent_ver/libevent-$libevent_ver.tar.gz|92e6de1be9ec176428fd2367677e61ceffc2ee1cb119035037a27d346b0403bb"
  "openconnect-$openconnect_ver.tar.gz|https://www.infradead.org/openconnect/download/openconnect-$openconnect_ver.tar.gz|5b32369467db6e5f317aa1ed12cfcbb81ed00bdbc765450b6bfcbdc300944a58"
  "ocproxy_$ocproxy_ver.orig.tar.gz|https://deb.debian.org/debian/pool/main/o/ocproxy/ocproxy_$ocproxy_ver.orig.tar.gz|a7367647f07df33869e2f79da66b6f104f6495ae806b12a8b8d9ca82fb7899ac"
)

# Runs a build step with its output in a log file, printing the log tail only on failure.
step=""
run() {
  if ! "$@" >>"$logs/$step.log" 2>&1; then
    echo "build-native: [$abi] $step failed: $*" >&2
    tail -n 80 "$logs/$step.log" >&2
    echo "build-native: full log: $logs/$step.log" >&2
    exit 1
  fi
}
begin() { step="$1"; echo "==> [$abi] $1"; }

# Replaces a literal string in a file, failing if it is not there (so a version bump that
# moves the code breaks the build instead of silently shipping an unpatched binary).
patch_literal() {
  local file="$1" from="$2" to="$3"
  grep -qF -- "$from" "$file" || die "patch target not found in $file: $from"
  FROM="$from" TO="$to" perl -0pi -e 's/\Q$ENV{FROM}\E/$ENV{TO}/g' "$file"
  ! grep -qF -- "$from" "$file" || die "patch did not apply in $file: $from"
}

mkdir -p "$dl"
for entry in "${sources[@]}"; do
  IFS='|' read -r file url sum <<<"$entry"
  if [ ! -f "$dl/$file" ] || ! (cd "$dl" && echo "$sum  $file" | sha256sum -c --status); then
    echo "==> downloading $file"
    curl -fsSL --retry 3 -o "$dl/$file.part" "$url"
    mv "$dl/$file.part" "$dl/$file"
  fi
  (cd "$dl" && echo "$sum  $file" | sha256sum -c --quiet) || die "checksum mismatch for $file"
done

rm -rf "$build"
mkdir -p "$build/src" "$prefix" "$logs"
for entry in "${sources[@]}"; do
  tar -xf "$dl/${entry%%|*}" -C "$build/src"
done
src="$build/src"

export PATH="$toolchain/bin:$PATH"
export CC="$triple$api-clang" CXX="$triple$api-clang++"
export AR=llvm-ar RANLIB=llvm-ranlib STRIP=llvm-strip NM=llvm-nm
export CFLAGS="-O2 -fPIC" CPPFLAGS="-I$prefix/include" LDFLAGS="-L$prefix/lib"
export PKG_CONFIG_LIBDIR="$prefix/lib/pkgconfig" PKG_CONFIG_PATH=""
# Static libraries need their private dependencies on the link line too.
export PKG_CONFIG="pkg-config --static"
autoconf_args=(--host="$triple" --prefix="$prefix" --disable-shared --enable-static --with-pic)

begin openssl
cd "$src/openssl-$openssl_ver"
run env ANDROID_NDK_ROOT="$ANDROID_NDK_HOME" CC=clang CFLAGS= CPPFLAGS= LDFLAGS= \
  ./Configure "$openssl_target" no-shared no-tests no-docs no-apps no-module no-engine \
  -D__ANDROID_API__=$api --prefix="$prefix" --libdir=lib
run make -j"$jobs" build_libs
run make install_dev

begin libxml2
cd "$src/libxml2-$libxml2_ver"
run ./configure "${autoconf_args[@]}" --without-python --without-lzma --without-zlib \
  --without-icu --without-http --without-ftp --without-modules --without-debug \
  --without-readline --without-history
run make -j"$jobs"
run make install

begin libevent
cd "$src/libevent-$libevent_ver"
run ./configure "${autoconf_args[@]}" --disable-openssl --disable-samples \
  --disable-libevent-regress --disable-debug-mode --disable-mbedtls
run make -j"$jobs"
run make install

begin openconnect
cd "$src/openconnect-$openconnect_ver"
# openconnect runs --script (and --script-tun) through /bin/sh -c; Android's shell is
# /system/bin/sh and there is no /bin.
patch_literal script.c 'execl("/bin/sh", "/bin/sh", "-c"' 'execl("/system/bin/sh", "/system/bin/sh", "-c"'
patch_literal tun.c 'execl("/bin/sh", "/bin/sh", "-c"' 'execl("/system/bin/sh", "/system/bin/sh", "-c"'
if grep -rn --include='*.c' '"/bin/sh"' . >&2; then die "openconnect still references /bin/sh"; fi
run ./configure "${autoconf_args[@]}" --with-openssl --without-gnutls --disable-nls \
  --without-libpskc --without-stoken --without-libpcsclite --without-gssapi \
  --without-libproxy --without-lz4 --disable-docs --with-builtin-json \
  --with-vpnc-script=/dev/null --without-java
run make -j"$jobs" openconnect
cp openconnect "$build/openconnect"

begin ocproxy
cd "$src/ocproxy-$ocproxy_ver"
# Only ocproxy itself: vpnns (network namespaces, bind mounts over /etc) is Linux desktop only.
run autoreconf --force --install
run ./configure --host="$triple" --disable-vpnns
run make -j"$jobs" ocproxy
cp ocproxy "$build/ocproxy"

begin install
llvm-strip --strip-all -o "$out/libopenconnect.so" "$build/openconnect"
llvm-strip --strip-all -o "$out/libocproxy.so" "$build/ocproxy"
chmod 755 "$out/libopenconnect.so" "$out/libocproxy.so"
rm -rf "$out/licenses"
mkdir -p "$out/licenses"
cp "$src/openconnect-$openconnect_ver/COPYING.LGPL" "$out/licenses/openconnect-COPYING.LGPL"
cp "$src/ocproxy-$ocproxy_ver/LICENSE" "$out/licenses/ocproxy-LICENSE"
cp "$src/ocproxy-$ocproxy_ver/lwip/COPYING" "$out/licenses/lwip-COPYING"
cp "$src/openssl-$openssl_ver/LICENSE.txt" "$out/licenses/openssl-LICENSE.txt"
cp "$src/libxml2-$libxml2_ver/Copyright" "$out/licenses/libxml2-Copyright"
cp "$src/libevent-$libevent_ver/LICENSE" "$out/licenses/libevent-LICENSE"

# Only Android system libraries may be loaded at runtime.
allowed=" libc.so libm.so libdl.so liblog.so libz.so "
for bin in "$out/libopenconnect.so" "$out/libocproxy.so"; do
  llvm-readelf -h "$bin" | grep -q 'Type:.*DYN' || die "$bin is not a PIE executable"
  llvm-readelf -l "$bin" | grep -q 'Requesting program interpreter: /system/bin/linker' \
    || die "$bin has no Android program interpreter"
  needed=$(llvm-readelf -d "$bin" | sed -n 's/.*(NEEDED).*\[\(.*\)\]/\1/p')
  for lib in $needed; do
    case "$allowed" in *" $lib "*) ;; *) die "$bin needs non-system library $lib" ;; esac
  done
  echo "$(basename "$bin"): $(wc -c <"$bin") bytes, NEEDED:" $needed
done
