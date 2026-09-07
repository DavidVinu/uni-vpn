import os
import plistlib
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from uni_vpn import platform as pf
from uni_vpn import service


class RenderTests(unittest.TestCase):
    def test_render_replaces_all(self):
        self.assertEqual(service.render("a @X@ b @Y@", {"X": "1", "Y": "2"}), "a 1 b 2")

    def test_render_rejects_leftover(self):
        with self.assertRaises(ValueError):
            service.render("a @X@ @Z@", {"X": "1"})

    def test_systemd_unit(self):
        with mock.patch.object(pf, "IS_MACOS", False):
            text = service.render_unit("/usr/bin/python3", "/home/x/uni-vpn/bin/uni-vpn", "/home/x/.local/state/uni-vpn")
        self.assertIn('ExecStart="/usr/bin/python3" "/home/x/uni-vpn/bin/uni-vpn" daemon', text)
        self.assertIn("WantedBy=graphical-session.target", text)
        self.assertIn("PartOf=graphical-session.target", text)
        self.assertNotIn("@", text)
        self.assertNotIn("Environment=", text)

    def test_systemd_unit_quotes_path_with_space(self):
        with mock.patch.object(pf, "IS_MACOS", False):
            text = service.render_unit("/usr/bin/python3", "/home/x/Uni Zeug/uni-vpn/bin/uni-vpn", "/home/x/.local/state/uni-vpn")
        self.assertIn('ExecStart="/usr/bin/python3" "/home/x/Uni Zeug/uni-vpn/bin/uni-vpn" daemon', text)

    def test_render_unit_rejects_quote_and_backslash(self):
        for bad in ('/home/x/a"b/uni-vpn', "/home/x/a\\b/uni-vpn"):
            for is_macos in (False, True):
                with mock.patch.object(pf, "IS_MACOS", is_macos), self.assertRaises(ValueError, msg=bad):
                    service.render_unit("/usr/bin/python3", bad, "/home/x/state")
        with mock.patch.object(pf, "IS_MACOS", False), self.assertRaises(ValueError):
            service.render_unit("/usr/bin/python3", "/home/x/uni-vpn", "/home/x/state",
                                extra_env={"XDG_CONFIG_HOME": '/home/x/"cfg'})

    def test_systemd_unit_extra_env(self):
        with mock.patch.object(pf, "IS_MACOS", False):
            text = service.render_unit("/usr/bin/python3", "/home/x/uni-vpn/bin/uni-vpn", "/home/x/.local/state/uni-vpn",
                                       extra_env={"XDG_CONFIG_HOME": "/home/x/.cfg", "XDG_STATE_HOME": "/home/x/st ate"})
        self.assertIn('Environment="XDG_CONFIG_HOME=/home/x/.cfg"', text)
        self.assertIn('Environment="XDG_STATE_HOME=/home/x/st ate"', text)
        self.assertNotIn("@", text)

    def test_launchd_plist_extra_env(self):
        with mock.patch.object(pf, "IS_MACOS", True):
            text = service.render_unit("/opt/homebrew/bin/python3", "/Users/x/Uni Zeug/uni-vpn/bin/uni-vpn",
                                       "/Users/x/Library/Logs/uni-vpn", brew_prefix="/opt/homebrew",
                                       extra_env={"XDG_CONFIG_HOME": "/Users/x/.cfg & co"})
        data = plistlib.loads(text.encode())
        self.assertEqual(data["ProgramArguments"][1], "/Users/x/Uni Zeug/uni-vpn/bin/uni-vpn")
        self.assertEqual(data["EnvironmentVariables"]["XDG_CONFIG_HOME"], "/Users/x/.cfg & co")
        self.assertTrue(data["EnvironmentVariables"]["PATH"].startswith("/opt/homebrew/bin:"))
        with mock.patch.object(pf, "IS_MACOS", True):
            plain = service.render_unit("/opt/homebrew/bin/python3", "/Users/x/uni-vpn/bin/uni-vpn",
                                        "/Users/x/Library/Logs/uni-vpn", brew_prefix="/opt/homebrew")
        self.assertNotIn("XDG_CONFIG_HOME", plistlib.loads(plain.encode())["EnvironmentVariables"])

    def test_launchd_plist(self):
        with mock.patch.object(pf, "IS_MACOS", True):
            text = service.render_unit("/opt/homebrew/bin/python3", "/Users/x/uni-vpn/bin/uni-vpn",
                                       "/Users/x/Library/Logs/uni-vpn", brew_prefix="/opt/homebrew")
        data = plistlib.loads(text.encode())
        self.assertEqual(data["Label"], "de.davidvinu.uni-vpn")
        self.assertEqual(data["ProgramArguments"], ["/opt/homebrew/bin/python3", "/Users/x/uni-vpn/bin/uni-vpn", "daemon"])
        self.assertTrue(data["EnvironmentVariables"]["PATH"].startswith("/opt/homebrew/bin:"))
        self.assertTrue(data["KeepAlive"])


