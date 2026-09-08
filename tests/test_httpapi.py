import asyncio
import json

from uni_vpn import daemon as dm
from uni_vpn import totp
from uni_vpn.httpapi import allowed_origin

from tests.test_daemon import DaemonHarness, wait_state


async def http(port, method, path, headers=None, body=b""):
    reader, writer = await asyncio.open_connection("127.0.0.1", port)
    # Eigene Host- oder Content-Length-Header des Aufrufers ersetzen die Standardwerte.
    hdrs = {"Host": "127.0.0.1", "Content-Length": str(len(body))}
    for key, value in (headers or {}).items():
        hdrs = {k: v for k, v in hdrs.items() if k.lower() != key.lower()}
        hdrs[key] = value
    lines = [f"{method} {path} HTTP/1.1"] + [f"{key}: {value}" for key, value in hdrs.items()]
    writer.write(("\r\n".join(lines) + "\r\n\r\n").encode("latin-1") + body)
    await writer.drain()
    raw = await asyncio.wait_for(reader.read(), 5)
    writer.close()
    head, _, payload = raw.partition(b"\r\n\r\n")
    status_line, *header_lines = head.decode().split("\r\n")
    status = int(status_line.split()[1])
    hdrs = {k.lower(): v for k, v in (h.split(": ", 1) for h in header_lines)}
    return status, hdrs, payload


class OriginTests(DaemonHarness):
    async def test_allowed_origin(self):
        self.assertTrue(allowed_origin(None, 1081))
        self.assertTrue(allowed_origin("http://127.0.0.1:1081", 1081))
        self.assertTrue(allowed_origin("chrome-extension://abcdef", 1081))
        self.assertTrue(allowed_origin("moz-extension://1234-5678", 1081))
        self.assertFalse(allowed_origin("https://evil.example", 1081))
        self.assertFalse(allowed_origin("http://127.0.0.1:9999", 1081))
        # "null" senden sandboxed iframes und data:-Seiten: kein vertrauenswuerdiger Origin.
        self.assertFalse(allowed_origin("null", 1081))
        self.assertFalse(allowed_origin("chrome-extension://abc\nX-Injected: 1", 1081))
        self.assertFalse(allowed_origin("chrome-extension://abc\r\n", 1081))
        self.assertFalse(allowed_origin("moz-extension://abc/def", 1081))
        self.assertFalse(allowed_origin("chrome-extension://a b", 1081))
        self.assertFalse(allowed_origin("chrome-extension://", 1081))
        self.assertFalse(allowed_origin("xchrome-extension://abc", 1081))


