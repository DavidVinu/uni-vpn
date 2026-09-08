import asyncio
import logging
import os
import socket
import stat
import subprocess
import sys
import tempfile
import time
import unittest
from pathlib import Path
from unittest import mock

from uni_vpn import tunnel as tn
from uni_vpn.config import Config

FAKE = str(Path(__file__).parent / "fake_openconnect.py")
WRAPPER = "/nonexistent/uni-vpn-ocproxy"


class ClassifyTests(unittest.TestCase):
    def test_markers(self):
        self.assertEqual(tn.classify_line("User input required in non-interactive mode")[0], "auth_failed")
        self.assertIn("uni-vpn log", tn.classify_line("User input required in non-interactive mode")[1])
        self.assertIn("Passwort", tn.classify_line("Failed to complete authentication")[1])
        self.assertIn("HostScan", tn.classify_line("Error: Server asked us to run CSD hostscan.")[1])
        self.assertEqual(tn.classify_line("SAML authentication required")[0], "auth_failed")
        self.assertEqual(tn.classify_line("Server certificate verify failed")[0], "error")
        self.assertIsNone(tn.classify_line("Connected as 10.0.0.2"))

    def test_wrong_password_and_input_required_share_message(self):
        # Feststellung 6: ein falsches Passwort erzeugt bei --non-inter zuerst
        # "User input required", dann "Failed to complete authentication".
        # Beide Marker muessen dieselbe ehrliche Meldung liefern.
        input_required = tn.classify_line("User input required in non-interactive mode")
        auth_failed = tn.classify_line("Failed to complete authentication")
        self.assertEqual(input_required[0], "auth_failed")
        self.assertEqual(auth_failed[0], "auth_failed")
        self.assertEqual(input_required[1], auth_failed[1])
        self.assertIn("Passwort", input_required[1])
        self.assertIn("uni-vpn password", input_required[1])
        self.assertIn("uni-vpn log", input_required[1])

    def test_invalid_soft_token_string_names_the_command(self):
        # Echtes openconnect 9.12 bei kaputter Schluesseldatei: "Invalid base32 token string",
        # dann "Soft token string is invalid", Exit 1 vor jedem Netzkontakt.
        state, message = tn.classify_line("Soft token string is invalid")
        self.assertEqual(state, "auth_failed")
        self.assertIn("uni-vpn totp", message)

    def test_rejected_soft_token_names_the_second_factor(self):
        # openconnect probiert zwei Codes, dann "switching to manual entry"; danach folgen
        # "User input required" und "Failed to complete authentication". Der erste Treffer zaehlt.
        state, message = tn.classify_line("Server is rejecting the soft token; switching to manual entry")
        self.assertEqual(state, "auth_failed")
        self.assertIn("Einmalcode", message)
        self.assertIn("uni-vpn totp", message)
        self.assertIn("Uhrzeit", message)


