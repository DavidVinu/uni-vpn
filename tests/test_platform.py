import os
import stat
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from uni_vpn import platform as pf

from tests import posix_only, simulate_posix

setUpModule, tearDownModule = simulate_posix()


class PathTests(unittest.TestCase):
    def test_config_dir_honours_xdg(self):
        with mock.patch.dict(os.environ, {"XDG_CONFIG_HOME": "/tmp/xdg-test"}):
            self.assertEqual(pf.config_dir(), Path("/tmp/xdg-test/uni-vpn"))

    def test_config_dir_default(self):
        with mock.patch.dict(os.environ, {}, clear=False):
            os.environ.pop("XDG_CONFIG_HOME", None)
            self.assertEqual(pf.config_dir(), Path.home() / ".config" / "uni-vpn")

    def test_state_dir_platform(self):
        with mock.patch.object(pf, "IS_MACOS", True), mock.patch.dict(os.environ):
            os.environ.pop("XDG_STATE_HOME", None)
            self.assertEqual(pf.state_dir(), Path.home() / "Library" / "Logs" / "uni-vpn")
        with mock.patch.object(pf, "IS_MACOS", False), mock.patch.dict(os.environ, {"XDG_STATE_HOME": "/tmp/st"}):
            self.assertEqual(pf.state_dir(), Path("/tmp/st/uni-vpn"))
        # An override wins on macOS too, so a test daemon never writes into the real log folder.
        with mock.patch.object(pf, "IS_MACOS", True), mock.patch.dict(os.environ, {"XDG_STATE_HOME": "/tmp/st"}):
            self.assertEqual(pf.state_dir(), Path("/tmp/st/uni-vpn"))

    def test_repo_root_contains_bin(self):
        self.assertTrue((pf.repo_root() / "bin" / "uni-vpn").exists())


class BinaryTests(unittest.TestCase):
    def test_find_binary_override_must_be_executable(self):
        d = Path(tempfile.mkdtemp())
        exe = d / "tool"
        exe.write_text("#!/bin/sh\n")
        exe.chmod(exe.stat().st_mode | stat.S_IXUSR)
        self.assertEqual(pf.find_binary("tool", override=str(exe)), str(exe))
        self.assertIsNone(pf.find_binary("tool", override=str(d / "missing")))

    @posix_only
    def test_find_binary_searches_path(self):
        self.assertIsNotNone(pf.find_binary("sh"))
        self.assertIsNone(pf.find_binary("definitely-not-a-binary-xyz"))


class AdminOnlyBinaryTests(unittest.TestCase):
    """Windows: the elevated service must only run programs from Program Files."""

    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp())
        self.program_files = self.tmp / "Program Files"
        self.tool = self.program_files / "OpenConnect-GUI" / "openconnect"
        self.tool.parent.mkdir(parents=True)
        self.user_tool = self.tmp / "user" / "openconnect"
        self.user_tool.parent.mkdir()
        for path in (self.tool, self.user_tool):
            path.write_text("#!/bin/sh\n")
            path.chmod(0o755)
        for patcher in (mock.patch.object(pf, "IS_WINDOWS", True),
                        mock.patch.object(pf, "_PROGRAM_FILES", [str(self.program_files)]),
                        mock.patch.object(pf, "SEARCH_DIRS", [str(self.tool.parent)]),
                        mock.patch.dict(os.environ, {"PATH": str(self.user_tool.parent)})):
            patcher.start()
            self.addCleanup(patcher.stop)

    def test_path_and_user_writable_override_are_ignored(self):
        self.assertTrue(pf.admin_only(str(self.tool)))
        self.assertFalse(pf.admin_only(str(self.user_tool)))
        self.assertFalse(pf.admin_only(str(self.tmp / "Program Files Evil" / "x")))
        self.assertEqual(pf.find_binary("openconnect"), str(self.tool))
        self.assertEqual(pf.find_binary("openconnect", str(self.user_tool)), str(self.tool))
        self.assertEqual(pf.find_binary("openconnect", str(self.tool)), str(self.tool))

    def test_app_goes_to_program_files(self):
        self.assertEqual(pf.app_install_dir(), self.program_files / "uni-vpn")


