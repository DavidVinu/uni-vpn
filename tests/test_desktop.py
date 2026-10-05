import hashlib
import io
import os
import plistlib
import subprocess
import tempfile
import unittest
import zipfile
from pathlib import Path
from unittest import mock

from uni_vpn import desktop
from uni_vpn import platform as pf

ROOT = Path(__file__).resolve().parent.parent


def ok(stdout=""):
    return subprocess.CompletedProcess([], 0, stdout=stdout, stderr="")


class Harness(unittest.TestCase):
    def setUp(self):
        self.home = Path(tempfile.mkdtemp())
        for patch in (mock.patch.dict(os.environ, {"HOME": str(self.home), "XDG_DATA_HOME": str(self.home / "data"),
                                                   "XDG_CONFIG_HOME": str(self.home / "config")}),
                      mock.patch.object(Path, "home", return_value=self.home),
                      mock.patch.object(pf, "IS_MACOS", False), mock.patch.object(pf, "IS_WINDOWS", False),
                      mock.patch.object(desktop, "_spawn", side_effect=self.spawned)):
            patch.start()
            self.addCleanup(patch.stop)
        self.spawns = []
        self.created = []
        self.commands = []

    def spawned(self, command):
        self.spawns.append(command)
        return True

    def run_fake(self, command, **kwargs):
        self.commands.append(command)
        return ok()


class SourcesTests(unittest.TestCase):
    def test_the_shells_and_icons_are_in_the_repository(self):
        for name in ("icon.svg", "icon.png", "macos/main.swift", "windows/UniVpn.cs", "windows/uni-vpn.ico",
                     "linux/uni-vpn-app.py"):
            self.assertTrue((ROOT / "app" / name).is_file(), name)

    def test_linux_shell_compiles(self):
        compile((ROOT / "app" / "linux" / "uni-vpn-app.py").read_text(encoding="utf-8"), "uni-vpn-app.py", "exec")

    def test_shells_open_the_same_pages(self):
        # The page knows #main and #settings and the ?app=1 mode; every shell has to use them.
        page = (ROOT / "uni_vpn" / "ui" / "index.html").read_text(encoding="utf-8")
        self.assertIn('has("app")', page)
        for name in ("macos/main.swift", "windows/UniVpn.cs", "linux/uni-vpn-app.py"):
            text = (ROOT / "app" / name).read_text(encoding="utf-8")
            for needle in ("?app=1", "#settings", "#main", "X-Uni-VPN", "status.json"):
                self.assertIn(needle, text, f"{name}: {needle}")


class MacTests(Harness):
    def test_info_plist_carries_port_and_url_scheme(self):
        info = plistlib.loads(plistlib.dumps(desktop.info_plist(1099)))
        self.assertEqual(info["UniVPNPort"], "1099")
        self.assertEqual(info["CFBundleURLTypes"][0]["CFBundleURLSchemes"], ["uni-vpn"])
        self.assertEqual(info["CFBundleExecutable"], "Uni VPN")
        self.assertNotIn("LSUIElement", info)  # a Dock icon while the window is open

    def test_login_item_starts_the_menu_bar_item_without_a_window(self):
        binary = Path("/x/Uni VPN.app/Contents/MacOS/Uni VPN")
        plist = desktop.login_plist(binary)
        self.assertEqual(plist["ProgramArguments"], [str(binary), "--hidden"])
        self.assertTrue(plist["RunAtLoad"])

    def test_build_puts_the_bundle_in_applications_and_registers_login(self):
        def run(command, **kwargs):
            self.commands.append(command)
            if command[0] == "/usr/bin/swiftc":
                Path(command[command.index("-o") + 1]).write_bytes(b"binary")
            return ok()

        with mock.patch.object(pf, "IS_MACOS", True), mock.patch.object(desktop.shutil, "which",
                                                                          return_value="/usr/bin/swiftc"), \
                mock.patch.object(desktop.os, "getuid", create=True, return_value=501):
            self.assertTrue(desktop.install(1081, False, self.created.append, run=run))
        app = self.home / "Applications" / "Uni VPN.app"
        self.assertEqual((app / "Contents" / "MacOS" / "Uni VPN").read_bytes(), b"binary")
        info = plistlib.loads((app / "Contents" / "Info.plist").read_bytes())
        self.assertEqual(info["UniVPNPort"], "1081")
        login = self.home / "Library" / "LaunchAgents" / "de.davidvinu.uni-vpn.app.plist"
        self.assertIn(app, self.created)
        self.assertIn(login, self.created)
        self.assertIn(["launchctl", "bootstrap", "gui/501", str(login)], self.commands)

    def test_without_swift_the_entry_opens_the_browser(self):
        def run(command, **kwargs):
            self.commands.append(command)
            return subprocess.CompletedProcess(command, 1, stdout="", stderr="")

        with mock.patch.object(pf, "IS_MACOS", True), mock.patch.object(desktop.shutil, "which", return_value=None):
            self.assertFalse(desktop.install(1081, False, self.created.append, run=run))
        self.assertIn(["osacompile", "-o", str(self.home / "Applications" / "Uni VPN.app"), "-e",
                       'open location "http://127.0.0.1:1081/"'], self.commands)

    def test_pages_become_app_urls(self):
        binary = self.home / "Applications" / "Uni VPN.app" / "Contents" / "MacOS" / "Uni VPN"
        binary.parent.mkdir(parents=True)
        binary.write_bytes(b"")
        with mock.patch.object(pf, "IS_MACOS", True):
            self.assertEqual(desktop.app_command(1081, ""), ["open", "uni-vpn://open"])
            self.assertEqual(desktop.app_command(1081, "#settings"), ["open", "uni-vpn://settings"])
            self.assertEqual(desktop.app_command(1081, "&user=ab123"), ["open", "uni-vpn://open?user=ab123"])


