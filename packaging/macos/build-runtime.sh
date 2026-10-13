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
cellar=$(brew --cellar)
# Homebrew pours its bottles for the runner's newer macOS, which can need what the minimum lacks
# (openconnect's for Apple Silicon calls strchrnul, new in macOS 15.4). Where Homebrew still has
# bottles for the minimum, their programs and libraries replace those, placed as pouring places
# them. Only bottles made for this Cellar or for any.
case "${minimum%.0}" in
  14) os=sonoma ;;
  *) echo "which Homebrew bottles are for macOS $minimum?"; exit 1 ;;
esac
if [ "$(uname -m)" = arm64 ]; then tag="arm64_$os"; else tag="$os"; fi
placed() { sed -e "s|@@HOMEBREW_PREFIX@@|$prefix|g" -e "s|@@HOMEBREW_CELLAR@@|$cellar|g" <<<"$1"; }
# A bottle names its libraries (and itself) by placeholders for Homebrew's folders.
place() {
  local args=() name
  name=$(otool -D "$1" | sed -n 2p)
  case "$name" in @@HOMEBREW_*) args+=(-id "$(placed "$name")") ;; esac
  while IFS= read -r name; do
    args+=(-change "$name" "$(placed "$name")")
  done < <(otool -L "$1" | sed -n 's/^[[:space:]]*\(@@HOMEBREW_[^ ]*\) (compatibility .*/\1/p')
  while IFS= read -r name; do
    args+=(-rpath "$name" "$(placed "$name")")
  done < <(otool -l "$1" | awk '$1 == "path" && $2 ~ /^@@HOMEBREW_/ { print $2 }')
  chmod u+w "$1"
  if [ ${#args[@]} -gt 0 ]; then install_name_tool "${args[@]}" "$1"; fi
  codesign --force -s - "$1"
}
bottles=$(mktemp -d)
for f in openconnect ocproxy $(brew deps --union openconnect ocproxy); do
  # The runner's Homebrew knows only the bottles for its own macOS, Homebrew's website all.
  bottle=$(curl -fsSL "https://formulae.brew.sh/api/formula/$f.json" | jq -c --arg t "$tag" --arg c "$cellar" \
    '.bottle.stable.files[$t] // empty | select(.cellar == $c or .cellar == ":any" or .cellar == ":any_skip_relocation")')
  if [ -z "$bottle" ]; then continue; fi
  curl -fsSL -H "Authorization: Bearer QQ==" -o "$bottles/$f.tar.gz" "$(jq -r .url <<<"$bottle")"
  if ! echo "$(jq -r .sha256 <<<"$bottle")  $bottles/$f.tar.gz" | shasum -a 256 -c --status; then
    echo "the bottle of $f for macOS $minimum is not the one Homebrew names"
    exit 1
  fi
  mkdir "$bottles/$f"
  tar -xzf "$bottles/$f.tar.gz" -C "$bottles/$f"
  (cd "$bottles/$f" && find . -type f) | while IFS= read -r p; do
    # The same version as the one installed, or nothing is replaced (and the check below tells).
    if [ -e "$cellar/$p" ] && file -b "$bottles/$f/$p" | grep -q Mach-O; then
      cp -f "$bottles/$f/$p" "$cellar/$p"
      place "$cellar/$p"
      echo "from the bottle for macOS $minimum: ${p#./}"
    fi
  done
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
