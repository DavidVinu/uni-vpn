import asyncio
import logging
import os
import socket
import tempfile
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
        self.assertIn("OTP", tn.classify_line("User input required in non-interactive mode")[1])
        self.assertIn("Passwort", tn.classify_line("Failed to complete authentication")[1])
        self.assertIn("HostScan", tn.classify_line("Error: Server asked us to run CSD hostscan.")[1])
        self.assertEqual(tn.classify_line("SAML authentication required")[0], "auth_failed")
        self.assertEqual(tn.classify_line("Server certificate verify failed")[0], "error")
        self.assertIsNone(tn.classify_line("Connected as 10.0.0.2"))


class TunnelTests(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        self.cfg = Config(user="u", host="vpn.example")
        self.log = logging.getLogger("test")
        self.pwfile = Path(tempfile.mkdtemp()) / "pw"
        self.env = mock.patch.dict(os.environ, {"FAKE_PASSWORD_FILE": str(self.pwfile), "FAKE_MODE": "ok", "FAKE_DELAY": "0.2"})
        self.env.start()

    def tearDown(self):
        self.env.stop()

    def make(self):
        return tn.Tunnel(self.cfg, FAKE, WRAPPER, self.log)

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
        self.assertIn("OTP", t.classification[1])

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
