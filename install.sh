#!/usr/bin/env bash
# Install uni-vpn on Linux or macOS: fetch packages, then run "uni-vpn setup".
# Usage: ./install.sh [--uninstall | --update] [--dry-run] [--no-gui] [--user UNIVERSITY-ID]
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

mode=setup
dry=0
extra=()
while [ $# -gt 0 ]; do
  case "$1" in
    --uninstall) mode=uninstall ;;
    --update) mode=update ;;
    --dry-run) extra+=(--dry-run); dry=1 ;;
    --no-gui) extra+=(--no-gui) ;;
    --user)
      [ $# -ge 2 ] || { echo "--user needs a university ID"; exit 2; }
      extra+=(--user "$2"); shift ;;
    -h|--help) sed -n '2,3p' "$0"; exit 0 ;;
    *) echo "Unknown option: $1"; exit 2 ;;
  esac
  shift
done
# --no-gui and --user only mean something to "setup".
if [ "$mode" != setup ]; then
  filtered=()
  for a in "${extra[@]:-}"; do [ "$a" = "--dry-run" ] && filtered+=("$a"); done
  extra=("${filtered[@]:-}")
fi

say() { echo "-> $*"; }

run_root() {
  if [ "$dry" = 1 ]; then say "would run: $*"; return 0; fi
  if [ "$(id -u)" = 0 ]; then "$@"; else
    say "$* (sudo asks for your password)"
    sudo "$@"
  fi
}

# First Python >= 3.11 found, or nothing.
find_python() {
  local candidate
  for candidate in "$@" python3.13 python3.12 python3.11 python3; do
    if command -v "$candidate" >/dev/null 2>&1 &&
       "$candidate" -c 'import sys; sys.exit(0 if sys.version_info >= (3, 11) else 1)' 2>/dev/null; then
      command -v "$candidate"
      return 0
    fi
  done
  return 1
}

linux_packages() {
  local missing=()
  if command -v apt-get >/dev/null && command -v dpkg >/dev/null; then
    local p
    for p in openconnect ocproxy libsecret-tools; do
      dpkg -s "$p" >/dev/null 2>&1 || missing+=("$p")
    done
    find_python >/dev/null || missing+=(python3)
    if [ ${#missing[@]} -gt 0 ]; then
      run_root apt-get update -qq || true
      run_root apt-get install -y "${missing[@]}"
    fi
    # Ubuntu 22.04 and Debian 11 ship Python 3.10; 3.11 is a separate package there.
    if [ "$dry" = 0 ] && ! find_python >/dev/null; then
      run_root apt-get install -y python3.11 || true
    fi
  elif command -v dnf >/dev/null; then
    command -v openconnect >/dev/null || missing+=(openconnect)
    command -v ocproxy >/dev/null || missing+=(ocproxy)
    command -v secret-tool >/dev/null || missing+=(libsecret)
    find_python >/dev/null || missing+=(python3)
    [ ${#missing[@]} -eq 0 ] || run_root dnf install -y "${missing[@]}"
  elif command -v zypper >/dev/null; then
    command -v openconnect >/dev/null || missing+=(openconnect)
    command -v ocproxy >/dev/null || missing+=(ocproxy)
    command -v secret-tool >/dev/null || missing+=(libsecret-tools)
    find_python >/dev/null || missing+=(python311)
    [ ${#missing[@]} -eq 0 ] || run_root zypper --non-interactive install "${missing[@]}"
  elif command -v pacman >/dev/null; then
    command -v openconnect >/dev/null || missing+=(openconnect)
    command -v secret-tool >/dev/null || missing+=(libsecret)
    find_python >/dev/null || missing+=(python)
    [ ${#missing[@]} -eq 0 ] || run_root pacman -S --needed --noconfirm "${missing[@]}"
    command -v ocproxy >/dev/null || echo "-> ocproxy is only in the AUR on Arch: install it (for example: yay -S ocproxy), then run this again"
  else
    echo "-> unknown package manager: install openconnect, ocproxy, secret-tool (libsecret) and Python >= 3.11 yourself"
  fi
  if ! command -v systemctl >/dev/null || ! systemctl --user show-environment >/dev/null 2>&1; then
    if [ "$dry" = 0 ]; then
      echo "No systemd user session found. uni-vpn runs as a systemd user service; log in to a desktop session and try again."
      exit 1
    fi
  fi
}

macos_packages() {
  BREW=""
  for b in /opt/homebrew/bin/brew /usr/local/bin/brew; do [ -x "$b" ] && BREW=$b && break; done
  if [ -z "$BREW" ]; then
    if [ "$dry" = 1 ]; then say "would install Homebrew (https://brew.sh)"; return 0; fi
    echo "uni-vpn needs Homebrew (https://brew.sh) for openconnect and ocproxy."
    printf "Install Homebrew now? It asks for your Mac password. [Y/n] "
    read -r answer </dev/tty || answer=n
    case "$answer" in
      ""|y|Y|yes|j|J|ja) ;;
      *) echo "Install Homebrew from https://brew.sh, then run this again."; exit 1 ;;
    esac
    /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
    for b in /opt/homebrew/bin/brew /usr/local/bin/brew; do [ -x "$b" ] && BREW=$b && break; done
    [ -n "$BREW" ] || { echo "Homebrew installation did not finish"; exit 1; }
  fi
  local prefix missing=()
  prefix=$("$BREW" --prefix)
  local f
  for f in openconnect ocproxy; do "$BREW" list --versions "$f" >/dev/null 2>&1 || missing+=("$f"); done
  find_python "$prefix/bin/python3" >/dev/null || missing+=(python)
  if [ ${#missing[@]} -gt 0 ]; then
    if [ "$dry" = 1 ]; then say "would run: brew install ${missing[*]}"; else "$BREW" install "${missing[@]}"; fi
  fi
  BREW_PYTHON="$prefix/bin/python3"
}

PY=""
case "$(uname -s)" in
  Linux)
    [ "$mode" = setup ] && linux_packages
    PY=$(find_python /usr/bin/python3 || true)
    ;;
  Darwin)
    BREW_PYTHON=""
    if [ "$mode" = setup ]; then macos_packages; fi
    for b in /opt/homebrew/bin/python3 /usr/local/bin/python3; do [ -z "$BREW_PYTHON" ] && [ -x "$b" ] && BREW_PYTHON=$b; done
    # Homebrew's Python first: /usr/bin/python3 is 3.9 or a stub that opens a dialog.
    PY=$(find_python ${BREW_PYTHON:+"$BREW_PYTHON"} || true)
    ;;
  *) echo "Not supported: $(uname -s). On Windows use install.ps1."; exit 1 ;;
esac

if [ -z "$PY" ]; then
  if [ "$dry" = 1 ] && command -v python3 >/dev/null; then
    PY=$(command -v python3)
  else
    echo "Python >= 3.11 is required and was not found."
    exit 1
  fi
fi
exec "$PY" bin/uni-vpn "$mode" "${extra[@]:-}"
