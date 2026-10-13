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

# The .pkg installs on macOS from its distribution.xml minimum on.
minimum=$(sed -n 's/.*<os-version min="\([0-9.]*\)".*/\1/p' "$(dirname "$0")/distribution.xml")

brew install --quiet openconnect ocproxy dylibbundler
prefix=$(brew --prefix)
# Homebrew pours its bottles for the runner's newer macOS, which can need what the minimum lacks
# (openconnect's for Apple Silicon calls strchrnul, new in macOS 15.4). Where Homebrew still has
# bottles for the minimum, those replace them.
case "${minimum%.0}" in
  14) os=sonoma ;;
  *) echo "which Homebrew bottles are for macOS $minimum?"; exit 1 ;;
esac
if [ "$(uname -m)" = arm64 ]; then tag="arm64_$os"; else tag="$os"; fi
bottles=$(mktemp -d)
for f in openconnect ocproxy $(brew deps --union openconnect ocproxy); do
  brew info --json=v2 "$f" | jq -e --arg t "$tag" '.formulae[0].bottle.stable.files[$t]' >/dev/null || continue
  brew fetch --quiet --bottle-tag="$tag" "$f"
  # Under its usual name (the download cache puts a hash in front), which brew install reads.
  cached=$(brew --cache --bottle-tag="$tag" "$f")
  bottle="$bottles/$(basename "$cached" | sed -E 's/^[0-9a-f]{64}--//')"
  cp "$cached" "$bottle"
  brew uninstall --quiet --ignore-dependencies "$f"
  brew install --quiet --ignore-dependencies "$bottle"
  echo "the bottle for macOS $minimum: $f"
done
cp "$prefix/bin/openconnect" "$prefix/bin/ocproxy" "$out/bin/"
chmod u+w "$out/bin/"*
# Copies every Homebrew library they load next to them and points them there.
dylibbundler -cd -of -b -s "$prefix/lib" -x "$out/bin/openconnect" -x "$out/bin/ocproxy" -d "$out/lib" -p @executable_path/../lib/

# The rest is for the runner's newer macOS (what Homebrew has no bottle for the minimum for, or
# builds itself), and dyld on the minimum refuses such a library. Marked for the minimum instead;
# below, a library that really needs the newer macOS fails.
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

# dyld on the minimum also refuses a library it lacks or a symbol its libraries lack. The runner
# has a newer macOS: the check goes by a list made from the minimum's SDK (system-symbols.py).
symbols="$(dirname "$0")/macos${minimum%.0}-symbols.txt.gz"
if [ ! -e "$symbols" ]; then echo "no list of what macOS $minimum has: make $symbols"; exit 1; fi
python3 "$(dirname "$0")/system-symbols.py" check "$symbols" "$out"

# Every changed file needs a signature again; ad hoc unless the release signs it later.
find "$out" -type f \( -perm -u+x -o -name '*.dylib' -o -name '*.so' \) -print0 |
  xargs -0 -n 50 sh -c 'for f; do file -b "$f" | grep -q Mach-O && codesign --force -s - "$f" 2>/dev/null || true; done' sh

# Nothing may point at Homebrew: the users' Macs do not have it.
leaks=$(find "$out/bin" "$out/lib" -type f -exec otool -L {} + | grep -E '^\s+(/opt/homebrew|/usr/local)/' || true)
if [ -n "$leaks" ]; then echo "still linked against Homebrew:"; echo "$leaks"; exit 1; fi
"$out/bin/openconnect" --version | head -n 1
du -sh "$out"
