#!/usr/bin/env bash
# Builds what the .pkg ships for this Mac's processor (arm64 or x86_64) into OUT/<arch>:
# openconnect and ocproxy with their libraries and licences. The Go core comes from
# packaging/stage-app.sh.
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

brew install --quiet openconnect ocproxy dylibbundler pkgconf
prefix=$(brew --prefix)
cellar=$(brew --cellar)
# What Homebrew's own openconnect can do; the one built below for the minimum must do the same.
features=$("$prefix/bin/openconnect" --version)
# Homebrew pours its bottles for the runner's newer macOS, which can need what the minimum lacks.
# Where Homebrew still has bottles for the minimum, their programs and libraries replace those,
# placed as pouring places them. Only bottles made for this Cellar or for any.
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
for f in ocproxy $(brew deps --union openconnect ocproxy); do
  # The runner's Homebrew knows only the bottles for its own macOS, Homebrew's website all.
  # Into a file: a retry after a cut-off download starts it over, in a pipe jq would get both.
  curl -fsSL --retry 5 --retry-all-errors -o "$bottles/$f.json" "https://formulae.brew.sh/api/formula/$f.json"
  bottle=$(jq -c --arg t "$tag" --arg c "$cellar" \
    '.bottle.stable.files[$t] // empty | select(.cellar == $c or .cellar == ":any" or .cellar == ":any_skip_relocation")' "$bottles/$f.json")
  if [ -z "$bottle" ]; then continue; fi
  curl -fsSL --retry 5 --retry-all-errors -H "Authorization: Bearer QQ==" -o "$bottles/$f.tar.gz" "$(jq -r .url <<<"$bottle")"
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
# openconnect itself is built here for the minimum, from the source Homebrew builds it from and
# with its dependencies only. Homebrew makes no new bottles for older macOS, so the next rebuild
# of openconnect leaves none for the minimum, and its newer ones call strchrnul (macOS 15.4).
# brew fetch tries the formula's mirrors and checks the source against its sha256; again a minute
# later when the runner cannot reach them for a moment. ocproxy's source has lwIP's licence.
for try in 1 2 3; do
  brew fetch --quiet --build-from-source openconnect ocproxy && break
  if [ "$try" = 3 ]; then exit 1; fi
  sleep 60
done
src=$(mktemp -d)
tar -xzf "$(brew --cache --build-from-source openconnect)" -C "$src" --strip-components 1
# Only its dependencies' pkg-config files, and macOS's zlib and libxml2 as Homebrew uses them.
pc=$(mktemp -d)
printf 'Name: zlib\nDescription: macOS zlib\nVersion: 1.2.12\nLibs: -lz\n' >"$pc/zlib.pc"
printf 'Name: libXML\nDescription: macOS libxml2\nVersion: 2.9.13\nLibs: -lxml2\nCflags: -I%s/usr/include/libxml2\n' \
  "$(xcrun --show-sdk-path)" >"$pc/libxml-2.0.pc"
for f in $(brew deps openconnect); do
  for d in lib/pkgconfig share/pkgconfig; do
    if [ -d "$prefix/opt/$f/$d" ]; then pc="$pc:$prefix/opt/$f/$d"; fi
  done
done
keg="$prefix/opt/openconnect"
chmod -R u+w "$(cd "$keg" && pwd -P)"
(
  cd "$src"
  export MACOSX_DEPLOYMENT_TARGET="$minimum" PKG_CONFIG_LIBDIR="$pc"
  export CPPFLAGS="-I$prefix/opt/gettext/include" LDFLAGS="-L$prefix/opt/gettext/lib"
  ./configure --quiet --prefix="$keg" --sbindir="$keg/bin" --localstatedir="$prefix/var" \
    --with-vpnc-script="$prefix/etc/vpnc/vpnc-script" --disable-dependency-tracking \
    ac_cv_func_strchrnul=no || { grep -E "No package|not found|error" config.log | tail -n 20; exit 1; }
  make -j"$(sysctl -n hw.ncpu)" >/dev/null
  make install >/dev/null
)
if [ "$("$prefix/bin/openconnect" --version)" != "$features" ]; then
  echo "openconnect built for macOS $minimum differs from Homebrew's:"
  diff <(echo "$features") <("$prefix/bin/openconnect" --version) || true
  exit 1
fi
otool -L "$keg/lib/libopenconnect.5.dylib"
cp "$prefix/bin/openconnect" "$prefix/bin/ocproxy" "$out/bin/"
chmod u+w "$out/bin/"*
# Copies every Homebrew library they load next to them and points them there.
dylibbundler -cd -of -b -s "$prefix/lib" -x "$out/bin/openconnect" -x "$out/bin/ocproxy" -d "$out/lib" -p @executable_path/../lib/
# Their licences: openconnect's from its source, the others' from their kegs, where Homebrew
# keeps each formula's licence files.
mkdir -p "$out/licenses"
cp "$src/COPYING.LGPL" "$out/licenses/openconnect-COPYING.LGPL"
cp "$src/json/LICENSE" "$out/licenses/openconnect-json-parser-LICENSE"
# lwIP, built into ocproxy, has its licence only in ocproxy's source.
tar -xzOf "$(brew --cache --build-from-source ocproxy)" '*/lwip/COPYING' >"$out/licenses/lwip-COPYING"
[ -s "$out/licenses/lwip-COPYING" ]
for f in ocproxy $(brew deps --union openconnect ocproxy); do
  for file in "$prefix/opt/$f"/COPYING* "$prefix/opt/$f"/LICENSE* "$prefix/opt/$f"/LICENCE*; do
    if [ -f "$file" ]; then cp "$file" "$out/licenses/$f-$(basename "$file")"; fi
  done
done
chmod 644 "$out/licenses/"*
ls "$out/licenses"

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