class TunnelTests(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        self.cfg = Config(user="u", host="vpn.example")
        self.log = logging.getLogger("test")
        tmp = Path(tempfile.mkdtemp())
        self.pwfile = tmp / "pw"
        self.tokenfile = tmp / "token"
        self.token_dir = tmp / "state"
        self.token_dir.mkdir()
        self.env = mock.patch.dict(os.environ, {"FAKE_PASSWORD_FILE": str(self.pwfile), "FAKE_TOKEN_FILE": str(self.tokenfile),
                                                "FAKE_MODE": "ok", "FAKE_DELAY": "0.2"})
        self.env.start()

    def tearDown(self):
        self.env.stop()

    def make(self):
        return tn.Tunnel(self.cfg, FAKE, WRAPPER, self.log, token_dir=self.token_dir)

    def token_files(self):
        return sorted(p.name for p in self.token_dir.iterdir())

    def test_command(self):
        cmd = self.make().command(4321)
        self.assertEqual(cmd[0], FAKE)
        self.assertIn("--protocol=anyconnect", cmd)
        self.assertIn("--user=u", cmd)
        self.assertIn("--passwd-on-stdin", cmd)
        self.assertIn("--non-inter", cmd)
        self.assertIn("--no-dtls", cmd)
        self.assertIn(f"--script={WRAPPER} 4321", cmd)
        self.assertEqual(cmd[-1], "vpn.example")
        self.assertNotIn("--dump-http-traffic", cmd)
        self.assertFalse([a for a in cmd if a.startswith("--token")])

    def test_command_with_token_file_enables_totp(self):
        t = self.make()
        t.token_file = self.token_dir / "totp-1"
        cmd = t.command(4321)
        self.assertIn("--token-mode=totp", cmd)
        self.assertIn(f"--token-secret=@{self.token_dir / 'totp-1'}", cmd)
        # Der Schluessel selbst steht nie in der Kommandozeile.
        self.assertFalse([a for a in cmd if "base32" in a])

    async def test_totp_secret_reaches_openconnect_by_file_and_is_removed_when_ready(self):
        t = self.make()
        await t.start(b"geheim", totp="base32:GEZDGNBVGY3TQOJQ")
        self.assertEqual(len(self.token_files()), 1)
        self.assertTrue(self.token_files()[0].startswith("totp-"))
        self.assertEqual(stat.S_IMODE((self.token_dir / self.token_files()[0]).stat().st_mode), 0o600)
        self.assertTrue(await t.wait_ready(3))
        self.assertEqual(self.tokenfile.read_text(), "base32:GEZDGNBVGY3TQOJQ\n")
        self.assertEqual(self.token_files(), [], "Schluesseldatei muss nach dem Aufbau weg sein")
        await t.stop(2)

    async def test_token_file_removed_when_openconnect_exits_early(self):
        os.environ["FAKE_MODE"] = "auth_fail"
        t = self.make()
        await t.start(b"x", totp="base32:GEZDGNBVGY3TQOJQ")
        self.assertFalse(await t.wait_ready(3))
        await asyncio.sleep(0.1)
        self.assertEqual(self.token_files(), [])

    async def test_token_file_removed_on_stop_before_ready(self):
        os.environ["FAKE_MODE"] = "never_ready"
        t = self.make()
        await t.start(b"x", totp="base32:GEZDGNBVGY3TQOJQ")
        self.assertEqual(len(self.token_files()), 1)
        await t.stop(1)
        self.assertEqual(self.token_files(), [])

    async def test_without_totp_no_token_file(self):
        t = self.make()
        await t.start(b"geheim")
        self.assertTrue(await t.wait_ready(3))
        self.assertEqual(self.token_files(), [])
        self.assertFalse(self.tokenfile.exists())
        await t.stop(2)

    async def test_rejected_soft_token_is_classified(self):
        os.environ["FAKE_MODE"] = "totp_rejected"
        t = self.make()
        await t.start(b"x", totp="base32:GEZDGNBVGY3TQOJQ")
        self.assertFalse(await t.wait_ready(3))
        self.assertEqual(t.returncode, 1)
        self.assertEqual(t.classification[0], "auth_failed")
        self.assertIn("Einmalcode", t.classification[1])

    def test_remove_stale_token_files(self):
        (self.token_dir / "totp-123").write_text("alt")
        (self.token_dir / "totp-456").write_text("alt")
        (self.token_dir / "daemon.log").write_text("bleibt")
        removed = tn.remove_stale_token_files(self.token_dir)
        self.assertEqual(removed, 2)
        self.assertEqual(self.token_files(), ["daemon.log"])
        self.assertEqual(tn.remove_stale_token_files(self.token_dir / "gibt-es-nicht"), 0)

    async def test_start_ready_stop(self):
        t = self.make()
        await t.start(b"geheim")
        self.assertTrue(await t.wait_ready(3))
        self.assertTrue(tn.port_open(t.port))
        self.assertEqual(self.pwfile.read_text(), "geheim\n")
        await t.stop(2)
        self.assertTrue(t.exited.is_set())
        self.assertEqual(t.returncode, 0)
        self.assertTrue(t.stopped_by_us)
        self.assertFalse(tn.port_open(t.port))

    async def test_auth_fail(self):
        os.environ["FAKE_MODE"] = "auth_fail"
        t = self.make()
        await t.start(b"x")
        self.assertFalse(await t.wait_ready(3))
        self.assertTrue(t.exited.is_set())
        self.assertEqual(t.returncode, 1)
        self.assertEqual(t.classification[0], "auth_failed")
        self.assertTrue(any("Failed to complete" in line for line in t.stderr_tail))

    async def test_input_required_message(self):
        os.environ["FAKE_MODE"] = "input_required"
        t = self.make()
        await t.start(b"x")
        self.assertFalse(await t.wait_ready(3))
        self.assertEqual(t.classification[0], "auth_failed")
        self.assertIn("uni-vpn log", t.classification[1])
        # Feststellung 6: dieselbe Zeilenfolge entsteht bei falschem Passwort.
        self.assertIn("Passwort", t.classification[1])

    def test_script_value_survives_sh_with_spaces_and_quote(self):
        # Feststellung 5: openconnect fuehrt --script per /bin/sh -c aus.
        base = Path(tempfile.mkdtemp()) / "mit leerzeichen"
        base.mkdir()
        wrapper = base / "o'proxy wrapper"
        argfile = base / "args"
        wrapper.write_text(f"#!/bin/sh\nprintf '%s\\n' \"$@\" > '{argfile}'\n")
        wrapper.chmod(0o755)
        t = tn.Tunnel(self.cfg, FAKE, str(wrapper), self.log)
        cmd = t.command(4321)
        script = [a for a in cmd if a.startswith("--script=")][0][len("--script="):]
        result = subprocess.run(["/bin/sh", "-c", script], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(argfile.read_text(), "4321\n")

    def test_kill_wrapper_argv(self):
        # Feststellung 4: Muster ohne fuehrenden Bindestrich, "--" vor dem Muster,
        # nur eigene Prozesse, Rueckgabewert im Log.
        t = self.make()
        t.port = 4321
        with mock.patch.object(tn.subprocess, "run") as run:
            run.return_value = subprocess.CompletedProcess(args=[], returncode=1)
            with self.assertLogs(self.log, "WARNING") as logs:
                t._kill_wrapper()
        run.assert_called_once_with(
            ["pkill", "-9", "-U", str(os.getuid()), "-f", "--", "ocproxy -D 127.0.0.1:4321 "],
            check=False,
        )
        self.assertTrue(any("1" in line and "pkill" in line for line in logs.output), logs.output)

    def test_kill_wrapper_kills_port_holder(self):
        # Feststellung 4: ein Prozess, dessen Kommandozeile wie der Wrapper-exec aussieht
        # und der den Port haelt, muss nach _kill_wrapper() verschwunden sein.
        port = tn.free_port()
        code = (
            "import socket, sys, time\n"
            "s = socket.socket()\n"
            "s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)\n"
            "s.bind(('127.0.0.1', int(sys.argv[3].split(':')[1])))\n"
            "s.listen(16)\n"
            "while True:\n"
            "    c, _ = s.accept()\n"
            "    c.close()\n"
        )
        dummy = subprocess.Popen(
            [sys.executable, "-c", code, "ocproxy", "-D", f"127.0.0.1:{port}", "-k", "30"],
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
        )
        try:
            deadline = time.monotonic() + 3
            while not tn.port_open(port) and time.monotonic() < deadline:
                time.sleep(0.05)
            self.assertTrue(tn.port_open(port), "Dummy haelt den Port nicht")
            t = self.make()
            t.port = port
            t._kill_wrapper()
            deadline = time.monotonic() + 2
            while tn.port_open(port) and time.monotonic() < deadline:
                time.sleep(0.05)
            self.assertFalse(tn.port_open(port), "Port nach _kill_wrapper() noch offen")
            self.assertEqual(dummy.wait(timeout=2), -9)
        finally:
            if dummy.poll() is None:
                dummy.kill()
                dummy.wait()

    async def test_ignore_sigterm_gets_killed(self):
        os.environ["FAKE_MODE"] = "ignore_sigterm"
        t = self.make()
        await t.start(b"x")
        self.assertTrue(await t.wait_ready(3))
        await t.stop(0.5)
        self.assertTrue(t.exited.is_set())
        self.assertEqual(t.returncode, -9)

    async def test_never_ready_timeout(self):
        os.environ["FAKE_MODE"] = "never_ready"
        t = self.make()
        await t.start(b"x")
        self.assertFalse(await t.wait_ready(0.6))
        self.assertFalse(t.exited.is_set())
        await t.stop(1)
        self.assertTrue(t.exited.is_set())

    async def test_missing_binary(self):
        t = tn.Tunnel(self.cfg, "/nonexistent/openconnect", WRAPPER, self.log)
        with self.assertRaises(OSError):
            await t.start(b"x")

    async def test_reconnect_sends_sigusr2(self):
        t = self.make()
        await t.start(b"x")
        self.assertTrue(await t.wait_ready(3))
        t.reconnect()
        await asyncio.sleep(0.3)
        self.assertTrue(any("SIGUSR2" in line for line in t.stderr_tail))
        await t.stop(2)


class PortTests(unittest.TestCase):
    def test_free_port_is_closed(self):
        port = tn.free_port()
        self.assertFalse(tn.port_open(port))
        s = socket.socket()
        s.bind(("127.0.0.1", port))
        s.listen(1)
        self.assertTrue(tn.port_open(port))
        s.close()
