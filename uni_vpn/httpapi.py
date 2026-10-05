"""Status page and JSON API on 127.0.0.1:<http_port>. Plain asyncio, no http.server."""

from __future__ import annotations

import asyncio
import json
import time
import logging
import re
import urllib.parse
from pathlib import Path

from . import config as config_mod
from . import detect, i18n, totp
from . import universities as unis

MAX_HEADER = 16 * 1024
MAX_BODY = 64 * 1024

CONTENT_LENGTH = re.compile(r"[0-9]+")

UI_FILE = Path(__file__).resolve().parent / "ui" / "index.html"


def status_page() -> bytes:
    """The app: setup assistant on first run, status and settings afterwards."""
    return UI_FILE.read_bytes()


def problem(status: int, message, **extra):
    """An error for the app: the English text, plus key and arguments when it can be translated."""
    payload = {"ok": False, **extra, "error": str(message), "error_t": i18n.as_json(message)}
    return status, "application/json", json.dumps(payload).encode()


def allowed_origin(origin: str | None, port: int) -> bool:
    # "null" (sandboxed iframe, data: page) is a foreign origin, not a missing one.
    if origin is None or origin == "":
        return True
    return origin in (f"http://127.0.0.1:{port}", f"http://localhost:{port}")


def allowed_host(host: str | None, port: int) -> bool:
    """Protection against DNS rebinding: loopback names only, otherwise a foreign page reads same-origin."""
    return host in ("127.0.0.1", f"127.0.0.1:{port}", "localhost", f"localhost:{port}")


