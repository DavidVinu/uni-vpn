import json
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from uni_vpn import platform as pf
from uni_vpn import sysproxy


class Runner:
    """Captures commands and returns prepared answers."""

    def __init__(self, answers=None):
        self.calls = []
        self.answers = answers or {}

    def __call__(self, cmd, **kwargs):
        self.calls.append(cmd)
        for key, (rc, out) in self.answers.items():
            if " ".join(cmd).startswith(key):
                return subprocess.CompletedProcess(cmd, rc, out, "")
        return subprocess.CompletedProcess(cmd, 0, "", "")


class PacUrlTests(unittest.TestCase):
    def test_pac_url_with_and_without_version(self):
        self.assertEqual(sysproxy.pac_url(1081), "http://127.0.0.1:1081/proxy.pac")
        self.assertEqual(sysproxy.pac_url(1081, 42), "http://127.0.0.1:1081/proxy.pac?v=42")
        self.assertTrue(sysproxy.is_ours("http://127.0.0.1:1081/proxy.pac?v=7", 1081))
        self.assertFalse(sysproxy.is_ours("http://127.0.0.1:1082/proxy.pac", 1081))
        self.assertFalse(sysproxy.is_ours("", 1081))


def as_platform(test, macos=False, windows=False):
    for name, value in (("IS_MACOS", macos), ("IS_WINDOWS", windows)):
        patcher = mock.patch.object(pf, name, value)
        patcher.start()
        test.addCleanup(patcher.stop)


class LinuxTests(unittest.TestCase):
    def setUp(self):
        as_platform(self)
        env = mock.patch.dict("os.environ", {"XDG_CURRENT_DESKTOP": "ubuntu:GNOME"})
        env.start()
        self.addCleanup(env.stop)

    def test_current_reads_gsettings(self):
        run = Runner({"gsettings get org.gnome.system.proxy mode": (0, "'auto'\n"),
                      "gsettings get org.gnome.system.proxy autoconfig-url": (0, "'http://x/p.pac'\n")})
        self.assertEqual(sysproxy.current(run=run), {"mode": "auto", "url": "http://x/p.pac"})

    def test_current_without_gsettings_is_none(self):
        def run(cmd, **kwargs):
            raise FileNotFoundError("gsettings")

        self.assertIsNone(sysproxy.current(run=run))

    def test_apply_sets_auto_mode_and_url(self):
        run = Runner()
        sysproxy.apply("http://127.0.0.1:1081/proxy.pac", run=run)
        self.assertIn(["gsettings", "set", "org.gnome.system.proxy", "autoconfig-url", "http://127.0.0.1:1081/proxy.pac"], run.calls)
        self.assertIn(["gsettings", "set", "org.gnome.system.proxy", "mode", "auto"], run.calls)

    def test_restore_puts_previous_values_back(self):
        run = Runner()
        sysproxy.restore({"mode": "none", "url": ""}, run=run)
        self.assertIn(["gsettings", "set", "org.gnome.system.proxy", "mode", "none"], run.calls)
        self.assertIn(["gsettings", "set", "org.gnome.system.proxy", "autoconfig-url", ""], run.calls)

    def test_refresh_bumps_url_version_so_browsers_refetch(self):
        run = Runner()
        with mock.patch.object(sysproxy.time, "time", return_value=1700000000.9):
            sysproxy.refresh(1081, run=run)
        self.assertIn(["gsettings", "set", "org.gnome.system.proxy", "autoconfig-url", "http://127.0.0.1:1081/proxy.pac?v=1700000000"], run.calls)

    def test_install_backs_up_then_applies_and_uninstall_restores(self):
        backup = Path(tempfile.mkdtemp()) / "proxy-backup.json"
        run = Runner({"gsettings get org.gnome.system.proxy mode": (0, "'none'\n"),
                      "gsettings get org.gnome.system.proxy autoconfig-url": (0, "''\n")})
        result = sysproxy.install(1081, backup=backup, run=run)
        self.assertEqual(result, "ok")
        self.assertEqual(json.loads(backup.read_text()), {"mode": "none", "url": ""})
        self.assertIn(["gsettings", "set", "org.gnome.system.proxy", "mode", "auto"], run.calls)
        run2 = Runner()
        self.assertTrue(sysproxy.uninstall(backup=backup, run=run2))
        self.assertIn(["gsettings", "set", "org.gnome.system.proxy", "mode", "none"], run2.calls)
        self.assertFalse(backup.exists())

    def test_install_keeps_existing_backup_when_rerun(self):
        # On the second install.sh our own entry is active; the original state stays backed up.
        backup = Path(tempfile.mkdtemp()) / "proxy-backup.json"
        backup.write_text(json.dumps({"mode": "none", "url": ""}))
        run = Runner({"gsettings get org.gnome.system.proxy mode": (0, "'auto'\n"),
                      "gsettings get org.gnome.system.proxy autoconfig-url": (0, "'http://127.0.0.1:1081/proxy.pac?v=1'\n")})
        sysproxy.install(1081, backup=backup, run=run)
        self.assertEqual(json.loads(backup.read_text()), {"mode": "none", "url": ""})

    def test_install_reports_foreign_proxy(self):
        backup = Path(tempfile.mkdtemp()) / "proxy-backup.json"
        run = Runner({"gsettings get org.gnome.system.proxy mode": (0, "'manual'\n"),
                      "gsettings get org.gnome.system.proxy autoconfig-url": (0, "''\n")})
        self.assertEqual(sysproxy.install(1081, backup=backup, run=run), "replaced")
        self.assertEqual(json.loads(backup.read_text())["mode"], "manual")

    def test_install_without_gsettings(self):
        def run(cmd, **kwargs):
            raise FileNotFoundError("gsettings")

        self.assertEqual(sysproxy.install(1081, backup=Path(tempfile.mkdtemp()) / "b.json", run=run), "unavailable")

    def test_uninstall_without_backup_is_noop(self):
        run = Runner()
        self.assertFalse(sysproxy.uninstall(backup=Path(tempfile.mkdtemp()) / "missing.json", run=run))
        self.assertEqual(run.calls, [])

    def test_state_for_doctor(self):
        run = Runner({"gsettings get org.gnome.system.proxy mode": (0, "'auto'\n"),
                      "gsettings get org.gnome.system.proxy autoconfig-url": (0, "'http://127.0.0.1:1081/proxy.pac?v=3'\n")})
        self.assertEqual(sysproxy.state(1081, run=run), "ok")
        run = Runner({"gsettings get org.gnome.system.proxy mode": (0, "'none'\n")})
        self.assertEqual(sysproxy.state(1081, run=run), "unset")
        run = Runner({"gsettings get org.gnome.system.proxy mode": (0, "'manual'\n")})
        self.assertEqual(sysproxy.state(1081, run=run), "foreign")