class WindowsTests(Harness):
    def sdk(self):
        buffer = io.BytesIO()
        with zipfile.ZipFile(buffer, "w") as archive:
            for member in desktop.WEBVIEW2_FILES:
                archive.writestr(member, member.encode())
            archive.writestr("lib/net462/other.dll", b"x")
        return buffer.getvalue()

    def test_sdk_with_the_wrong_hash_is_refused(self):
        with self.assertRaises(ValueError):
            desktop.extract_webview2(self.sdk(), Path(tempfile.mkdtemp()))

    def test_sdk_files_are_extracted_flat(self):
        data = self.sdk()
        target = Path(tempfile.mkdtemp())
        with mock.patch.object(desktop, "WEBVIEW2_SHA256", hashlib.sha256(data).hexdigest()):
            desktop.extract_webview2(data, target)
        self.assertEqual(sorted(p.name for p in target.iterdir()), sorted(desktop.WEBVIEW2_FILES.values()))

    def test_shortcut_script_quotes_paths(self):
        link = Path("C:/Users/O'Brien/Uni VPN.lnk")
        script = desktop.shortcut_script(link, Path("C:/Program Files/uni-vpn/x.exe"), "--port 1081 --hidden",
                                         Path("C:/x.exe"))
        self.assertIn("'" + str(link).replace("'", "''") + "'", script)
        self.assertIn("O''Brien", script)
        self.assertIn("$s.Arguments = '--port 1081 --hidden'", script)

    def test_app_command_passes_port_and_page(self):
        exe = self.home / "desktop" / "Uni VPN.exe"
        with mock.patch.object(pf, "IS_WINDOWS", True), mock.patch.object(desktop, "windows_exe", return_value=exe):
            self.assertIsNone(desktop.app_command(1081, ""))
            exe.parent.mkdir()
            exe.write_bytes(b"")
            self.assertEqual(desktop.app_command(1081, "#settings"), [str(exe), "--port", "1081", "--page", "#settings"])


class LinuxTests(Harness):
    def test_desktop_entry_quotes_and_names_the_window_class(self):
        text = desktop.desktop_entry("/usr/bin/python3", 1081)
        self.assertIn("Exec=\"/usr/bin/python3\" ", text)
        self.assertIn("--port 1081\n", text)
        self.assertIn("StartupWMClass=de.davidvinu.UniVPN", text)
        self.assertIn("Icon=de.davidvinu.UniVPN", text)
        self.assertNotIn("--hidden", text)
        hidden = desktop.desktop_entry("/usr/bin/python3", 1081, hidden=True)
        self.assertIn("--hidden", hidden)
        self.assertIn("NoDisplay=true", hidden)
        self.assertEqual(desktop.desktop_quote('a "b" $c 100%'), '"a \\"b\\" \\$c 100%%"')

    def test_without_webkitgtk_the_entry_opens_the_browser(self):
        with mock.patch.object(desktop, "gtk_python", return_value=None):
            self.assertFalse(desktop.install(1081, False, self.created.append, run=self.run_fake))
        entry = self.home / "data" / "applications" / "uni-vpn.desktop"
        self.assertIn("Exec=xdg-open http://127.0.0.1:1081/", entry.read_text())
        self.assertEqual(self.spawns, [])

    def test_with_webkitgtk_the_app_replaces_the_old_entry_and_starts_at_login(self):
        old = self.home / "data" / "applications" / "uni-vpn.desktop"
        old.parent.mkdir(parents=True)
        old.write_text("old")
        with mock.patch.object(desktop, "gtk_python", return_value="/usr/bin/python3"), \
                mock.patch.object(desktop, "has_indicator", return_value=True):
            self.assertTrue(desktop.install(1081, False, self.created.append, run=self.run_fake))
        self.assertFalse(old.exists())
        entry = self.home / "data" / "applications" / "de.davidvinu.UniVPN.desktop"
        login = self.home / "config" / "autostart" / "de.davidvinu.UniVPN.desktop"
        icon = self.home / "data" / "icons" / "hicolor" / "scalable" / "apps" / "de.davidvinu.UniVPN.svg"
        for path in (entry, login, icon):
            self.assertTrue(path.exists(), path)
            self.assertIn(path, self.created)
        self.assertEqual(len(self.spawns), 1)
        self.assertIn("--hidden", self.spawns[0])

    def test_without_a_panel_nothing_starts_at_login(self):
        with mock.patch.object(desktop, "gtk_python", return_value="/usr/bin/python3"), \
                mock.patch.object(desktop, "has_indicator", return_value=False):
            desktop.install(1081, False, self.created.append, run=self.run_fake)
        self.assertFalse((self.home / "config" / "autostart" / "de.davidvinu.UniVPN.desktop").exists())
        self.assertEqual(self.spawns, [])


class OpenTests(Harness):
    def test_setup_prefill_becomes_a_page(self):
        self.assertEqual(desktop.page_for({"user": "ab123", "university": ""}), "&user=ab123")
        self.assertEqual(desktop.page_for({}), "")

    def test_no_desktop_no_app(self):
        with mock.patch.object(pf, "has_desktop", return_value=False):
            self.assertFalse(desktop.open_app(1081))
        self.assertEqual(self.spawns, [])

    def test_open_runs_the_app_command(self):
        with mock.patch.object(pf, "has_desktop", return_value=True), \
                mock.patch.object(desktop, "app_command", return_value=["app", "--page", "#settings"]):
            self.assertTrue(desktop.open_app(1081, "#settings"))
        self.assertEqual(self.spawns, [["app", "--page", "#settings"]])


if __name__ == "__main__":
    unittest.main()