class CiscoTests(unittest.TestCase):
    def test_connected_via_interface(self):
        d = Path(tempfile.mkdtemp())
        iface = d / "cscotun0"
        iface.mkdir()
        self.assertTrue(pf.cisco_connected(run=lambda *a, **k: None, cscotun=iface))

    def test_connected_via_vpn_state_on_macos(self):
        def run(cmd, **kwargs):
            return subprocess.CompletedProcess(cmd, 0, stdout="  >> state: Connected\n  >> state: Unknown\n", stderr="")

        with mock.patch.object(pf, "cisco_installed", return_value=True), mock.patch.object(pf, "IS_MACOS", True):
            self.assertTrue(pf.cisco_connected(run=run, cscotun=Path("/nonexistent/cscotun0")))

    def test_linux_never_asks_the_cisco_cli(self):
        # "vpn state" takes 2.2 s (measured 2026-09-08) and delays every connect; on Linux the
        # Cisco client always creates cscotun0 when connected, which is enough for detection.
        calls = []

        def run(cmd, **kwargs):
            calls.append(cmd)
            return subprocess.CompletedProcess(cmd, 0, stdout="  >> state: Connected\n", stderr="")

        with mock.patch.object(pf, "cisco_installed", return_value=True), mock.patch.object(pf, "IS_MACOS", False):
            self.assertFalse(pf.cisco_connected(run=run, cscotun=Path("/nonexistent/cscotun0")))
        self.assertEqual(calls, [])

    def test_not_connected(self):
        def run(cmd, **kwargs):
            return subprocess.CompletedProcess(cmd, 0, stdout="  >> state: Disconnected\n", stderr="")

        with mock.patch.object(pf, "cisco_installed", return_value=True), mock.patch.object(pf, "IS_MACOS", True):
            self.assertFalse(pf.cisco_connected(run=run, cscotun=Path("/nonexistent/cscotun0")))
        with mock.patch.object(pf, "cisco_installed", return_value=False), mock.patch.object(pf, "IS_MACOS", True):
            self.assertFalse(pf.cisco_connected(run=run, cscotun=Path("/nonexistent/cscotun0")))

    def test_vpn_state_failure_means_not_connected(self):
        def run(cmd, **kwargs):
            raise subprocess.TimeoutExpired(cmd, 5)

        with mock.patch.object(pf, "cisco_installed", return_value=True), mock.patch.object(pf, "IS_MACOS", True):
            self.assertFalse(pf.cisco_connected(run=run, cscotun=Path("/nonexistent/cscotun0")))


class OpenUrlTests(unittest.TestCase):
    def popen(self, wait):
        calls = []

        class Process:
            def __init__(self, cmd, **kwargs):
                calls.append((cmd, kwargs))

            def wait(self, timeout=None):
                return wait(timeout)

        return Process, calls

    def test_browser_started_by_xdg_open_does_not_block(self):
        # xdg-open stays in the foreground when it starts the browser itself; no pipes to inherit.
        def wait(timeout):
            raise subprocess.TimeoutExpired("xdg-open", timeout)

        popen, calls = self.popen(wait)
        with mock.patch.object(pf, "has_desktop", return_value=True), mock.patch.object(pf, "IS_MACOS", False):
            self.assertTrue(pf.open_url("http://127.0.0.1:1081/", popen=popen))
        cmd, kwargs = calls[0]
        self.assertEqual(cmd, ["xdg-open", "http://127.0.0.1:1081/"])
        self.assertEqual(kwargs["stdout"], subprocess.DEVNULL)
        self.assertEqual(kwargs["stderr"], subprocess.DEVNULL)
        self.assertTrue(kwargs["start_new_session"])

    def test_failing_launcher_is_reported(self):
        popen, _ = self.popen(lambda timeout: 3)
        with mock.patch.object(pf, "has_desktop", return_value=True):
            self.assertFalse(pf.open_url("http://127.0.0.1:1081/", popen=popen))
        popen, _ = self.popen(lambda timeout: 0)
        with mock.patch.object(pf, "has_desktop", return_value=True):
            self.assertTrue(pf.open_url("http://127.0.0.1:1081/", popen=popen))

    def test_app_install_dir_matches_get_sh(self):
        with mock.patch.object(pf, "IS_MACOS", False), mock.patch.dict(os.environ, {"XDG_DATA_HOME": "/tmp/data"}):
            self.assertEqual(pf.app_install_dir(), Path("/tmp/data/uni-vpn/app"))
        with mock.patch.object(pf, "IS_MACOS", True):
            self.assertEqual(pf.app_install_dir(), Path.home() / "Library" / "Application Support" / "uni-vpn" / "app")


class PackagedTests(unittest.TestCase):
    def setUp(self):
        self.package = Path(tempfile.mkdtemp())

    def test_packaged_needs_the_program_copy(self):
        self.assertFalse(pf.packaged(str(self.package)))
        (self.package / "app" / "uni_vpn").mkdir(parents=True)
        (self.package / "app" / "uni_vpn" / "__init__.py").write_text("")
        self.assertTrue(pf.packaged(str(self.package)))

    def test_bundled_programs_only_on_macos_for_this_processor(self):
        directory = self.package / os.uname().machine / "bin"
        directory.mkdir(parents=True)
        tool = directory / "ocproxy"
        tool.write_text("#!/bin/sh\n")
        tool.chmod(0o755)
        with mock.patch.object(pf, "IS_MACOS", False):
            self.assertIsNone(pf.bundled_bin_dir(str(self.package)))
        with mock.patch.object(pf, "IS_MACOS", True), mock.patch.object(pf, "PACKAGE_DIR", str(self.package)):
            self.assertEqual(pf.bundled_bin_dir(), str(directory))
            # Before anything on the PATH or in Homebrew.
            self.assertEqual(pf.find_binary("ocproxy"), str(tool))
            self.assertTrue(pf.is_bundled(str(tool)))
            self.assertFalse(pf.is_bundled("/opt/homebrew/bin/ocproxy"))