class MacTests(unittest.TestCase):
    def setUp(self):
        as_platform(self, macos=True)
        self.answers = {
            "networksetup -listallnetworkservices": (0, "An asterisk (*) denotes that a network service is disabled.\nWi-Fi\n*Thunderbolt Bridge\n"),
            "networksetup -getautoproxyurl Wi-Fi": (0, "URL: (null)\nEnabled: No\n"),
        }

    def test_current_lists_enabled_services(self):
        run = Runner(self.answers)
        self.assertEqual(sysproxy.current(run=run), {"services": {"Wi-Fi": {"url": "", "enabled": False}}})

    def test_apply_sets_url_on_every_service(self):
        run = Runner(self.answers)
        sysproxy.apply("http://127.0.0.1:1081/proxy.pac", run=run)
        self.assertIn(["networksetup", "-setautoproxyurl", "Wi-Fi", "http://127.0.0.1:1081/proxy.pac"], run.calls)
        self.assertNotIn(["networksetup", "-setautoproxyurl", "Thunderbolt Bridge", "http://127.0.0.1:1081/proxy.pac"], run.calls)

    def test_restore_disables_when_previously_off(self):
        run = Runner(self.answers)
        sysproxy.restore({"services": {"Wi-Fi": {"url": "", "enabled": False}}}, run=run)
        self.assertIn(["networksetup", "-setautoproxystate", "Wi-Fi", "off"], run.calls)

    def test_restore_puts_old_url_back(self):
        run = Runner(self.answers)
        sysproxy.restore({"services": {"Wi-Fi": {"url": "http://old/p.pac", "enabled": True}}}, run=run)
        self.assertIn(["networksetup", "-setautoproxyurl", "Wi-Fi", "http://old/p.pac"], run.calls)

    def test_state_ok_when_our_url_is_enabled(self):
        answers = dict(self.answers)
        answers["networksetup -getautoproxyurl Wi-Fi"] = (0, "URL: http://127.0.0.1:1081/proxy.pac\nEnabled: Yes\n")
        self.assertEqual(sysproxy.state(1081, run=Runner(answers)), "ok")
        self.assertEqual(sysproxy.state(1081, run=Runner(self.answers)), "unset")

    def test_refresh_is_noop_on_macos(self):
        run = Runner(self.answers)
        sysproxy.refresh(1081, run=run)
        self.assertEqual(run.calls, [])