class HttpApi:
    def __init__(self, daemon, host: str, port: int, log: logging.Logger):
        self.daemon = daemon
        self.host = host
        self.port = port
        self.log = log
        self.detector = detect.probe  # tests replace it, there is no gateway in CI
        self._server: asyncio.AbstractServer | None = None

    async def start(self) -> None:
        self._server = await asyncio.start_server(self._handle, self.host, self.port)

    async def stop(self) -> None:
        if self._server:
            self._server.close()
            try:
                await asyncio.wait_for(self._server.wait_closed(), 5)
            except asyncio.TimeoutError:
                self.log.warning("HTTP: connections not closed in time")

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
            await self._respond(writer, 400, "text/plain", b"invalid Content-Length", headers)
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
            await self._respond(writer, 413, "text/plain", b"too large", headers)
            return
        path = target.split("?", 1)[0]
        if not allowed_host(headers.get("host"), self.port):
            await self._respond(writer, 403, "text/plain", b"forbidden", headers)
            return
        try:
            status, ctype, payload = await self._route(method, path, headers, body, target.partition("?")[2])
        except Exception as exc:  # noqa: BLE001 - the status page must never die
            self.log.exception("HTTP error: %s", exc)
            status, ctype, payload = 500, "text/plain", b"internal error"
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

    async def _route(self, method: str, path: str, headers: dict[str, str], body: bytes, query: str = ""):
        if method == "OPTIONS":
            return 204, "text/plain", b""
        if method == "GET":
            if path == "/":
                return 200, "text/html", status_page()
            if path == "/status.json":
                payload = self.daemon.status()
                # The native app's menus ask with their system languages: ?menu=de-DE,en
                wanted = urllib.parse.parse_qs(query).get("menu")
                if wanted:
                    payload["menu"] = i18n.menu(i18n.negotiate(payload.get("language", ""), wanted[0]))
                return 200, "application/json", json.dumps(payload, ensure_ascii=False).encode()
            if path == "/proxy.pac":
                return 200, "application/x-ns-proxy-autoconfig", self.daemon.pac().encode()
            if path == "/locales.json":
                return 200, "application/json", json.dumps(i18n.catalogs(), ensure_ascii=False).encode()
            if path == "/universities.json":
                payload = {"default": unis.DEFAULT_ID, "universities": unis.public_list()}
                return 200, "application/json", json.dumps(payload).encode()
            return 404, "text/plain", b"not found"
        if method != "POST":
            return 405, "text/plain", b"method not allowed"
        if headers.get("x-uni-vpn") != "1" or not allowed_origin(headers.get("origin"), self.port):
            return 403, "text/plain", b"forbidden"
        if path == "/api/connect":
            await self.daemon.request_connect()
        elif path == "/api/disconnect":
            await self.daemon.request_disconnect()
        elif path == "/api/password":
            try:
                data = json.loads(body.decode("utf-8"))
                password = data["password"]
            except (ValueError, KeyError, TypeError, UnicodeDecodeError):
                return 400, "text/plain", b"expected JSON with 'password'"
            if not isinstance(password, str) or not password:
                return 400, "text/plain", b"password empty"
            if "\n" in password or "\r" in password:
                return problem(400, i18n.t("password.line_break"))
            try:
                await self.daemon.set_password(password)
            except Exception as exc:  # noqa: BLE001 - the error text goes to the page
                return problem(500, i18n.of(exc))
        elif path == "/api/domains":
            try:
                data = json.loads(body.decode("utf-8"))
                text = data["text"]
            except (ValueError, KeyError, TypeError, UnicodeDecodeError):
                return 400, "text/plain", b"expected JSON with 'text'"
            if not isinstance(text, str):
                return 400, "text/plain", b"domains must be text"
            try:
                domains = await self.daemon.set_domains(text)
            except ValueError as exc:
                return problem(400, i18n.of(exc))
            except Exception as exc:  # noqa: BLE001 - the error text goes to the page
                return problem(500, i18n.of(exc))
            return 200, "application/json", json.dumps({"ok": True, "domains": domains}).encode()
        elif path == "/api/auto-update":
            try:
                enabled = json.loads(body.decode("utf-8"))["enabled"]
            except (ValueError, KeyError, TypeError, UnicodeDecodeError):
                return 400, "text/plain", b"expected JSON with 'enabled'"
            if not isinstance(enabled, bool):
                return 400, "text/plain", b"enabled must be true or false"
            try:
                await self.daemon.set_auto_update(enabled)
            except ValueError as exc:
                return problem(409, i18n.of(exc))
            except Exception as exc:  # noqa: BLE001 - the error text goes to the page
                return problem(500, i18n.of(exc))
            return 200, "application/json", json.dumps({"ok": True, "enabled": enabled}).encode()
        elif path == "/api/language":
            try:
                language = json.loads(body.decode("utf-8"))["language"]
            except (ValueError, KeyError, TypeError, UnicodeDecodeError):
                return 400, "text/plain", b"expected JSON with 'language'"
            if not isinstance(language, str) or not i18n.valid(language):
                return 400, "text/plain", b"unknown language"
            try:
                await self.daemon.set_language(language)
            except ValueError as exc:
                return problem(409, i18n.of(exc))
            except Exception as exc:  # noqa: BLE001 - the error text goes to the page
                return problem(500, i18n.of(exc))
            return 200, "application/json", json.dumps({"ok": True, "language": language}).encode()
        elif path == "/api/totp":
            try:
                data = json.loads(body.decode("utf-8"))
                secret = data["secret"]
            except (ValueError, KeyError, TypeError, UnicodeDecodeError):
                return 400, "text/plain", b"expected JSON with 'secret'"
            if not isinstance(secret, str):
                return 400, "text/plain", b"secret must be text"
            try:
                token = totp.normalize(secret)
            except ValueError as exc:
                return problem(400, i18n.of(exc))
            try:
                await self.daemon.set_totp(token)
            except Exception as exc:  # noqa: BLE001 - the error text goes to the page
                return problem(500, i18n.of(exc))
            payload = {"ok": True, "state": self.daemon.state.value, "code": totp.code(token)}
            return 200, "application/json", json.dumps(payload).encode()
        elif path == "/api/totp/check":
            # Check code for a secret without storing it: the setup assistant shows it for the
            # portal's "Testen" step before anything is saved.
            try:
                secret = json.loads(body.decode("utf-8"))["secret"]
                token = totp.normalize(secret if isinstance(secret, str) else "")
            except (KeyError, TypeError, UnicodeDecodeError):
                return 400, "text/plain", b"expected JSON with 'secret'"
            except ValueError as exc:
                return problem(400, i18n.of(exc))
            remaining = totp.STEP - int(time.time()) % totp.STEP
            self.daemon.note_code_shown()
            return 200, "application/json", json.dumps({"ok": True, "code": totp.code(token),
                                                         "remaining": remaining}).encode()
        elif path == "/api/detect":
            # "Not listed" in the setup assistant: what the gateway's login form looks like.
            try:
                data = json.loads(body.decode("utf-8"))
                address, group = data["host"], data.get("group", "")
            except (ValueError, KeyError, TypeError, AttributeError, UnicodeDecodeError):
                return 400, "text/plain", b"expected JSON with 'host'"
            if not isinstance(address, str) or not isinstance(group, str):
                return 400, "text/plain", b"host and group must be text"
            try:
                host, usergroup = detect.split_address(address)
                result = await asyncio.get_running_loop().run_in_executor(
                    None, lambda: self.detector(host, usergroup, group))
            except unis.FieldError as exc:
                return problem(400, i18n.of(exc), field=exc.field)
            return 200, "application/json", json.dumps({"ok": True, **result.as_dict()}).encode()
        elif path == "/api/setup":
            # Errors name the step that has to change, so the assistant can go back to it.
            def fail(status: int, field: str | None, message):
                return problem(status, message, field=field)

            try:
                data = json.loads(body.decode("utf-8"))
                user, password = data["user"], data["password"]
                secret = data.get("secret", "")
                university = data.get("university", unis.DEFAULT_ID)
                overrides = data.get("profile") or {}
            except (ValueError, KeyError, TypeError, AttributeError, UnicodeDecodeError):
                return fail(400, None, "expected JSON with 'user' and 'password'")
            if not all(isinstance(v, str) for v in (user, password, secret, university)) or not isinstance(overrides, dict):
                return fail(400, None, "user, password, secret and university must be text, profile an object")
            if not config_mod.valid_user(user.strip()):
                return fail(400, "user", i18n.t("setup.invalid_user"))
            if not password or "\n" in password or "\r" in password:
                return fail(400, "password", i18n.t("setup.enter_password"))
            token = None
            if secret.strip():
                try:
                    token = totp.normalize(secret)
                except ValueError as exc:
                    return fail(400, "totp", i18n.of(exc))
            try:
                await self.daemon.complete_setup(user, password, token, university, overrides)
            except unis.FieldError as exc:
                return fail(400, exc.field, i18n.of(exc))
            except ValueError as exc:
                return fail(400, "user", i18n.of(exc))
            except Exception as exc:  # noqa: BLE001 - the error text goes to the page
                return fail(500, None, i18n.of(exc))
            payload = {"ok": True, "state": self.daemon.state.value, "code": totp.code(token) if token else None}
            return 200, "application/json", json.dumps(payload).encode()
        else:
            return 404, "text/plain", b"not found"
        return 200, "application/json", json.dumps({"ok": True, "state": self.daemon.state.value}).encode()
