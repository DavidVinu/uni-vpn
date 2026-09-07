"""Statusseite und JSON-API auf 127.0.0.1:<http_port>. Reines asyncio, kein http.server."""

from __future__ import annotations

import asyncio
import json
import logging
import re

MAX_HEADER = 16 * 1024
MAX_BODY = 64 * 1024

# Nur dieses Muster darf in Access-Control-Allow-Origin zurueckgespiegelt werden.
EXTENSION_ORIGIN = re.compile(r"(chrome|moz)-extension://[A-Za-z0-9-]+")
CONTENT_LENGTH = re.compile(r"[0-9]+")

STATUS_PAGE = """<!doctype html>
<html lang="de"><head><meta charset="utf-8"><title>Uni VPN</title>
<meta name="viewport" content="width=device-width, initial-scale=1">
<style>
body{font:15px/1.4 system-ui,sans-serif;max-width:40rem;margin:2rem auto;padding:0 1rem;color:#222;background:#fafafa}
h1{font-size:1.3rem;margin:0 0 1rem}
.state{display:flex;align-items:center;gap:.6rem;font-size:1.1rem;margin:1rem 0}
.dot{width:.9rem;height:.9rem;border-radius:50%;background:#999;flex:none}
.dot.connected{background:#2a9d4b}.dot.connecting,.dot.disconnecting{background:#e0b400}
.dot.auth_failed,.dot.keyring,.dot.error{background:#d33}.dot.blocked{background:#e07b00}
button{font:inherit;padding:.45rem .9rem;margin-right:.5rem;border:1px solid #888;border-radius:6px;background:#fff;cursor:pointer}
button:disabled{opacity:.5;cursor:default}
form{margin-top:1.5rem;padding-top:1rem;border-top:1px solid #ddd}
input[type=password]{font:inherit;padding:.4rem;width:14rem}
pre{background:#eee;padding:.6rem;font-size:12px;max-height:16rem;overflow:auto;white-space:pre-wrap}
small{color:#666}
</style></head><body>
<h1>Uni VPN</h1>
<div class="state"><span class="dot" id="dot"></span><span id="msg">wird geladen</span></div>
<div><button id="connect">Verbinden</button><button id="disconnect">Trennen</button></div>
<div id="meta"><small></small></div>
<form id="pwform"><label>Uni-Passwort im Keyring ablegen:<br><input type="password" id="pw" autocomplete="current-password"></label>
<button type="submit">Speichern</button> <small id="pwmsg"></small></form>
<details><summary>Log</summary><pre id="log"></pre></details>
<script>
const H = {"X-Uni-VPN": "1", "Content-Type": "application/json"};
async function post(path, body) { return fetch(path, {method: "POST", headers: H, body: body ? JSON.stringify(body) : "{}"}); }
async function refresh() {
  try {
    const s = await (await fetch("/status.json")).json();
    document.getElementById("dot").className = "dot " + s.state;
    document.getElementById("msg").textContent = s.message;
    document.querySelector("#meta small").textContent =
      s.user + " @ " + s.host + " | SOCKS 127.0.0.1:" + s.socks_port + " | Verbindungen: " + s.active_connections +
      " | Version " + s.version;
    document.getElementById("log").textContent = (s.log_tail || []).join("\\n");
    document.getElementById("connect").disabled = ["connected", "connecting"].includes(s.state);
    document.getElementById("disconnect").disabled = ["idle", "disconnecting"].includes(s.state);
  } catch (e) { document.getElementById("msg").textContent = "Daemon nicht erreichbar"; }
}
document.getElementById("connect").onclick = () => post("/api/connect").then(refresh);
document.getElementById("disconnect").onclick = () => post("/api/disconnect").then(refresh);
document.getElementById("pwform").onsubmit = async (e) => {
  e.preventDefault();
  const r = await post("/api/password", {password: document.getElementById("pw").value});
  document.getElementById("pwmsg").textContent = r.ok ? "gespeichert" : "Fehler: " + (await r.text());
  document.getElementById("pw").value = "";
  refresh();
};
refresh(); setInterval(refresh, 2000);
</script></body></html>
"""


def allowed_origin(origin: str | None, port: int) -> bool:
    # "null" (sandboxed iframe, data:-Seite) ist ein fremder Origin, kein fehlender.
    if origin is None or origin == "":
        return True
    if origin == f"http://127.0.0.1:{port}":
        return True
    return EXTENSION_ORIGIN.fullmatch(origin) is not None


def allowed_host(host: str | None, port: int) -> bool:
    """Schutz vor DNS-Rebinding: nur Loopback-Namen, sonst liest eine fremde Seite same-origin."""
    return host in ("127.0.0.1", f"127.0.0.1:{port}", "localhost", f"localhost:{port}")


