import asyncio
import json
import time

from uni_vpn import daemon as dm
from uni_vpn import config, credentials, detect, pac, totp
from uni_vpn.httpapi import allowed_origin

from tests.test_daemon import DaemonHarness, wait_state
from tests.test_detect import fixture


async def http(port, method, path, headers=None, body=b""):
    reader, writer = await asyncio.open_connection("127.0.0.1", port)
    # The caller's own Host or Content-Length headers replace the defaults.
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
        self.assertTrue(allowed_origin("http://localhost:1081", 1081))
        self.assertFalse(allowed_origin("http://localhost:9999", 1081))
        # Without the extension there is no longer any foreign origin allowed to POST.
        self.assertFalse(allowed_origin("chrome-extension://abcdef", 1081))
        self.assertFalse(allowed_origin("moz-extension://1234-5678", 1081))
        self.assertFalse(allowed_origin("https://evil.example", 1081))
        self.assertFalse(allowed_origin("http://127.0.0.1:9999", 1081))
        # Sandboxed iframes and data: pages send "null": not a trustworthy origin.
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
                                  {"X-Uni-VPN": "1", "Origin": f"http://127.0.0.1:{self.cfg.http_port}"})
        self.assertEqual(status, 200)
        await wait_state(d, dm.State.idle)

    async def test_extension_origins_get_no_cors_header(self):
        await self.start_daemon()
        for origin in ("moz-extension://x", "chrome-extension://x"):
            status, hdrs, _ = await http(self.cfg.http_port, "GET", "/status.json", {"Origin": origin})
            self.assertEqual(status, 200)
            self.assertNotIn("access-control-allow-origin", hdrs)

    async def test_password_endpoint(self):
        d = await self.start_daemon()
        body = json.dumps({"password": "new"}).encode()
        status, _, _ = await http(self.cfg.http_port, "POST", "/api/password",
                                  {"X-Uni-VPN": "1", "Content-Type": "application/json"}, body)
        self.assertEqual(status, 200)
        self.assertEqual(self.stored, ["new"])
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
            self.assertEqual(payload.decode(), "Password must not contain a line break")
        self.assertEqual(self.stored, [])
        self.assertEqual(d.state, dm.State.idle)


class TotpEndpointTests(DaemonHarness):
    HEADERS = {"X-Uni-VPN": "1", "Content-Type": "application/json"}

    async def test_status_page_has_totp_form(self):
        await self.start_daemon()
        _, _, payload = await http(self.cfg.http_port, "GET", "/")
        self.assertIn(b"/api/totp", payload)
        # The portal link comes from the profile now, not from the page.
        self.assertNotIn(b"mfa.uni-heidelberg.de", payload)
        _, _, payload = await http(self.cfg.http_port, "GET", "/status.json")
        self.assertEqual(json.loads(payload)["mfa_portal_url"], "https://mfa.uni-heidelberg.de/")

    async def test_status_page_has_the_university_picker(self):
        await self.start_daemon()
        _, _, payload = await http(self.cfg.http_port, "GET", "/")
        for needle in (b'id="s-uni"', b'role="combobox"', b"/universities.json", b"/api/detect", b'id="s-mfa"',
                       b"{portal}", b"university: uni.id"):
            self.assertIn(needle, payload)
        self.assertNotIn("\u2014".encode(), payload, "no em-dashes")

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
        self.assertNotIn(b"pw-s3cret", payload)


