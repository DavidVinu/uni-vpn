#!/usr/bin/env bash
# Builds what the .pkg ships for this Mac's processor (arm64 or x86_64) into OUT/<arch>:
# openconnect and ocproxy with their libraries, and a standalone Python.
# Python is a stopgap until the Go core replaces it (see docs/superpowers/specs).
# Runs on a GitHub macOS runner of the matching processor; needs Homebrew and gh.
# Usage: packaging/macos/build-runtime.sh OUT
set -euo pipefail
out="$1/$(uname -m)"
case "$(uname -m)" in
  arm64) pbs_arch=aarch64 ;;
  x86_64) pbs_arch=x86_64 ;;
  *) echo "unsupported processor $(uname -m)"; exit 1 ;;
esac
rm -rf "$out"
mkdir -p "$out/bin" "$out/lib"

brew install --quiet openconnect ocproxy dylibbundler
prefix=$(brew --prefix)
cp "$prefix/bin/openconnect" "$prefix/bin/ocproxy" "$out/bin/"
chmod u+w "$out/bin/"*
# Copies every Homebrew library they load next to them and points them there.
dylibbundler -cd -of -b -s "$prefix/lib" -x "$out/bin/openconnect" -x "$out/bin/ocproxy" -d "$out/lib" -p @executable_path/../lib/

# Standalone CPython (the build uv and others use), checked against the release's checksums.
tmp=$(mktemp -d)
gh release download --repo astral-sh/python-build-standalone --dir "$tmp" \
  --pattern "cpython-3.12.*-${pbs_arch}-apple-darwin-install_only_stripped.tar.gz" --pattern SHA256SUMS
archive=$(find "$tmp" -name 'cpython-*.tar.gz' | head -n 1)
(cd "$tmp" && grep " $(basename "$archive")\$" SHA256SUMS | shasum -a 256 -c -)
tar -xzf "$archive" -C "$out"
rm -rf "$tmp"
# Not used by uni-vpn: graphical toolkits, the test suite, pip.
rm -rf "$out"/python/lib/python3.*/{tkinter,idlelib,turtledemo,test,ensurepip,lib2to3} \
  "$out"/python/lib/{tcl,tk,itcl,thread}* "$out"/python/share

# Every changed file needs a signature again; ad hoc unless the release signs it later.
find "$out" -type f \( -perm -u+x -o -name '*.dylib' -o -name '*.so' \) -print0 |
  xargs -0 -n 50 sh -c 'for f; do file -b "$f" | grep -q Mach-O && codesign --force -s - "$f" 2>/dev/null || true; done' sh

# Nothing may point at Homebrew: the users' Macs do not have it.
leaks=$(find "$out/bin" "$out/lib" -type f -exec otool -L {} + | grep -E '^\s+(/opt/homebrew|/usr/local)/' || true)
if [ -n "$leaks" ]; then echo "still linked against Homebrew:"; echo "$leaks"; exit 1; fi
"$out/bin/openconnect" --version | head -n 1
"$out/python/bin/python3" --version
du -sh "$out"
