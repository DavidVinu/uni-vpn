import asyncio
import logging
import os
import signal
import sys
import tempfile
import unittest
from pathlib import Path
from xml.dom import minidom
from unittest import mock

from uni_vpn import windows, wintunnel
from uni_vpn.config import Config

FAKE = str(Path(__file__).parent / "fake_openconnect.py")


class ParseTests(unittest.TestCase):
    def test_state_file(self):
        values = wintunnel.parse_state("INTERNAL_IP4_ADDRESS=10.1.2.3\nINTERNAL_IP4_DNS=10.0.0.1 10.0.0.2\njunk\nTUNIDX=\n")
        self.assertEqual(values["INTERNAL_IP4_ADDRESS"], "10.1.2.3")
        self.assertEqual(wintunnel.dns_servers(values), ["10.0.0.1", "10.0.0.2"])
        self.assertEqual(values["TUNIDX"], "")
        self.assertEqual(wintunnel.dns_servers({}), [])
        # openconnect lists IPv6 servers in the same variable; the SOCKS source address is IPv4.
        self.assertEqual(wintunnel.dns_servers({"INTERNAL_IP4_DNS": "fd00::53 10.0.0.1 junk"}), ["10.0.0.1"])

    def test_command_uses_interface_and_script_not_script_tun(self):
        tunnel = wintunnel.WindowsTunnel(Config(user="ab1", host="vpn.example"), "C:\\oc\\openconnect.exe",
                                         "C:\\Program Files\\uni vpn\\bin\\uni-vpn-vpnc.js", logging.getLogger("t"))
        cmd = tunnel.command(4000)
        self.assertIn("--interface=uni-vpn", cmd)
        self.assertIn("--script=C:\\Program Files\\uni vpn\\bin\\uni-vpn-vpnc.js", cmd)
        self.assertNotIn("--script-tun", cmd)
        self.assertIn("--disable-ipv6", cmd)
        self.assertEqual(cmd[-1], "vpn.example")

    def test_password_is_passed_in_the_ansi_code_page(self):
        tunnel = wintunnel.WindowsTunnel(Config(user="ab1", host="vpn.example"), "openconnect.exe", "x.js",
                                         logging.getLogger("t"))
        with mock.patch.object(wintunnel.locale, "getencoding", return_value="cp1252"):
            self.assertEqual(tunnel._password_bytes("pä€".encode()), "pä€".encode("cp1252"))
            with self.assertRaises(wintunnel.PasswordEncodingError):
                tunnel._password_bytes("p\u4e2d".encode())

    def test_child_environment_keeps_no_user_controlled_paths(self):
        current = {"ComSpec": "C:\\Users\\a\\evil.exe", "Path": "C:\\Users\\a\\bin", "TEMP": "C:\\T",
                   "SystemRoot": "C:\\Users\\a\\fake", "P11_KIT_SERVER_ADDRESS": "x", "OPENSSL_CONF": "y",
                   "UNI_VPN_STATE": "dropped by trusted_env, set again by _env"}
        env = windows.trusted_env(current, "C:\\Windows\\system32", "C:\\Windows")
        self.assertEqual(env["ComSpec"], os.path.join("C:\\Windows\\system32", "cmd.exe"))
        self.assertEqual(env["SystemRoot"], "C:\\Windows")
        self.assertTrue(env["PATH"].startswith("C:\\Windows\\system32;"))
        self.assertEqual(env["TEMP"], "C:\\T")
        for name in ("Path", "P11_KIT_SERVER_ADDRESS", "OPENSSL_CONF", "UNI_VPN_STATE"):
            self.assertNotIn(name, env)

    def test_remove_stale_state_files(self):
        tmp = Path(tempfile.mkdtemp())
        (tmp / "tunnel-123.env").write_text("x")
        (tmp / "daemon.log").write_text("x")
        self.assertEqual(wintunnel.remove_stale_state_files(tmp), 1)
        self.assertEqual([p.name for p in tmp.iterdir()], ["daemon.log"])
        self.assertEqual(wintunnel.remove_stale_state_files(tmp / "missing"), 0)


class PosixWindowsTunnel(wintunnel.WindowsTunnel):
    """The Windows tunnel logic, started like on POSIX so it runs against the fake here."""

    def _spawn_kwargs(self):
        return {"start_new_session": True}