class SetupTests(DaemonHarness):
    HEADERS = {"X-Uni-VPN": "1", "Content-Type": "application/json"}

    async def start_setup_daemon(self):
        import tempfile
        from pathlib import Path

        self.cfg_path = Path(tempfile.mkdtemp()) / "uni-vpn" / "config.toml"
        self.cfg.user = ""
        return await self.start_daemon(config_error="missing", config_path=self.cfg_path, needs_setup=True)

    async def test_status_reports_setup_needed_and_connect_is_refused(self):
        d = await self.start_setup_daemon()
        _, _, payload = await http(self.cfg.http_port, "GET", "/status.json")
        data = json.loads(payload)
        self.assertTrue(data["setup_needed"])
        self.assertEqual(data["state"], "idle")
        await d.request_connect()
        self.assertEqual(self.pw_lines(), [])

    async def test_setup_writes_config_stores_secrets_and_connects(self):
        d = await self.start_setup_daemon()
        body = json.dumps({"user": "ab123", "password": "pw", "secret": "GEZDGNBVGY3TQOJQ"}).encode()
        status, _, payload = await http(self.cfg.http_port, "POST", "/api/setup", self.HEADERS, body)
        self.assertEqual(status, 200, payload)
        self.assertEqual(json.loads(payload)["code"], totp.code("base32:GEZDGNBVGY3TQOJQ"))
        self.assertIn('user = "ab123"', self.cfg_path.read_text())
        self.assertEqual(self.stored, ["pw"])
        self.assertEqual(self.stored_totp, ["base32:GEZDGNBVGY3TQOJQ"])
        await wait_state(d, dm.State.connected)
        _, _, payload = await http(self.cfg.http_port, "GET", "/status.json")
        self.assertFalse(json.loads(payload)["setup_needed"])

    async def test_keyring_failure_writes_no_config_so_the_assistant_stays(self):
        def refuse(_secret):
            raise credentials.KeyringError("keyring locked")

        d = await self.start_setup_daemon()
        d.totp_setter = refuse
        body = json.dumps({"user": "ab123", "password": "pw", "secret": "GEZDGNBVGY3TQOJQ"}).encode()
        status, _, payload = await http(self.cfg.http_port, "POST", "/api/setup", self.HEADERS, body)
        self.assertEqual(status, 500)
        self.assertIn(b"keyring locked", payload)
        self.assertFalse(self.cfg_path.exists())
        self.assertTrue(d.needs_setup)
        self.assertEqual(d.cfg.user, "")

    async def test_check_code_marks_the_window_as_used(self):
        # The assistant asks to type the check code into the MFA portal, which uses it up.
        d = await self.start_setup_daemon()
        body = json.dumps({"secret": "GEZDGNBVGY3TQOJQ"}).encode()
        status, _, _ = await http(self.cfg.http_port, "POST", "/api/totp/check", self.HEADERS, body)
        self.assertEqual(status, 200)
        self.assertEqual(d.last_otp_step, int(time.time() // 30))

    async def test_setup_rejects_bad_input_without_writing(self):
        await self.start_setup_daemon()
        for body in ({"user": "ab 1\"", "password": "pw", "secret": "GEZDGNBVGY3TQOJQ"},
                     {"user": "ab1", "password": "", "secret": "GEZDGNBVGY3TQOJQ"},
                     {"user": "ab1", "password": "a\nb", "secret": "GEZDGNBVGY3TQOJQ"},
                     {"user": "ab1", "password": "pw", "secret": "0189"},
                     {"user": "ab1", "password": "pw"}):
            status, _, _ = await http(self.cfg.http_port, "POST", "/api/setup", self.HEADERS, json.dumps(body).encode())
            self.assertEqual(status, 400, body)
        self.assertFalse(self.cfg_path.exists())
        self.assertEqual(self.stored, [])

    async def test_setup_needs_csrf_header(self):
        await self.start_setup_daemon()
        body = json.dumps({"user": "ab1", "password": "pw", "secret": "GEZDGNBVGY3TQOJQ"}).encode()
        status, _, _ = await http(self.cfg.http_port, "POST", "/api/setup", {"Content-Type": "application/json"}, body)
        self.assertEqual(status, 403)

    async def test_totp_check_shows_code_without_storing(self):
        await self.start_daemon()
        body = json.dumps({"secret": "otpauth://totp/x?secret=GEZDGNBVGY3TQOJQ"}).encode()
        status, _, payload = await http(self.cfg.http_port, "POST", "/api/totp/check", self.HEADERS, body)
        self.assertEqual(status, 200, payload)
        data = json.loads(payload)
        self.assertEqual(len(data["code"]), 6)
        self.assertTrue(1 <= data["remaining"] <= 30)
        self.assertEqual(self.stored_totp, [])
        status, _, _ = await http(self.cfg.http_port, "POST", "/api/totp/check", self.HEADERS, b'{"secret": "nope!"}')
        self.assertEqual(status, 400)

    async def test_error_kind_names_the_factor(self):
        d = await self.start_daemon()
        d._set(dm.State.auth_failed, "One-time code rejected: check the clock")
        self.assertEqual(d.status()["error_kind"], "totp")
        d._set(dm.State.keyring, "No password stored: uni-vpn password")
        self.assertEqual(d.status()["error_kind"], "password")
        d._set(dm.State.idle, "Not connected")
        self.assertIsNone(d.status()["error_kind"])


class UniversitySetupTests(SetupTests):
    async def post_setup(self, body):
        status, _, payload = await http(self.cfg.http_port, "POST", "/api/setup", self.HEADERS, json.dumps(body).encode())
        return status, json.loads(payload)

    async def test_university_without_totp_needs_no_secret(self):
        self.totp = credentials.TotpMissing("x")
        d = await self.start_setup_daemon()
        status, data = await self.post_setup({"user": "ab123", "password": "pw", "university": "bonn"})
        self.assertEqual(status, 200, data)
        self.assertIsNone(data["code"])
        self.assertIn('university = "bonn"', self.cfg_path.read_text())
        self.assertEqual(self.stored, ["pw"])
        self.assertEqual(self.stored_totp, [])
        self.assertEqual((d.cfg.host, d.cfg.mfa, d.cfg.university_name), ("unibn-vpn.uni-bonn.de", "none", "University of Bonn"))
        await wait_state(d, dm.State.connected)

    async def test_unlisted_university_writes_its_profile(self):
        self.totp = credentials.TotpMissing("x")
        d = await self.start_setup_daemon()
        profile = {"host": "VPN.Example.edu", "usergroup": "staff", "authgroup": "Staff (Split)", "mfa": "none"}
        status, data = await self.post_setup({"user": "ab123", "password": "pw", "secret": "", "university": "other",
                                              "profile": profile})
        self.assertEqual(status, 200, data)
        cfg = config.load(self.cfg_path)
        self.assertEqual((cfg.university, cfg.host, cfg.usergroup, cfg.authgroup, cfg.mfa),
                         ("other", "vpn.example.edu", "staff", "Staff (Split)", "none"))
        self.assertEqual(d.cfg.host, "vpn.example.edu")
        await wait_state(d, dm.State.connected)

    async def test_errors_name_the_university_step(self):
        await self.start_setup_daemon()
        for body, field in (({"university": "fu-berlin"}, "university"),
                            ({"university": "nowhere"}, "university"),
                            ({"university": "other", "profile": {}}, "host"),
                            ({"university": "other", "profile": {"host": "vpn.example.edu", "mfa": "sms"}}, "mfa"),
                            ({"university": "heidelberg"}, "totp")):
            status, data = await self.post_setup({"user": "ab123", "password": "pw", **body})
            self.assertEqual((status, data["field"]), (400, field), body)
        self.assertIn("browser", (await self.post_setup({"user": "ab1", "password": "pw", "university": "fu-berlin"}))[1]["error"])
        self.assertFalse(self.cfg_path.exists())
        self.assertEqual(self.stored, [])

    async def test_user_with_a_realm_is_accepted(self):
        self.totp = credentials.TotpMissing("x")
        await self.start_setup_daemon()
        status, data = await self.post_setup({"user": "st1@stud.uni-stuttgart.de", "password": "pw", "university": "stuttgart"})
        self.assertEqual(status, 200, data)
        self.assertEqual(config.load(self.cfg_path).login_name, "st1@stud.uni-stuttgart.de")


class DetectTests(DaemonHarness):
    HEADERS = {"X-Uni-VPN": "1", "Content-Type": "application/json"}

    async def test_universities_json_lists_the_registry_without_notes(self):
        await self.start_daemon()
        status, _, payload = await http(self.cfg.http_port, "GET", "/universities.json")
        self.assertEqual(status, 200)
        data = json.loads(payload)
        self.assertEqual(data["default"], "heidelberg")
        heidelberg = next(u for u in data["universities"] if u["id"] == "heidelberg")
        self.assertEqual(heidelberg["mfa_portal_url"], "https://mfa.uni-heidelberg.de/")
        self.assertFalse([u for u in data["universities"] if "notes" in u])
        self.assertIn("fu-berlin", [u["id"] for u in data["universities"]])

    async def test_detect_runs_the_probe_and_returns_the_suggestion(self):
        d = await self.start_daemon()
        calls = []

        def fake(host, usergroup="", group=""):
            calls.append((host, usergroup, group))
            return detect.parse_reply(fixture("bremen.xml"), host, usergroup)

        d.http.detector = fake
        body = json.dumps({"host": "https://VPN.uni-bremen.de/", "group": "Tunnel-Uni-Bremen"}).encode()
        status, _, payload = await http(self.cfg.http_port, "POST", "/api/detect", self.HEADERS, body)
        self.assertEqual(status, 200, payload)
        data = json.loads(payload)
        self.assertEqual(calls, [("vpn.uni-bremen.de", "", "Tunnel-Uni-Bremen")])
        self.assertTrue(data["ok"])
        self.assertEqual(data["groups"], ["Tunnel-All-Traffic", "Tunnel-Uni-Bremen"])
        self.assertEqual(data["suggestion"]["mfa"], "totp_field")

    async def test_detect_refuses_bad_input_without_probing(self):
        d = await self.start_daemon()
        d.http.detector = lambda *a: self.fail("no probe for invalid input")
        for body, status_expected in ((b'{"host": "not a host"}', 400), (b'{"host": 5}', 400), (b"[]", 400),
                                      (b'{"host": "vpn.example.edu", "group": "a\nb"}', 400)):
            status, _, _ = await http(self.cfg.http_port, "POST", "/api/detect", self.HEADERS, body)
            self.assertEqual(status, status_expected, body)

    async def test_detect_needs_csrf_header(self):
        await self.start_daemon()
        status, _, _ = await http(self.cfg.http_port, "POST", "/api/detect", {"Content-Type": "application/json"},
                                  b'{"host": "vpn.example.edu"}')
        self.assertEqual(status, 403)


class PacTests(DaemonHarness):
    HEADERS = {"X-Uni-VPN": "1", "Content-Type": "application/json"}

    async def test_proxy_pac_serves_defaults_with_socks_port(self):
        await self.start_daemon()
        status, hdrs, payload = await http(self.cfg.http_port, "GET", "/proxy.pac")
        self.assertEqual(status, 200)
        self.assertEqual(hdrs["content-type"], "application/x-ns-proxy-autoconfig")
        self.assertEqual(hdrs["cache-control"], "no-store")
        text = payload.decode()
        self.assertIn("FindProxyForURL", text)
        for d in pac.DEFAULT_DOMAINS:
            self.assertIn(d, text)
        self.assertIn(f"SOCKS5 127.0.0.1:{self.cfg.socks_port}", text)

    async def test_status_json_lists_domains_and_pac_url(self):
        await self.start_daemon()
        _, _, payload = await http(self.cfg.http_port, "GET", "/status.json")
        data = json.loads(payload)
        self.assertEqual(data["domains"], pac.DEFAULT_DOMAINS)
        self.assertEqual(data["pac_url"], f"http://127.0.0.1:{self.cfg.http_port}/proxy.pac")

    async def test_status_page_has_domain_form(self):
        await self.start_daemon()
        _, _, payload = await http(self.cfg.http_port, "GET", "/")
        self.assertIn(b"/api/domains", payload)
        self.assertIn(b'id="domains"', payload)
        self.assertNotIn(b"Extension", payload)

    async def test_domains_endpoint_writes_file_refreshes_proxy_and_updates_pac(self):
        await self.start_daemon()
        body = json.dumps({"text": "Example.ORG\n# comment\nsogo.uni-heidelberg.de\n"}).encode()
        status, _, payload = await http(self.cfg.http_port, "POST", "/api/domains", self.HEADERS, body)
        self.assertEqual(status, 200, payload)
        self.assertEqual(json.loads(payload)["domains"], ["example.org", "sogo.uni-heidelberg.de"])
        self.assertEqual(pac.read_domains(self.domains_path), ["example.org", "sogo.uni-heidelberg.de"])
        self.assertEqual(self.refreshed, [self.cfg.http_port])
        _, _, payload = await http(self.cfg.http_port, "GET", "/proxy.pac")
        self.assertIn(b"example.org", payload)
        self.assertNotIn(b"elearning-med", payload)

    async def test_domains_endpoint_rejects_invalid_lines_and_changes_nothing(self):
        await self.start_daemon()
        body = json.dumps({"text": "sogo.uni-heidelberg.de\nbroken\n"}).encode()
        status, _, payload = await http(self.cfg.http_port, "POST", "/api/domains", self.HEADERS, body)
        self.assertEqual(status, 400)
        self.assertIn(b"line 2", payload)
        self.assertFalse(self.domains_path.exists())
        self.assertEqual(self.refreshed, [])

    async def test_domains_endpoint_needs_csrf_header(self):
        await self.start_daemon()
        status, _, _ = await http(self.cfg.http_port, "POST", "/api/domains", {"Content-Type": "application/json"},
                                  b'{"text": "example.org"}')
        self.assertEqual(status, 403)
