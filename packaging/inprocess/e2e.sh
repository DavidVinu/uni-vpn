#!/usr/bin/env bash
# End to end test of the in-process tunnel (internal/libtunnel, as on iOS) on Linux: the core
# built with libopenconnect logs in to a local ocserv, its SOCKS port fetches a page from the
# gateway's side of the tunnel, and a wrong password ends in auth_failed.
# Needs root or passwordless sudo (ocserv, the CA store) and ports 443, 1080, 1081 and 8080;
# made for GitHub Actions ubuntu-latest.
# Usage: packaging/inprocess/e2e.sh
set -euo pipefail
cd "$(dirname "$0")/../.."

sudo=""
[ "$(id -u)" -eq 0 ] || sudo=sudo

if ! command -v ocserv > /dev/null || ! command -v certtool > /dev/null || ! pkg-config --exists openconnect; then
  $sudo apt-get update -qq
  $sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq ocserv libopenconnect-dev gnutls-bin pkg-config curl > /dev/null
fi

# Ubuntu's package starts its own ocserv on port 443.
if command -v systemctl > /dev/null; then
  $sudo systemctl stop ocserv.socket ocserv.service 2> /dev/null || true
fi

work=$(mktemp -d)
ca_store=/usr/local/share/ca-certificates/uni-vpn-e2e.crt
core_pid="" http_pid=""

logs() {
  echo "--- core"
  cat "$work/core.out" 2> /dev/null || true
  tail -n 80 "$work/state/uni-vpn/daemon.log" 2> /dev/null || true
  echo "--- ocserv"
  tail -n 40 "$work/ocserv.log" 2> /dev/null || true
}

cleanup() {
  [ -n "$core_pid" ] && kill "$core_pid" 2> /dev/null && wait "$core_pid" 2> /dev/null
  [ -n "$http_pid" ] && kill "$http_pid" 2> /dev/null
  if [ -f /run/uni-vpn-e2e-ocserv.pid ]; then
    $sudo kill "$(cat /run/uni-vpn-e2e-ocserv.pid)" 2> /dev/null || true
  fi
  if [ -f "$ca_store" ]; then
    $sudo rm -f "$ca_store"
    $sudo update-ca-certificates --fresh > /dev/null 2>&1 || true
  fi
  rm -rf "$work"
}
trap cleanup EXIT
trap 'logs' ERR

# A CA the system trusts (openconnect checks the gateway like any other) and the gateway's
# certificate for 127.0.0.1.
cat > "$work/ca.tmpl" << 'EOF'
cn = "uni-vpn end to end CA"
ca
cert_signing_key
expiration_days = 2
EOF
cat > "$work/server.tmpl" << 'EOF'
cn = "127.0.0.1"
ip_address = "127.0.0.1"
tls_www_server
signing_key
encryption_key
expiration_days = 2
EOF
certtool --generate-privkey --outfile "$work/ca.key" 2> /dev/null
certtool --generate-self-signed --load-privkey "$work/ca.key" --template "$work/ca.tmpl" \
  --outfile "$work/ca.crt" 2> /dev/null
certtool --generate-privkey --outfile "$work/server.key" 2> /dev/null
certtool --generate-certificate --load-privkey "$work/server.key" --load-ca-certificate "$work/ca.crt" \
  --load-ca-privkey "$work/ca.key" --template "$work/server.tmpl" --outfile "$work/server.crt" 2> /dev/null
$sudo cp "$work/ca.crt" "$ca_store"
$sudo update-ca-certificates > /dev/null

printf 'secret\nsecret\n' | ocpasswd -c "$work/passwd" test
sed "s|@WORK@|$work|g" packaging/inprocess/ocserv.conf > "$work/ocserv.conf"
# The workers (nobody) read nothing from here, but ocserv checks the path.
chmod 755 "$work"
$sudo ocserv -f -c "$work/ocserv.conf" > "$work/ocserv.log" 2>&1 &

# A page on the gateway's side: ocserv gives itself 10.77.0.1 on vpns0 once a client is in.
mkdir "$work/www"
page="in-process tunnel works $RANDOM"
echo "$page" > "$work/www/hello.txt"
python3 -m http.server 8080 --bind 0.0.0.0 --directory "$work/www" > "$work/http.log" 2>&1 &
http_pid=$!

CGO_ENABLED=1 go build -tags libopenconnect -o "$work/uni-vpn" ./cmd/uni-vpn

for _ in $(seq 1 20); do
  (exec 3<> /dev/tcp/127.0.0.1/443) 2> /dev/null && break
  sleep 0.5
done

# Secrets go to the contract tests' stand-in for secret-tool; no desktop proxy settings.
mkdir -p "$work/bin" "$work/keyring"
cp tests/contract/secret-tool "$work/bin/secret-tool"
env -u DISPLAY -u WAYLAND_DISPLAY -u DBUS_SESSION_BUS_ADDRESS -u XDG_CURRENT_DESKTOP \
  UNI_VPN_INPROCESS=1 XDG_CONFIG_HOME="$work/config" XDG_STATE_HOME="$work/state" \
  XDG_DATA_HOME="$work/data" FAKE_KEYRING_DIR="$work/keyring" PATH="$work/bin:$PATH" \
  "$work/uni-vpn" daemon > "$work/core.out" 2>&1 &
core_pid=$!

api() {
  curl -fsS -X POST -H 'X-Uni-VPN: 1' -H 'Content-Type: application/json' --data "$2" "http://127.0.0.1:1081$1"
  echo
}

state() {
  curl -fsS http://127.0.0.1:1081/status.json | python3 -c 'import json, sys; print(json.load(sys.stdin)["state"])'
}

wait_state() {
  local now=""
  for _ in $(seq 1 90); do
    now=$(state 2> /dev/null || true)
    [ "$now" = "$1" ] && return 0
    sleep 1
  done
  echo "state is '$now', expected '$1'" >&2
  return 1
}

for _ in $(seq 1 30); do
  state > /dev/null 2>&1 && break
  sleep 0.5
done

profile='"university": "other", "profile": {"host": "127.0.0.1", "mfa": "none"}'
api /api/setup "{\"user\": \"test\", \"password\": \"secret\", $profile}"
api /api/connect '{}'
wait_state connected
got=$(curl -fsS --max-time 20 --socks5-hostname 127.0.0.1:1080 http://10.77.0.1:8080/hello.txt)
if [ "$got" != "$page" ]; then
  echo "through the tunnel: '$got', expected '$page'" >&2
  false
fi
echo "page through the tunnel: $got"
grep -q "openconnect: CSTP connected" "$work/state/uni-vpn/daemon.log"

api /api/setup "{\"user\": \"test\", \"password\": \"wrong\", $profile}"
api /api/connect '{}'
wait_state auth_failed
curl -fsS http://127.0.0.1:1081/status.json
echo
echo "in-process end to end: ok"
