#!/usr/bin/env bash
# Builds what the .pkg ships for this Mac's processor (arm64 or x86_64) into OUT/<arch>:
# openconnect and ocproxy with their libraries. The Go core comes from packaging/stage-app.sh.
# Runs on a GitHub macOS runner of the matching processor; needs Homebrew.
# Usage: packaging/macos/build-runtime.sh OUT
set -euo pipefail
out="$1/$(uname -m)"
case "$(uname -m)" in
  arm64 | x86_64) ;;
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

# The .pkg installs on macOS from its distribution.xml minimum on. What Homebrew has no bottle
# for (on Intel: gmp and others) it builds for the runner's newer macOS, and dyld on the older
# one refuses such a library. Marked for the minimum instead; the macos-pkg job (ci.yml) runs the
# Intel programs on that macOS, so a library that really needs the newer one fails there.
minimum=$(sed -n 's/.*<os-version min="\([0-9.]*\)".*/\1/p' "$(dirname "$0")/distribution.xml")
newer() { awk -v a="$1" -v b="$2" 'BEGIN { split(a, x, "."); split(b, y, "."); exit !(x[1] > y[1] || (x[1] == y[1] && x[2] + 0 > y[2] + 0)) }'; }
find "$out" -type f \( -perm -u+x -o -name '*.dylib' -o -name '*.so' \) -print0 |
  while IFS= read -r -d '' f; do
    file -b "$f" | grep -q Mach-O || continue
    build=$(vtool -show-build "$f" 2>/dev/null || true)
    minos=$(awk '$1 == "minos" { v = $2 } END { print v }' <<<"$build")
    sdk=$(awk '$1 == "sdk" { v = $2 } END { print v }' <<<"$build")
    if [ -z "$minos" ] || ! newer "$minos" "$minimum"; then continue; fi
    vtool -set-build-version macos "$minimum" "$sdk" -replace -output "$f.new" "$f"
    chmod "$(stat -f %Lp "$f")" "$f.new"
    mv "$f.new" "$f"
    echo "built for macOS $minos, marked for $minimum: ${f#"$out/"}"
  done

# Every changed file needs a signature again; ad hoc unless the release signs it later.
find "$out" -type f \( -perm -u+x -o -name '*.dylib' -o -name '*.so' \) -print0 |
  xargs -0 -n 50 sh -c 'for f; do file -b "$f" | grep -q Mach-O && codesign --force -s - "$f" 2>/dev/null || true; done' sh

# Nothing may point at Homebrew: the users' Macs do not have it.
leaks=$(find "$out/bin" "$out/lib" -type f -exec otool -L {} + | grep -E '^\s+(/opt/homebrew|/usr/local)/' || true)
if [ -n "$leaks" ]; then echo "still linked against Homebrew:"; echo "$leaks"; exit 1; fi
"$out/bin/openconnect" --version | head -n 1
du -sh "$out"
