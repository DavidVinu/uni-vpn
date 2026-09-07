import asyncio
import json

from uni_vpn import daemon as dm
from uni_vpn.httpapi import allowed_origin

from tests.test_daemon import DaemonHarness, wait_state


async def http(port, method, path, headers=None, body=b""):
    reader, writer = await asyncio.open_connection("127.0.0.1", port)
    lines = [f"{method} {path} HTTP/1.1", "Host: 127.0.0.1", f"Content-Length: {len(body)}"]
    for key, value in (headers or {}).items():
        lines.append(f"{key}: {value}")
    writer.write(("\r\n".join(lines) + "\r\n\r\n").encode() + body)
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