class KdeTests(unittest.TestCase):
    def setUp(self):
        as_platform(self)
        env = mock.patch.dict("os.environ", {"XDG_CURRENT_DESKTOP": "KDE"})
        env.start()
        self.addCleanup(env.stop)
        tools = mock.patch.object(pf, "find_binary", lambda name, override=None: f"/usr/bin/{name}")
        tools.start()
        self.addCleanup(tools.stop)
        self.answers = {
            "gsettings get": (1, ""),
            "/usr/bin/kreadconfig6 --file kioslaverc --group Proxy Settings --key ProxyType": (0, "0\n"),
            "/usr/bin/kreadconfig6 --file kioslaverc --group Proxy Settings --key Proxy Config Script": (0, "\n"),
        }

    def test_install_writes_kioslaverc_and_uninstall_restores(self):
        backup = Path(tempfile.mkdtemp()) / "backup.json"
        run = Runner(self.answers)
        self.assertEqual(sysproxy.install(1081, backup=backup, run=run), "ok")
        self.assertEqual(json.loads(backup.read_text()), {"kde": {"type": "0", "url": ""}})
        write = ["/usr/bin/kwriteconfig6", "--file", "kioslaverc", "--group", "Proxy Settings", "--key"]
        self.assertIn(write + ["ProxyType", "2"], run.calls)
        self.assertIn(write + ["Proxy Config Script", "http://127.0.0.1:1081/proxy.pac"], run.calls)
        run2 = Runner(self.answers)
        self.assertTrue(sysproxy.uninstall(backup=backup, run=run2))
        self.assertIn(write + ["ProxyType", "0"], run2.calls)

    def test_state_follows_kde(self):
        answers = dict(self.answers)
        answers["/usr/bin/kreadconfig6 --file kioslaverc --group Proxy Settings --key ProxyType"] = (0, "2\n")
        answers["/usr/bin/kreadconfig6 --file kioslaverc --group Proxy Settings --key Proxy Config Script"] = (
            0, "http://127.0.0.1:1081/proxy.pac?v=3\n")
        self.assertEqual(sysproxy.state(1081, run=Runner(answers)), "ok")
        self.assertEqual(sysproxy.state(1081, run=Runner(self.answers)), "unset")

    def test_without_kde_tools_and_gsettings_is_unavailable(self):
        with mock.patch.object(pf, "find_binary", lambda name, override=None: None):
            self.assertIsNone(sysproxy.current(run=Runner({"gsettings get": (1, "")})))


class WindowsTests(unittest.TestCase):
    def setUp(self):
        as_platform(self, windows=True)
        self.registry = {"url": ""}
        self.notified = []
        from uni_vpn import windows

        def proxy_set(url):
            self.registry["url"] = url
            self.notified.append(url)

        for name, value in (("proxy_get", lambda: dict(self.registry)), ("proxy_set", proxy_set)):
            patcher = mock.patch.object(windows, name, value)
            patcher.start()
            self.addCleanup(patcher.stop)

    def test_install_sets_autoconfig_url_and_uninstall_removes_it(self):
        backup = Path(tempfile.mkdtemp()) / "backup.json"
        self.assertEqual(sysproxy.install(1081, backup=backup), "ok")
        self.assertEqual(self.registry["url"], "http://127.0.0.1:1081/proxy.pac")
        self.assertEqual(sysproxy.state(1081), "ok")
        self.assertTrue(sysproxy.uninstall(backup=backup))
        self.assertEqual(self.registry["url"], "")
        self.assertEqual(sysproxy.state(1081), "unset")

    def test_foreign_pac_is_replaced_and_restored(self):
        self.registry["url"] = "http://corp/wpad.dat"
        backup = Path(tempfile.mkdtemp()) / "backup.json"
        self.assertEqual(sysproxy.state(1081), "foreign")
        self.assertEqual(sysproxy.install(1081, backup=backup), "replaced")
        self.assertTrue(sysproxy.uninstall(backup=backup))
        self.assertEqual(self.registry["url"], "http://corp/wpad.dat")

    def test_refresh_bumps_version_only_when_ours(self):
        sysproxy.refresh(1081)
        self.assertEqual(self.notified, [])
        self.registry["url"] = "http://127.0.0.1:1081/proxy.pac"
        sysproxy.refresh(1081)
        self.assertTrue(self.registry["url"].startswith("http://127.0.0.1:1081/proxy.pac?v="))

