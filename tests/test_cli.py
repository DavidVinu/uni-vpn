import asyncio
import io
import json
import tempfile
import unittest
from contextlib import redirect_stdout
from pathlib import Path
from unittest import mock

from uni_vpn import cli, config, credentials
from uni_vpn import daemon as dm

from tests.test_daemon import DaemonHarness, wait_state


class FormatTests(unittest.TestCase):
    def test_format_status(self):
        text = cli.format_status({"state": "connected", "message": "Verbunden", "user": "ab1", "host": "h",
                                  "socks_port": 1080, "active_connections": 2})
        self.assertEqual(text, "connected: Verbunden (ab1@h, SOCKS 127.0.0.1:1080, 2 Verbindungen)")


class ApiTests(DaemonHarness):
    def config_path(self):
        path = Path(tempfile.mkdtemp()) / "config.toml"
        path.write_text(
            f'user = "u"\nhost = "vpn.example"\nsocks_port = {self.cfg.socks_port}\nhttp_port = {self.cfg.http_port}\n')
        return str(path)

    async def run_cli(self, *argv):
        # Die CLI blockiert synchron in urlopen; der Daemon-Server laeuft auf dieser Loop.
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
        self.assertIn("nicht erreichbar", out.getvalue())

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

    async def test_password_empty_rejected(self):
        with mock.patch.object(cli.getpass, "getpass", return_value=""), redirect_stdout(io.StringIO()):
            self.assertEqual(cli.main(["--config", self.config_path(), "password"]), 2)

    async def test_config_error_exit_code(self):
        path = Path(tempfile.mkdtemp()) / "config.toml"
        path.write_text("user = 1\n")
        with redirect_stdout(io.StringIO()):
            self.assertEqual(cli.main(["--config", str(path), "status"]), 2)

    async def test_log_command(self):
        log_path = Path(tempfile.mkdtemp()) / "daemon.log"
        log_path.write_text("\n".join(f"zeile {i}" for i in range(300)) + "\n")
        out = io.StringIO()
        with mock.patch("uni_vpn.platform.log_file", return_value=log_path), redirect_stdout(out):
            self.assertEqual(cli.main(["--config", self.config_path(), "log", "-n", "5"]), 0)
        self.assertEqual(out.getvalue().splitlines(), [f"zeile {i}" for i in range(295, 300)])
