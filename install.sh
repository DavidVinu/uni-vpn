#!/usr/bin/env bash
# uni-vpn installieren: Pakete holen, dann "uni-vpn setup".
# Aufruf: ./install.sh [--uninstall | --update] [--dry-run] [--user UNI-ID]
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

mode=setup
extra=()
while [ $# -gt 0 ]; do
  case "$1" in
    --uninstall) mode=uninstall ;;
    --update) mode=update ;;
    --dry-run) extra+=(--dry-run) ;;
    --user) extra+=(--user "$2"); shift ;;
    -h|--help) sed -n '2,3p' "$0"; exit 0 ;;
    *) echo "Unbekannte Option: $1"; exit 2 ;;
  esac
  shift
done
dry=0
for a in "${extra[@]:-}"; do [ "$a" = "--dry-run" ] && dry=1; done

case "$(uname -s)" in
  Linux)
    PY=/usr/bin/python3
    if [ "$mode" = setup ]; then
      missing=()
      for p in openconnect ocproxy libsecret-tools; do
        dpkg -s "$p" >/dev/null 2>&1 || missing+=("$p")
      done
      if [ ${#missing[@]} -gt 0 ]; then
        if [ "$dry" = 1 ]; then
          echo "-> wuerde installieren: ${missing[*]}"
        elif command -v apt-get >/dev/null; then
          echo "-> installiere ${missing[*]} (sudo fragt nach deinem Passwort)"
          sudo apt-get install -y "${missing[@]}"
        else
          echo "Kein apt-get gefunden. Bitte selbst installieren: ${missing[*]}"
          exit 1
        fi
      fi
    fi
    ;;
  Darwin)
    BREW=""
    for b in /opt/homebrew/bin/brew /usr/local/bin/brew; do [ -x "$b" ] && BREW=$b && break; done
    if [ -z "$BREW" ]; then
      echo "Homebrew fehlt. Erst https://brew.sh installieren (Admin-Passwort, dauert ein paar Minuten), dann install.sh erneut."
      exit 1
    fi
    PREFIX=$("$BREW" --prefix)
    PY="$PREFIX/bin/python3"
    if [ "$mode" = setup ]; then
      missing=()
      for f in openconnect ocproxy; do "$BREW" list --versions "$f" >/dev/null 2>&1 || missing+=("$f"); done
      [ -x "$PY" ] || missing+=(python)
      if [ ${#missing[@]} -gt 0 ]; then
        if [ "$dry" = 1 ]; then echo "-> wuerde installieren: brew install ${missing[*]}"; else "$BREW" install "${missing[@]}"; fi
      fi
    fi
    if [ ! -x "$PY" ] && [ "$dry" = 1 ]; then PY=$(command -v python3); fi
    ;;
  *) echo "Nicht unterstuetzt: $(uname -s)"; exit 1 ;;
esac

"$PY" -c 'import sys; sys.exit(0 if sys.version_info >= (3, 11) else 1)' || { echo "Python >= 3.11 noetig, gefunden: $("$PY" --version)"; exit 1; }
exec "$PY" bin/uni-vpn "$mode" "${extra[@]:-}"