class InstallTests(unittest.TestCase):
    def setUp(self):
        self.home = Path(tempfile.mkdtemp())
        self.env = mock.patch.dict(os.environ, {"HOME": str(self.home), "XDG_CONFIG_HOME": str(self.home / ".config")})
        self.env.start()
        self.calls = []

    def tearDown(self):
        self.env.stop()

    def run_ok(self, cmd, **kwargs):
        self.calls.append(cmd)
        return subprocess.CompletedProcess(cmd, 0, "", "")

    def test_linux_install_writes_unit_and_enables(self):
        with mock.patch.object(pf, "IS_MACOS", False), mock.patch.object(Path, "home", return_value=self.home):
            files = service.install(run=self.run_ok)
        unit = self.home / ".config" / "systemd" / "user" / "uni-vpn.service"
        self.assertEqual(files, [unit])
        self.assertTrue(unit.exists())
        self.assertIn(["systemctl", "--user", "daemon-reload"], self.calls)
        self.assertIn(["systemctl", "--user", "enable", "--now", "uni-vpn"], self.calls)

    def test_install_takes_xdg_from_environment(self):
        unit = self.home / ".config" / "systemd" / "user" / "uni-vpn.service"
        with mock.patch.object(pf, "IS_MACOS", False), mock.patch.object(Path, "home", return_value=self.home):
            with mock.patch.dict(os.environ, {"XDG_STATE_HOME": str(self.home / "st")}):
                service.install(run=self.run_ok)
            text = unit.read_text()
            self.assertIn(f'Environment="XDG_CONFIG_HOME={self.home / ".config"}"', text)
            self.assertIn(f'Environment="XDG_STATE_HOME={self.home / "st"}"', text)
            os.environ.pop("XDG_CONFIG_HOME")
            os.environ.pop("XDG_STATE_HOME", None)
            service.install(run=self.run_ok)
            self.assertNotIn("Environment=", unit.read_text())

    def test_install_reports_failed_load(self):
        def run_fail(cmd, **kwargs):
            if cmd[:3] == ["systemctl", "--user", "daemon-reload"]:
                raise subprocess.CalledProcessError(1, cmd, "", "Failed to connect to bus: No medium found")
            return self.run_ok(cmd, **kwargs)

        unit = self.home / ".config" / "systemd" / "user" / "uni-vpn.service"
        with mock.patch.object(pf, "IS_MACOS", False), mock.patch.object(Path, "home", return_value=self.home):
            with self.assertRaises(service.ServiceError) as ctx:
                service.install(run=run_fail)
        self.assertIn("Failed to connect to bus", str(ctx.exception))
        self.assertEqual(ctx.exception.files, [unit])
        self.assertTrue(unit.exists())

    def test_install_reports_missing_systemctl(self):
        def run_missing(cmd, **kwargs):
            raise FileNotFoundError(2, "No such file or directory", cmd[0])

        with mock.patch.object(pf, "IS_MACOS", False), mock.patch.object(Path, "home", return_value=self.home):
            with self.assertRaises(service.ServiceError) as ctx:
                service.install(run=run_missing)
        self.assertIn("systemctl", str(ctx.exception))

    def test_dry_run_writes_nothing(self):
        with mock.patch.object(pf, "IS_MACOS", False), mock.patch.object(Path, "home", return_value=self.home):
            files = service.install(dry_run=True, run=self.run_ok)
        self.assertFalse(files[0].exists())
        self.assertEqual(self.calls, [])

    def test_macos_install_bootstraps(self):
        with mock.patch.object(pf, "IS_MACOS", True), mock.patch.object(Path, "home", return_value=self.home), \
             mock.patch.object(pf, "brew_prefix", return_value="/opt/homebrew"):
            files = service.install(run=self.run_ok)
        plist = self.home / "Library" / "LaunchAgents" / "de.davidvinu.uni-vpn.plist"
        self.assertEqual(files, [plist])
        plistlib.loads(plist.read_bytes())
        self.assertTrue(any(c[:2] == ["launchctl", "bootstrap"] for c in self.calls))

    def test_uninstall_removes_unit(self):
        with mock.patch.object(pf, "IS_MACOS", False), mock.patch.object(Path, "home", return_value=self.home):
            unit = service.install(run=self.run_ok)[0]
            service.uninstall(run=self.run_ok)
        self.assertFalse(unit.exists())
        self.assertIn(["systemctl", "--user", "disable", "--now", "uni-vpn"], self.calls)

    def test_is_active(self):
        def run(cmd, **kwargs):
            return subprocess.CompletedProcess(cmd, 0, "active\n", "")

        with mock.patch.object(pf, "IS_MACOS", False):
            self.assertTrue(service.is_active(run=run))