class ApiTests(DaemonHarness):
    async def test_status_json(self):
        await self.start_daemon()
        status, hdrs, payload = await http(self.cfg.http_port, "GET", "/status.json")
        self.assertEqual(status, 200)
        self.assertEqual(hdrs["content-type"], "application/json")
        data = json.loads(payload)
        self.assertEqual(data["protocol"], 1)
        self.assertEqual(data["state"], "idle")
        self.assertNotIn("password", json.dumps(data))

    async def test_status_page(self):
        await self.start_daemon()
        status, hdrs, payload = await http(self.cfg.http_port, "GET", "/")
        self.assertEqual(status, 200)
        self.assertIn("text/html", hdrs["content-type"])
        self.assertIn(b"Uni VPN", payload)
        self.assertIn(b"/status.json", payload)

    async def test_not_found(self):
        await self.start_daemon()
        status, _, _ = await http(self.cfg.http_port, "GET", "/nope")
        self.assertEqual(status, 404)

    async def test_post_without_header_is_forbidden(self):
        d = await self.start_daemon()
        status, _, _ = await http(self.cfg.http_port, "POST", "/api/connect")
        self.assertEqual(status, 403)
        self.assertEqual(d.state, dm.State.idle)

    async def test_post_with_foreign_origin_is_forbidden(self):
        await self.start_daemon()
        status, _, _ = await http(self.cfg.http_port, "POST", "/api/connect",
                                  {"X-Uni-VPN": "1", "Origin": "https://evil.example"})
        self.assertEqual(status, 403)

    async def test_post_with_null_origin_is_forbidden(self):
        d = await self.start_daemon()
        status, hdrs, _ = await http(self.cfg.http_port, "POST", "/api/connect",
                                     {"X-Uni-VPN": "1", "Origin": "null"})
        self.assertEqual(status, 403)
        self.assertNotIn("access-control-allow-origin", hdrs)
        self.assertEqual(d.state, dm.State.idle)
        status, hdrs, _ = await http(self.cfg.http_port, "GET", "/status.json", {"Origin": "null"})
        self.assertEqual(status, 200)
        self.assertNotIn("access-control-allow-origin", hdrs)

    async def test_origin_with_special_characters_is_never_reflected(self):
        await self.start_daemon()
        for origin in ("chrome-extension://abc\nX-Injected: 1", "moz-extension://abc;evil", "chrome-extension://a b"):
            status, hdrs, _ = await http(self.cfg.http_port, "POST", "/api/connect",
                                         {"X-Uni-VPN": "1", "Origin": origin})
            self.assertEqual(status, 403, origin)
            self.assertNotIn("access-control-allow-origin", hdrs, origin)
            self.assertNotIn("x-injected", hdrs, origin)
            status, hdrs, _ = await http(self.cfg.http_port, "GET", "/status.json", {"Origin": origin})
            self.assertEqual(status, 200, origin)
            self.assertNotIn("access-control-allow-origin", hdrs, origin)
            self.assertNotIn("x-injected", hdrs, origin)

    async def test_foreign_host_header_is_forbidden(self):
        d = await self.start_daemon()
        for host in ("evil.example:1081", f"evil.example:{self.cfg.http_port}", "127.0.0.1:9", ""):
            status, _, _ = await http(self.cfg.http_port, "GET", "/status.json", {"Host": host})
            self.assertEqual(status, 403, host)
            status, _, _ = await http(self.cfg.http_port, "POST", "/api/connect", {"Host": host, "X-Uni-VPN": "1"})
            self.assertEqual(status, 403, host)
        self.assertEqual(d.state, dm.State.idle)
        for host in ("127.0.0.1", f"127.0.0.1:{self.cfg.http_port}", "localhost", f"localhost:{self.cfg.http_port}"):
            status, _, _ = await http(self.cfg.http_port, "GET", "/status.json", {"Host": host})
            self.assertEqual(status, 200, host)

    async def test_responses_deny_framing(self):
        await self.start_daemon()
        for method, path in (("GET", "/"), ("GET", "/status.json"), ("GET", "/nope")):
            _, hdrs, _ = await http(self.cfg.http_port, method, path)
            self.assertEqual(hdrs.get("x-frame-options"), "DENY", path)

    async def test_bad_content_length_is_rejected(self):
        d = await self.start_daemon()
        for value in ("abc", "1e5", "-5", "12abc", "0x10"):
            status, _, payload = await http(self.cfg.http_port, "POST", "/api/connect",
                                            {"X-Uni-VPN": "1", "Content-Length": value})
            self.assertEqual(status, 400, value)
            self.assertIn(b"Content-Length", payload)
        self.assertEqual(d.state, dm.State.idle)

    async def test_connect_and_disconnect(self):
        d = await self.start_daemon()
        status, _, payload = await http(self.cfg.http_port, "POST", "/api/connect", {"X-Uni-VPN": "1"})
        self.assertEqual(status, 200)
        self.assertTrue(json.loads(payload)["ok"])
        await wait_state(d, dm.State.connected)
        status, _, _ = await http(self.cfg.http_port, "POST", "/api/disconnect",
                                  {"X-Uni-VPN": "1", "Origin": "chrome-extension://abc"})
        self.assertEqual(status, 200)
        await wait_state(d, dm.State.idle)

    async def test_extension_origin_gets_cors_header(self):
        await self.start_daemon()
        status, hdrs, _ = await http(self.cfg.http_port, "GET", "/status.json", {"Origin": "moz-extension://x"})
        self.assertEqual(status, 200)
        self.assertEqual(hdrs["access-control-allow-origin"], "moz-extension://x")
        status, hdrs, _ = await http(self.cfg.http_port, "OPTIONS", "/api/connect",
                                     {"Origin": "chrome-extension://x", "Access-Control-Request-Method": "POST"})
        self.assertEqual(status, 204)
        self.assertIn("X-Uni-VPN", hdrs["access-control-allow-headers"])

    async def test_password_endpoint(self):
        d = await self.start_daemon()
        body = json.dumps({"password": "neu"}).encode()
        status, _, _ = await http(self.cfg.http_port, "POST", "/api/password",
                                  {"X-Uni-VPN": "1", "Content-Type": "application/json"}, body)
        self.assertEqual(status, 200)
        self.assertEqual(self.stored, ["neu"])
        await wait_state(d, dm.State.connected)

    async def test_password_endpoint_rejects_bad_json(self):
        await self.start_daemon()
        status, _, _ = await http(self.cfg.http_port, "POST", "/api/password",
                                  {"X-Uni-VPN": "1"}, b"{not json")
        self.assertEqual(status, 400)
        status, _, _ = await http(self.cfg.http_port, "POST", "/api/password",
                                  {"X-Uni-VPN": "1"}, b'{"password": ""}')
        self.assertEqual(status, 400)

    async def test_password_endpoint_rejects_newline(self):
        d = await self.start_daemon()
        for password in ("a\nb", "a\rb", "pw\n", "pw\r\ndelete-generic-password -s x"):
            body = json.dumps({"password": password}).encode()
            status, _, payload = await http(self.cfg.http_port, "POST", "/api/password",
                                            {"X-Uni-VPN": "1", "Content-Type": "application/json"}, body)
            self.assertEqual(status, 400, repr(password))
            self.assertEqual(payload.decode(), "Passwort darf keinen Zeilenumbruch enthalten")
        self.assertEqual(self.stored, [])
        self.assertEqual(d.state, dm.State.idle)


