#!/usr/bin/env bash
# openconnect.exe for Windows with every DLL it loads and Wintun, into OUT-DIR.
# OpenConnect-GUI's installer has no openconnect.exe and neither MSYS2 nor Fedora package one,
# so uni-vpn cross-builds the release the way OpenConnect's own Windows CI does.
# Runs in a Fedora container (see ci.yml).
# Usage: packaging/windows/build-openconnect.sh OUT-DIR
set -euo pipefail
out=$(realpath -m "$1")
version=9.21
# The release, signed by David Woodhouse (BE07D9FD54809AB2C4B0FF5F63762CDA67E2F359).
sha256=5b32369467db6e5f317aa1ed12cfcbb81ed00bdbc765450b6bfcbdc300944a58
root=/usr/x86_64-w64-mingw32/sys-root/mingw
dnf install -y -q make gcc pkgconf-pkg-config gettext unzip mingw64-gcc mingw64-binutils \
  mingw64-gnutls mingw64-libxml2 mingw64-zlib mingw64-gettext >/dev/null
work=$(mktemp -d)
curl -fsSL -o "$work/src.tar.gz" "https://www.infradead.org/openconnect/download/openconnect-$version.tar.gz"
echo "$sha256  $work/src.tar.gz" | sha256sum -c -
tar -xzf "$work/src.tar.gz" -C "$work"
cd "$work/openconnect-$version"
# uni-vpn passes its own script; this default is never used.
mingw64-configure --disable-nls --without-libpskc --without-stoken --without-libproxy \
  --with-vpnc-script=vpnc-script-win.js >/dev/null
touch vpnc-script-win.js  # else make downloads the latest one
make -s -j"$(nproc)" >/dev/null
mkdir -p "$out"
cp .libs/openconnect.exe .libs/libopenconnect-5.dll "$out/"
cp COPYING.LGPL "$out/openconnect-COPYING.LGPL"
# The DLLs it loads, and theirs, from Fedora; Windows' own ones are not in $root/bin and stay out.
todo=("$out/openconnect.exe" "$out/libopenconnect-5.dll")
while ((${#todo[@]})); do
  file="${todo[0]}"
  todo=("${todo[@]:1}")
  for dll in $(x86_64-w64-mingw32-objdump -p "$file" | awk '/DLL Name:/ { print $3 }'); do
    src=$(find "$root/bin" -maxdepth 1 -iname "$dll" | head -n1)
    if [[ -n "$src" && ! -e "$out/$(basename "$src")" ]]; then
      cp "$src" "$out/"
      todo+=("$out/$(basename "$src")")
    fi
  done
done
# Wintun, signed by WireGuard LLC, checked against the SHA-256 of the release.
curl -fsSL -o "$work/wintun.zip" https://www.wintun.net/builds/wintun-0.14.1.zip
echo "07c256185d6ee3652e09fa55c0b673e2624b565e02c4b9091c79ca7d2f24ef51  $work/wintun.zip" | sha256sum -c -
unzip -o -j -q "$work/wintun.zip" wintun/bin/amd64/wintun.dll -d "$out"
ls -l "$out"