class HttpApi:
    def __init__(self, daemon, host: str, port: int, log: logging.Logger):
        self.daemon = daemon
        self.host = host
        self.port = port
        self.log = log
        self._server: asyncio.AbstractServer | None = None

    async def start(self) -> None:
        self._server = await asyncio.start_server(self._handle, self.host, self.port)

    async def stop(self) -> None:
        if self._server:
            self._server.close()
            try:
                await asyncio.wait_for(self._server.wait_closed(), 5)
            except asyncio.TimeoutError:
                self.log.warning("HTTP: Verbindungen nicht rechtzeitig geschlossen")

    async def _handle(self, reader: asyncio.StreamReader, writer: asyncio.StreamWriter) -> None:
        try:
            head = await asyncio.wait_for(reader.readuntil(b"\r\n\r\n"), 10)
        except (asyncio.IncompleteReadError, asyncio.LimitOverrunError, asyncio.TimeoutError, ConnectionError):
            writer.close()
            return
        if len(head) > MAX_HEADER:
            writer.close()
            return
        try:
            request_line, *header_lines = head.decode("latin-1").split("\r\n")
            method, target, _version = request_line.split(" ", 2)
        except ValueError:
            writer.close()
            return
        headers: dict[str, str] = {}
        for line in header_lines:
            if ": " in line:
                key, value = line.split(": ", 1)
                headers[key.lower()] = value
        raw_length = headers.get("content-length", "0").strip() or "0"
        if not CONTENT_LENGTH.fullmatch(raw_length):
            await self._respond(writer, 400, "text/plain", b"ungueltige Content-Length", headers)
            return
        length = int(raw_length)
        body = b""
        if 0 < length <= MAX_BODY:
            try:
                body = await asyncio.wait_for(reader.readexactly(length), 10)
            except (asyncio.IncompleteReadError, asyncio.TimeoutError, ConnectionError):
                writer.close()
                return
        elif length > MAX_BODY:
            await self._respond(writer, 413, "text/plain", b"zu gross", headers)
            return
        path = target.split("?", 1)[0]
        if not allowed_host(headers.get("host"), self.port):
            await self._respond(writer, 403, "text/plain", b"verboten", headers)
            return
        try:
            status, ctype, payload = await self._route(method, path, headers, body)
        except Exception as exc:  # noqa: BLE001 - Statusseite darf nie sterben
            self.log.exception("HTTP-Fehler: %s", exc)
            status, ctype, payload = 500, "text/plain", b"interner Fehler"
        await self._respond(writer, status, ctype, payload, headers)

    async def _respond(self, writer, status: int, ctype: str, payload: bytes, request_headers: dict[str, str]) -> None:
        reasons = {200: "OK", 204: "No Content", 400: "Bad Request", 403: "Forbidden", 404: "Not Found",
                   405: "Method Not Allowed", 413: "Payload Too Large", 500: "Internal Server Error"}
        lines = [f"HTTP/1.1 {status} {reasons.get(status, 'Status')}",
                 f"Content-Type: {ctype}; charset=utf-8" if ctype.startswith("text/") else f"Content-Type: {ctype}",
                 f"Content-Length: {len(payload)}", "Cache-Control: no-store", "Connection: close",
                 "X-Content-Type-Options: nosniff", "X-Frame-Options: DENY"]
        origin = request_headers.get("origin")
        if origin and allowed_origin(origin, self.port):
            lines += [f"Access-Control-Allow-Origin: {origin}", "Vary: Origin",
                      "Access-Control-Allow-Methods: GET, POST, OPTIONS",
                      "Access-Control-Allow-Headers: X-Uni-VPN, Content-Type"]
        writer.write(("\r\n".join(lines) + "\r\n\r\n").encode() + payload)
        try:
            await writer.drain()
        except ConnectionError:
            pass
        writer.close()

    async def _route(self, method: str, path: str, headers: dict[str, str], body: bytes):
        if method == "OPTIONS":
            return 204, "text/plain", b""
        if method == "GET":
            if path == "/":
                return 200, "text/html", STATUS_PAGE.encode()
            if path == "/status.json":
                return 200, "application/json", json.dumps(self.daemon.status()).encode()
            return 404, "text/plain", b"nicht gefunden"
        if method != "POST":
            return 405, "text/plain", b"Methode nicht erlaubt"
        if headers.get("x-uni-vpn") != "1" or not allowed_origin(headers.get("origin"), self.port):
            return 403, "text/plain", b"verboten"
        if path == "/api/connect":
            await self.daemon.request_connect()
        elif path == "/api/disconnect":
            await self.daemon.request_disconnect()
        elif path == "/api/password":
            try:
                data = json.loads(body.decode("utf-8"))
                password = data["password"]
            except (ValueError, KeyError, TypeError, UnicodeDecodeError):
                return 400, "text/plain", b"JSON mit 'password' erwartet"
            if not isinstance(password, str) or not password:
                return 400, "text/plain", b"Passwort leer"
            if "\n" in password or "\r" in password:
                return 400, "text/plain", "Passwort darf keinen Zeilenumbruch enthalten".encode()
            try:
                await self.daemon.set_password(password)
            except Exception as exc:  # noqa: BLE001 - Fehlertext geht an die Seite
                return 500, "text/plain", str(exc).encode()
        else:
            return 404, "text/plain", b"nicht gefunden"
        return 200, "application/json", json.dumps({"ok": True, "state": self.daemon.state.value}).encode()