class TotpEndpointTests(DaemonHarness):
    HEADERS = {"X-Uni-VPN": "1", "Content-Type": "application/json"}

    async def test_status_page_has_totp_form(self):
        await self.start_daemon()
        _, _, payload = await http(self.cfg.http_port, "GET", "/")
        self.assertIn(b"/api/totp", payload)
        self.assertIn(b"mfa.uni-heidelberg.de", payload)

    async def test_totp_endpoint_normalizes_stores_and_answers_with_check_code(self):
        d = await self.start_daemon()
        body = json.dumps({"secret": "gezd gnbv gy3t qojq gezd gnbv gy3t qojq"}).encode()
        status, _, payload = await http(self.cfg.http_port, "POST", "/api/totp", self.HEADERS, body)
        self.assertEqual(status, 200, payload)
        data = json.loads(payload)
        self.assertTrue(data["ok"])
        self.assertEqual(self.stored_totp, ["base32:GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"])
        self.assertEqual(data["code"], totp.code("base32:GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"))
        await wait_state(d, dm.State.connected)

    async def test_totp_endpoint_accepts_otpauth_uri(self):
        await self.start_daemon()
        body = json.dumps({"secret": "otpauth://totp/Uni:ab1?secret=GEZDGNBVGY3TQOJQ&issuer=Uni"}).encode()
        status, _, _ = await http(self.cfg.http_port, "POST", "/api/totp", self.HEADERS, body)
        self.assertEqual(status, 200)
        self.assertEqual(self.stored_totp, ["base32:GEZDGNBVGY3TQOJQ"])

    async def test_totp_endpoint_rejects_bad_input(self):
        d = await self.start_daemon()
        for body in (b"{nope", b'{"secret": ""}', b'{"secret": "0189"}', b'{"secret": "otpauth://hotp/x?secret=GEZDGNBVGY3TQOJQ"}',
                     b'{"secret": 12}'):
            status, _, payload = await http(self.cfg.http_port, "POST", "/api/totp", self.HEADERS, body)
            self.assertEqual(status, 400, body)
            self.assertTrue(payload, body)
        self.assertEqual(self.stored_totp, [])
        self.assertEqual(d.state, dm.State.idle)

    async def test_totp_endpoint_needs_csrf_header(self):
        await self.start_daemon()
        status, _, _ = await http(self.cfg.http_port, "POST", "/api/totp", {"Content-Type": "application/json"},
                                  b'{"secret": "GEZDGNBVGY3TQOJQ"}')
        self.assertEqual(status, 403)
        self.assertEqual(self.stored_totp, [])

    async def test_status_never_leaks_secrets(self):
        d = await self.start_daemon()
        await d.request_connect()
        await wait_state(d, dm.State.connected)
        _, _, payload = await http(self.cfg.http_port, "GET", "/status.json")
        self.assertNotIn(b"GEZDGNBVGY3TQOJQ", payload)
        self.assertNotIn(b"geheim", payload)
