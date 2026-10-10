"""Black-box contract of the background service: what the app windows and the setup assistant
rely on, checked over HTTP against a running service process.

The same tests run against every core. UNI_VPN_CORE names the command that starts the service
in the foreground (default: the Python core, `bin/uni-vpn daemon`), for example
`UNI_VPN_CORE=/path/to/uni-vpn daemon`. The Python modules serve as the oracle for exact
values (university list, config.toml text, proxy rule, one-time codes).

Linux only: secrets go to a stand-in for secret-tool, openconnect is tests/fake_openconnect.py.
"""

from __future__ import annotations

import json
import os
import shlex
import shutil
import socket
import struct
import subprocess
import sys
import tempfile
import time
import unittest
import urllib.error
import urllib.request
from pathlib import Path

from uni_vpn import config as config_mod
from uni_vpn import i18n, messages, pac, totp
from uni_vpn import universities as unis

ROOT = Path(__file__).resolve().parents[2]
HERE = Path(__file__).resolve().parent
FAKE_OPENCONNECT = ROOT / "tests" / "fake_openconnect.py"
SECRET = "JBSWY3DPEHPK3PXP"
# Reachable with a valid certificate from CI; the service checks the network with a TLS handshake.
PROBE_HOST = os.environ.get("UNI_VPN_CONTRACT_HOST", "github.com")

# POST /api/repair is part of the contract too, but it starts the installer, so it is only
# covered by each core's own tests.
# Every key the app reads from /status.json. A core may add keys, never drop one.
STATUS_KEYS = {
    "protocol", "version", "state", "message", "since", "host", "user", "university", "university_name",
    "mfa", "mfa_portal_url", "mfa_steps", "socks_port", "http_port", "idle_minutes", "active_connections",
    "bytes_in", "bytes_out", "connects", "last_error", "domains", "pac_url", "pac_refresh", "log_tail",
    "setup_needed", "busy", "error_kind", "platform", "elevated", "message_id", "action", "language", "message_t",
}
STATES = {"idle", "offline", "blocked", "connecting", "connected", "disconnecting", "auth_failed", "keyring",
          "error"}


def message_id(text: str) -> str | None:
    """The catalog id of an English status text."""
    for value in vars(messages).values():
        if isinstance(value, messages.Message) and value == text:
            return value.id
    return None


def free_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def core_command() -> list[str]:
    custom = os.environ.get("UNI_VPN_CORE")
    if custom:
        return shlex.split(custom)
    return [sys.executable, str(ROOT / "bin" / "uni-vpn"), "daemon"]