@unittest.skipIf(sys.platform == "win32", "the fake openconnect is a POSIX script")
class FlowTests(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        tmp = Path(tempfile.mkdtemp())
        self.token_dir = tmp / "state"
        patcher = mock.patch.dict(os.environ, {"FAKE_MODE": "ok", "FAKE_DELAY": "0.1",
                                               "FAKE_PASSWORD_FILE": str(tmp / "pw")})
        patcher.start()
        self.addCleanup(patcher.stop)
        ctrl_c = mock.patch.object(windows, "send_ctrl_c", lambda pid, python=None: os.kill(pid, signal.SIGINT) or True)
        ctrl_c.start()
        self.addCleanup(ctrl_c.stop)

        async def echo(reader, writer):
            while data := await reader.read(1024):
                writer.write(data)
                await writer.drain()
            writer.close()

        self.echo = await asyncio.start_server(echo, "127.0.0.1", 0)
        self.addAsyncCleanup(self._close_echo)
        self.tunnel = PosixWindowsTunnel(Config(user="u", host="vpn.example"), FAKE, "script.js",
                                         logging.getLogger("t"), token_dir=self.token_dir)

    async def _close_echo(self):
        self.echo.close()

    async def test_socks_server_comes_up_from_script_state_and_goes_away_on_stop(self):
        await self.tunnel.start(b"pw", "base32:GEZDGNBVGY3TQOJQ")
        self.assertTrue(await self.tunnel.wait_ready(5))
        self.assertTrue(self.tunnel.state_file.exists())
        port = self.echo.sockets[0].getsockname()[1]
        reader, writer = await asyncio.open_connection("127.0.0.1", self.tunnel.port)
        writer.write(b"\x05\x01\x00\x05\x01\x00\x01\x7f\x00\x00\x01" + port.to_bytes(2, "big"))
        self.assertEqual(await reader.readexactly(2), b"\x05\x00")
        self.assertEqual((await reader.readexactly(10))[1], 0)
        writer.write(b"ping")
        self.assertEqual(await asyncio.wait_for(reader.readexactly(4), 3), b"ping")
        writer.close()
        await self.tunnel.stop(3)
        self.assertTrue(self.tunnel.exited.is_set())
        self.assertIsNone(self.tunnel.socks)
        self.assertFalse(self.tunnel.state_file.exists())
        self.assertEqual(list(self.token_dir.glob("totp-*")), [])

    async def test_script_error_ends_the_attempt_with_its_message(self):
        os.environ["FAKE_SCRIPT_ERROR"] = "netsh add route failed"
        self.addCleanup(os.environ.pop, "FAKE_SCRIPT_ERROR", None)
        self.tunnel.cfg.stop_grace = 2
        await self.tunnel.start(b"pw")
        self.assertFalse(await self.tunnel.wait_ready(5))
        self.assertTrue(self.tunnel.exited.is_set())
        self.assertFalse(self.tunnel.stopped_by_us)
        self.assertEqual(self.tunnel.classification, ("error", "Tunnel setup failed: netsh add route failed"))
        self.assertIsNone(self.tunnel.socks)

    async def test_stop_before_the_process_exists_ends_the_attempt(self):
        await self.tunnel.stop(2)
        self.assertTrue(self.tunnel.stopped_by_us)
        await self.tunnel.start(b"pw")
        self.assertFalse(await self.tunnel.wait_ready(3))
        self.assertTrue(self.tunnel.exited.is_set())
        await self.tunnel.stop(2)

    async def test_server_stops_when_openconnect_dies(self):
        await self.tunnel.start(b"pw")
        self.assertTrue(await self.tunnel.wait_ready(5))
        self.tunnel.proc.kill()
        await asyncio.wait_for(self.tunnel.exited.wait(), 3)
        for _ in range(20):
            if self.tunnel.socks is None:
                break
            await asyncio.sleep(0.05)
        self.assertIsNone(self.tunnel.socks)


class TaskXmlTests(unittest.TestCase):
    def test_render_task_escapes_and_quotes(self):
        xml = windows.render_task("C:\\Python\\pythonw.exe", "C:\\Users\\A & B\\uni-vpn\\bin\\uni-vpn",
                                  "PC\\a&b", "C:\\Users\\A & B\\uni-vpn")
        self.assertIn("<Command>C:\\Python\\pythonw.exe</Command>", xml)
        # -I: no user site-packages or PYTHON* variables, which the user could plant code in.
        self.assertIn("<Arguments>-I \"C:\\Users\\A &amp; B\\uni-vpn\\bin\\uni-vpn\" daemon</Arguments>", xml)
        self.assertIn("<UserId>PC\\a&amp;b</UserId>", xml)
        self.assertIn("<RunLevel>HighestAvailable</RunLevel>", xml)
        self.assertIn("<ExecutionTimeLimit>PT0S</ExecutionTimeLimit>", xml)
        minidom.parseString(xml.encode("utf-16"))


if __name__ == "__main__":
    unittest.main()
