import asyncio
import io
import json
import logging
import os
import stat
import tempfile
import threading
import time
import unittest
import urllib.request
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path
from unittest import mock

from uni_vpn import cli, config, credentials, logsetup
from uni_vpn import daemon as dm
from uni_vpn import platform as pf
from uni_vpn.tunnel import free_port

from tests.test_daemon import DaemonHarness, wait_state


class FormatTests(unittest.TestCase):
    def test_format_status(self):
        text = cli.format_status({"state": "connected", "message": "Connected", "user": "ab1", "host": "h",
                                  "socks_port": 1080, "active_connections": 2})
        self.assertEqual(text, "connected: Connected (ab1@h, SOCKS 127.0.0.1:1080, 2 connections)")


class ApiTests(DaemonHarness):
    def config_path(self):
        path = Path(tempfile.mkdtemp()) / "config.toml"
        path.write_text(
            f'user = "u"\nhost = "vpn.example"\nsocks_port = {self.cfg.socks_port}\nhttp_port = {self.cfg.http_port}\n')
        return str(path)

    async def run_cli(self, *argv):
        # The CLI blocks synchronously in urlopen; the daemon server runs on this loop.
        return await asyncio.to_thread(cli.main, ["--config", self.config_path(), *argv])

    async def test_status_json_and_connect(self):
        d = await self.start_daemon()
        out = io.StringIO()
        with redirect_stdout(out):
            rc = await self.run_cli("status", "--json")
        self.assertEqual(rc, 0)
        self.assertEqual(json.loads(out.getvalue())["state"], "idle")
        with redirect_stdout(io.StringIO()):
            self.assertEqual(await self.run_cli("connect"), 0)
        await wait_state(d, dm.State.connected)
        with redirect_stdout(io.StringIO()):
            self.assertEqual(await self.run_cli("disconnect"), 0)
        await wait_state(d, dm.State.idle)

    async def test_status_unreachable(self):
        cfg_path = self.config_path()
        out = io.StringIO()
        with redirect_stdout(out):
            rc = cli.main(["--config", cfg_path, "status"])
        self.assertEqual(rc, 1)
        self.assertIn("not reachable", out.getvalue())

    async def test_password_command_stores_and_connects(self):
        d = await self.start_daemon()
        stored = []
        with mock.patch.object(cli.getpass, "getpass", return_value="pw1"), \
             mock.patch.object(credentials, "store_password", lambda user, pw: stored.append((user, pw))), \
             redirect_stdout(io.StringIO()):
            rc = await self.run_cli("password")
        self.assertEqual(rc, 0)
        self.assertEqual(stored, [("u", "pw1")])
        await wait_state(d, dm.State.connected)

    async def test_totp_command_normalizes_stores_and_connects(self):
        d = await self.start_daemon()
        stored = []
        out = io.StringIO()
        with mock.patch.object(cli.getpass, "getpass", return_value="gezd gnbv gy3t qojq gezd gnbv gy3t qojq"), \
             mock.patch.object(credentials, "store_totp", lambda user, token: stored.append((user, token))), \
             redirect_stdout(out):
            rc = await self.run_cli("totp")
        self.assertEqual(rc, 0)
        self.assertEqual(stored, [("u", "base32:GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ")])
        self.assertIn("Check code", out.getvalue())
        self.assertRegex(out.getvalue(), r"[0-9]{6}")
        self.assertNotIn("GEZDGNBVGY3TQOJQ", out.getvalue())
        await wait_state(d, dm.State.connected)

    async def test_totp_command_rejects_bad_input(self):
        for text in ("", "0189", "otpauth://hotp/x?secret=GEZDGNBVGY3TQOJQ"):
            out = io.StringIO()
            with mock.patch.object(cli.getpass, "getpass", return_value=text), \
                 mock.patch.object(credentials, "store_totp") as store, redirect_stdout(out):
                self.assertEqual(cli.main(["--config", self.config_path(), "totp"]), 2, repr(text))
            store.assert_not_called()
            self.assertTrue(out.getvalue().strip(), repr(text))

    async def test_password_empty_rejected(self):
        with mock.patch.object(cli.getpass, "getpass", return_value=""), redirect_stdout(io.StringIO()):
            self.assertEqual(cli.main(["--config", self.config_path(), "password"]), 2)

    async def test_password_newline_rejected(self):
        for password in ("a\nb", "a\rb"):
            out = io.StringIO()
            with mock.patch.object(cli.getpass, "getpass", return_value=password), \
                 mock.patch.object(credentials, "store_password") as store, redirect_stdout(out):
                self.assertEqual(cli.main(["--config", self.config_path(), "password"]), 2)
            store.assert_not_called()
            self.assertIn("line break", out.getvalue())

    async def test_config_error_exit_code(self):
        path = Path(tempfile.mkdtemp()) / "config.toml"
        path.write_text("user = 1\n")
        with redirect_stdout(io.StringIO()):
            self.assertEqual(cli.main(["--config", str(path), "status"]), 2)

    async def test_log_command(self):
        log_path = Path(tempfile.mkdtemp()) / "daemon.log"
        log_path.write_text("\n".join(f"line {i}" for i in range(300)) + "\n")
        out = io.StringIO()
        with mock.patch("uni_vpn.platform.log_file", return_value=log_path), redirect_stdout(out):
            self.assertEqual(cli.main(["--config", self.config_path(), "log", "-n", "5"]), 0)
        self.assertEqual(out.getvalue().splitlines(), [f"line {i}" for i in range(295, 300)])