@unittest.skipUnless(sys.platform.startswith("linux"), "contract tests run on Linux")
class ContractTest(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp(prefix="uni-vpn-contract-"))
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.config_home = self.tmp / "config"
        self.state_home = self.tmp / "state"
        self.keyring = self.tmp / "keyring"
        self.bin = self.tmp / "bin"
        for d in (self.config_home, self.state_home, self.keyring, self.bin):
            d.mkdir()
        shutil.copy(HERE / "secret-tool", self.bin / "secret-tool")
        # Never started: fake_openconnect listens itself. The core only has to find it.
        ocproxy = self.bin / "ocproxy"
        ocproxy.write_text("#!/bin/sh\nexit 1\n")
        ocproxy.chmod(0o755)
        self.config_dir = self.config_home / "uni-vpn"
        self.socks_port = free_port()
        self.http_port = free_port()
        self.process = None

    def write_config(self, extra: str = "", user: str = "ab123") -> None:
        self.config_dir.mkdir(parents=True, exist_ok=True)
        (self.config_dir / "config.toml").write_text(
            f'university = "heidelberg"\nuser = "{user}"\nhost = "{PROBE_HOST}"\n'
            f'socks_port = {self.socks_port}\nhttp_port = {self.http_port}\n'
            f'openconnect = "{FAKE_OPENCONNECT}"\n{extra}', encoding="utf-8")

    def store(self, kind: str, user: str, value: str) -> None:
        service = {"password": "uni-vpn", "totp": "uni-vpn-totp"}[kind]
        subprocess.run([str(self.bin / "secret-tool"), "store", "--label", "x", "service", service, "user", user],
                       input=value.encode(), check=True, env=self.env())

    def secrets(self) -> dict[tuple[str, str], dict]:
        found = {}
        for path in self.keyring.iterdir():
            data = json.loads(path.read_text())
            attrs = dict(zip(data["attrs"][::2], data["attrs"][1::2]))
            found[(attrs.get("service"), attrs.get("user"))] = data
        return found

    def env(self) -> dict[str, str]:
        env = dict(os.environ)
        env.update({
            "XDG_CONFIG_HOME": str(self.config_home), "XDG_STATE_HOME": str(self.state_home),
            "XDG_DATA_HOME": str(self.tmp / "data"), "FAKE_KEYRING_DIR": str(self.keyring),
            "PATH": f"{self.bin}{os.pathsep}{env.get('PATH', '')}",
        })
        env.setdefault("FAKE_MODE", "ok")
        env.setdefault("FAKE_DELAY", "0.2")
        # No desktop: registering the proxy rule with GNOME or KDE must not touch the real session.
        for name in ("DISPLAY", "WAYLAND_DISPLAY", "DBUS_SESSION_BUS_ADDRESS", "XDG_CURRENT_DESKTOP"):
            env.pop(name, None)
        return env

    def start(self, port: int | None = None) -> None:
        self.port = port or self.http_port
        self.log = open(self.tmp / "core.log", "wb")
        self.addCleanup(self.log.close)
        self.process = subprocess.Popen(core_command(), env=self.env(), stdout=self.log, stderr=subprocess.STDOUT,
                                        stdin=subprocess.DEVNULL)
        self.addCleanup(self.stop)
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            try:
                self.status()
                return
            except OSError:
                if self.process.poll() is not None:
                    break
                time.sleep(0.1)
        self.fail(f"service did not answer: {(self.tmp / 'core.log').read_text(errors='replace')}")

    def stop(self) -> None:
        if self.process and self.process.poll() is None:
            self.process.terminate()
            try:
                self.process.wait(10)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait()

    def request(self, method: str, path: str, body=None, headers: dict | None = None):
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(f"http://127.0.0.1:{self.port}{path}", data=data, method=method)
        for key, value in (headers or {}).items():
            req.add_header(key, value)
        try:
            with urllib.request.urlopen(req, timeout=10) as resp:
                return resp.status, resp.headers, resp.read()
        except urllib.error.HTTPError as exc:
            return exc.code, exc.headers, exc.read()

    def post(self, path: str, body=None, headers: dict | None = None):
        base = {"X-Uni-VPN": "1", "Content-Type": "application/json"}
        base.update(headers or {})
        status, _headers, payload = self.request("POST", path, body if body is not None else {}, base)
        try:
            return status, json.loads(payload)
        except ValueError:
            return status, payload.decode(errors="replace")

    def status(self) -> dict:
        with urllib.request.urlopen(f"http://127.0.0.1:{self.port}/status.json", timeout=2) as resp:
            return json.loads(resp.read())

    def wait_state(self, states: set[str], timeout: float = 15) -> dict:
        deadline = time.monotonic() + timeout
        status = self.status()
        while status["state"] not in states and time.monotonic() < deadline:
            time.sleep(0.1)
            status = self.status()
        return status

    # --- First run -------------------------------------------------------

    def test_first_run_shows_the_setup_assistant(self):
        self.start(port=1081)
        status = self.status()
        self.assertLessEqual(STATUS_KEYS, set(status))
        self.assertTrue(status["setup_needed"])
        self.assertEqual(status["state"], "idle")
        self.assertEqual(status["message"], "Setup needed")
        self.assertEqual(status["message_id"], message_id("Setup needed"))
        self.assertEqual(status["message_t"], {"key": "msg.setup_needed", "args": {}})
        self.assertEqual(status["platform"], "linux")
        self.assertIsNone(status["elevated"])
        code, headers, page = self.request("GET", "/")
        self.assertEqual(code, 200)
        self.assertIn("text/html", headers["Content-Type"])
        self.assertEqual(page, (ROOT / "uni_vpn" / "ui" / "index.html").read_bytes())

    def test_setup_names_the_step_to_change(self):
        self.start(port=1081)
        cases = [
            ({"user": "", "password": "pw"}, "user", "Check your username"),
            ({"user": "ab123", "password": ""}, "password", "Enter your password"),
            ({"user": "ab123", "password": "pw"}, "totp", "Paste the setup key first"),
            ({"user": "ab123", "password": "pw", "university": "nowhere"}, "university", None),
            ({"user": "ab123", "password": "pw", "secret": "not base32 !"}, "totp", None),
            ({"user": "ab123", "password": "pw", "university": "fu-berlin"}, "university", None),
        ]
        for body, field, message in cases:
            with self.subTest(body=body):
                code, payload = self.post("/api/setup", body)
                self.assertEqual(code, 400)
                self.assertFalse(payload["ok"])
                self.assertEqual(payload["field"], field)
                if message:
                    self.assertEqual(payload["error"], message)
                    self.assertEqual(i18n.t(payload["error_t"]["key"], **payload["error_t"]["args"]), message)
        self.assertFalse((self.config_dir / "config.toml").exists())
        self.assertEqual(self.secrets(), {})

    def test_setup_writes_config_and_secrets(self):
        self.start(port=1081)
        code, payload = self.post("/api/setup", {"user": " ab123 ", "password": "pa ss", "secret": SECRET})
        self.assertEqual(code, 200, payload)
        self.assertTrue(payload["ok"])
        self.assertEqual(payload["code"], totp.code(totp.normalize(SECRET)))
        expected = self.tmp / "expected.toml"
        config_mod.write_initial(expected, "ab123", "heidelberg", {})
        self.assertEqual((self.config_dir / "config.toml").read_text(), expected.read_text())
        self.assertEqual(oct((self.config_dir / "config.toml").stat().st_mode & 0o777), "0o600")
        secrets = self.secrets()
        self.assertEqual(secrets[("uni-vpn", "ab123")]["secret"], "pa ss")
        self.assertEqual(secrets[("uni-vpn-totp", "ab123")]["secret"], totp.normalize(SECRET))
        self.assertEqual(secrets[("uni-vpn-totp", "ab123")]["label"], "Uni VPN (second factor)")
        status = self.status()
        self.assertFalse(status["setup_needed"])
        self.assertEqual(status["user"], "ab123")

    # --- Requests from other pages ---------------------------------------

    def test_foreign_pages_are_refused(self):
        self.write_config()
        self.start()
        self.assertEqual(self.request("POST", "/api/connect", {}, {"Content-Type": "application/json"})[0], 403)
        self.assertEqual(self.post("/api/connect", headers={"Origin": "https://evil.example"})[0], 403)
        self.assertEqual(self.post("/api/connect", headers={"Origin": "null"})[0], 403)
        self.assertEqual(self.request("GET", "/status.json", headers={"Host": "evil.example"})[0], 403)
        code, headers, _ = self.request("GET", "/status.json")
        self.assertEqual(code, 200)
        self.assertEqual(headers["X-Frame-Options"], "DENY")
        self.assertEqual(headers["Cache-Control"], "no-store")
        self.assertEqual(self.request("GET", "/nothing")[0], 404)
        self.assertEqual(self.request("PUT", "/api/connect", {})[0], 405)

    # --- Data the app shows ----------------------------------------------

    def test_university_list(self):
        self.write_config()
        self.start()
        code, _headers, payload = self.request("GET", "/universities.json")
        self.assertEqual(code, 200)
        self.assertEqual(json.loads(payload), {"default": unis.DEFAULT_ID, "universities": unis.public_list()})

    def test_websites_and_proxy_rule(self):
        self.write_config()
        self.start()
        status = self.status()
        self.assertEqual(status["domains"], list(unis.get("heidelberg").default_domains))
        code, headers, payload = self.request("GET", "/proxy.pac")
        self.assertEqual(code, 200)
        self.assertIn("application/x-ns-proxy-autoconfig", headers["Content-Type"])
        self.assertEqual(payload.decode(), pac.build_pac(status["domains"], self.socks_port))

        code, payload = self.post("/api/domains", {"text": "Example.org\n*.uni-heidelberg.de # all\n\nexample.org"})
        self.assertEqual(code, 200)
        self.assertEqual(payload["domains"], ["example.org", "uni-heidelberg.de"])
        self.assertEqual(self.status()["domains"], ["example.org", "uni-heidelberg.de"])
        written = (self.config_dir / "domains.txt").read_text()
        self.assertEqual(written, pac.HEADER + "example.org\nuni-heidelberg.de\n")
        code, payload = self.post("/api/domains", {"text": "not a host!"})
        self.assertEqual(code, 400)
        self.assertEqual(payload["error"], 'Line 1: "not a host!" is not a website')
        self.assertEqual(payload["error_t"], {"key": "app.lines", "args": {"lines": [
            {"key": "domains.not_hostname", "args": {"line": 1, "text": "not a host!"}}]}})
        self.assertEqual(self.post("/api/domains", {"text": 5})[0], 400)

    # --- Languages ---------------------------------------------------------

    def test_translations_and_the_language_setting(self):
        self.write_config()
        self.start()
        code, _headers, payload = self.request("GET", "/locales.json")
        self.assertEqual(code, 200)
        self.assertEqual(json.loads(payload), i18n.catalogs())
        self.assertEqual(self.status()["language"], "")
        self.assertNotIn("menu", self.status())
        menu = lambda accepted: json.loads(self.request("GET", f"/status.json?menu={accepted}")[2])["menu"]
        self.assertEqual(menu("fr-CA%2Cen"), i18n.menu("fr"))

        self.assertEqual(self.post("/api/language", {"language": "de"}), (200, {"ok": True, "language": "de"}))
        self.assertEqual(self.status()["language"], "de")
        self.assertEqual(config_mod.load(self.config_dir / "config.toml").language, "de")
        self.assertEqual(menu("fr"), i18n.menu("de"))
        self.assertEqual(self.post("/api/language", {"language": "xx"})[0], 400)
        self.assertEqual(self.post("/api/language", {"language": 1})[0], 400)
        self.assertEqual(self.post("/api/language", {"language": ""})[0], 200)
        self.assertEqual(config_mod.load(self.config_dir / "config.toml").language, "")

    def test_language_waits_for_setup(self):
        self.start(port=1081)
        code, payload = self.post("/api/language", {"language": "de"})
        self.assertEqual(code, 409)
        self.assertEqual(payload["error_t"], {"key": "setup.finish_first", "args": {}})

    def test_check_code_without_saving(self):
        self.write_config()
        self.start()
        code, payload = self.post("/api/totp/check", {"secret": SECRET})
        self.assertEqual(code, 200)
        self.assertEqual(payload["code"], totp.code(totp.normalize(SECRET)))
        self.assertTrue(1 <= payload["remaining"] <= 30)
        self.assertEqual(self.secrets(), {})
        code, payload = self.post("/api/totp/check", {"secret": "!!"})
        self.assertEqual(code, 400)
        self.assertEqual(payload["error_t"], {"key": "totp.not_secret", "args": {}})

    def test_password_and_secret_changes(self):
        self.write_config()
        self.store("password", "ab123", "old")
        self.store("totp", "ab123", totp.normalize(SECRET))
        self.start()
        self.assertEqual(self.post("/api/password", {"password": "new"})[0], 200)
        self.assertEqual(self.secrets()[("uni-vpn", "ab123")]["secret"], "new")
        self.assertEqual(self.post("/api/password", {"password": "a\nb"})[0], 400)
        self.assertEqual(self.post("/api/password", {"password": ""})[0], 400)
        token = totp.normalize("GEZDGNBVGY3TQOJQ")
        code, payload = self.post("/api/totp", {"secret": "otpauth://totp/x?secret=GEZDGNBVGY3TQOJQ&issuer=y"})
        self.assertEqual(code, 200, payload)
        self.assertEqual(self.secrets()[("uni-vpn-totp", "ab123")]["secret"], token)
        self.assertEqual(payload["code"], totp.code(token))

    # --- Connecting ------------------------------------------------------

    def test_connect_on_demand_through_the_proxy(self):
        self.write_config()
        self.store("password", "ab123", "secret pw")
        self.store("totp", "ab123", totp.normalize(SECRET))
        self.start()
        self.assertEqual(self.status()["state"], "idle")
        with socket.create_connection(("127.0.0.1", self.socks_port), timeout=20) as sock:
            # fake_openconnect echoes what reaches the tunnel; the service passes the bytes through.
            greeting = struct.pack("BBB", 5, 1, 0)
            sock.sendall(greeting)
            echoed = sock.recv(16)
            status = self.status()
            if status["state"] == "offline":
                self.skipTest(f"no direct connection to {PROBE_HOST}")
            self.assertEqual(echoed, greeting)
            self.assertEqual(status["state"], "connected")
            self.assertEqual(status["active_connections"], 1)
            self.assertEqual(status["connects"], 1)
        code, payload = self.post("/api/disconnect")
        self.assertEqual(code, 200)
        self.assertIn(self.wait_state({"idle"})["state"], {"idle"})

    def test_wrong_password_is_reported(self):
        self.write_config()
        self.store("password", "ab123", "wrong")
        self.store("totp", "ab123", totp.normalize(SECRET))
        os.environ["FAKE_MODE"] = "auth_fail"
        self.addCleanup(os.environ.pop, "FAKE_MODE", None)
        self.start()
        self.assertEqual(self.post("/api/connect")[0], 200)
        status = self.wait_state({"auth_failed", "offline"})
        if status["state"] == "offline":
            self.skipTest(f"no direct connection to {PROBE_HOST}")
        self.assertEqual(status["state"], "auth_failed")
        self.assertEqual(status["error_kind"], "password")
        self.assertEqual(status["action"], "password")
        self.assertEqual(status["message_id"], message_id(status["message"]))
        self.assertIsNotNone(status["message_id"])
        self.assertIn(status["state"], STATES)


if __name__ == "__main__":
    unittest.main()