def file_mode(path: Path) -> int:
    return stat.S_IMODE(path.stat().st_mode)


class LogFileModeTests(unittest.TestCase):
    def test_rotated_log_file_is_private(self):
        self.addCleanup(os.umask, os.umask(0o022))
        path = Path(tempfile.mkdtemp()) / "daemon.log"
        handler = logsetup.PrivateRotatingFileHandler(path, maxBytes=200, backupCount=2, encoding="utf-8")
        self.addCleanup(handler.close)
        self.assertEqual(file_mode(path), 0o600)
        handler.emit(logging.makeLogRecord({"msg": "x" * 100}))
        handler.doRollover()
        self.assertTrue(path.with_name("daemon.log.1").exists())
        self.assertEqual(file_mode(path), 0o600)
        handler.emit(logging.makeLogRecord({"msg": "after the rollover"}))
        self.assertEqual(file_mode(path), 0o600)
        self.assertIn("after the rollover", path.read_text(encoding="utf-8"))


class DaemonCommandTests(unittest.TestCase):
    """cmd_daemon without a real service: lock file and log live in the temp directory."""

    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp())
        self.addCleanup(os.umask, os.umask(0o022))  # cmd_daemon sets the umask process-wide
        self.addCleanup(logging.getLogger("uni-vpn").handlers.clear)
        for patch in (mock.patch.object(pf, "lock_file", return_value=self.tmp / "daemon.lock"),
                      mock.patch.object(pf, "log_file", return_value=self.tmp / "daemon.log"),
                      mock.patch.object(cli, "harden")):  # no RLIMIT_CORE or PR_SET_DUMPABLE in the test process
            patch.start()
            self.addCleanup(patch.stop)

    def test_second_daemon_exits_zero_when_lock_is_held(self):
        cfg_path = self.tmp / "config.toml"
        cfg_path.write_text(f'user = "u"\nsocks_port = {free_port()}\nhttp_port = {free_port()}\n')
        lock_path = self.tmp / "daemon.lock"
        lock_path.parent.mkdir(parents=True, exist_ok=True)
        holder = cli.acquire_lock(lock_path)
        self.assertIsNotNone(holder)
        self.addCleanup(holder.close)
        err = io.StringIO()
        with redirect_stderr(err):
            rc = cli.main(["--config", str(cfg_path), "daemon"])
        self.assertEqual(rc, 0)
        self.assertIn("already running", err.getvalue())
        self.assertFalse((self.tmp / "daemon.log").exists())

    def test_config_error_daemon_serves_status_with_line(self):
        socks_port, http_port = free_port(), free_port()
        cfg_path = self.tmp / "config.toml"
        cfg_path.write_text(f'user = "u"\nsocks_port = {socks_port}\nhttp_port = {http_port}\nidle_minutes = "x"\n')
        captured = {}
        ready = threading.Event()

        def capture(loop, daemon):
            captured["loop"], captured["daemon"] = loop, daemon
            ready.set()

        results = []
        err = io.StringIO()
        with mock.patch.object(cli, "install_signal_handlers", capture), redirect_stderr(err):
            thread = threading.Thread(target=lambda: results.append(cli.main(["--config", str(cfg_path), "daemon"])))
            thread.start()
            try:
                self.assertTrue(ready.wait(3), "daemon was not started")
                data = None
                deadline = time.monotonic() + 3
                while time.monotonic() < deadline:
                    try:
                        with urllib.request.urlopen(f"http://127.0.0.1:{http_port}/status.json", timeout=1) as response:
                            data = json.loads(response.read().decode("utf-8"))
                        break
                    except OSError:
                        time.sleep(0.05)
            finally:
                if "loop" in captured:
                    captured["loop"].call_soon_threadsafe(captured["daemon"].stop)
                thread.join(5)
        self.assertFalse(thread.is_alive(), "daemon thread still running")
        self.assertEqual(results, [0])
        self.assertIsNotNone(data, "status.json not reachable")
        self.assertEqual(data["state"], "error")
        self.assertIn("line 4", data["message"])
        self.assertEqual(data["http_port"], http_port)
        self.assertEqual(data["socks_port"], socks_port)
        self.assertIn("line 4", (self.tmp / "daemon.log").read_text(encoding="utf-8"))
        self.assertEqual(file_mode(self.tmp / "daemon.log"), 0o600)
        self.assertEqual(file_mode(self.tmp / "daemon.lock"), 0o600)
