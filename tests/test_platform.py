import os
import stat
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from uni_vpn import platform as pf


class PathTests(unittest.TestCase):
    def test_config_dir_honours_xdg(self):
        with mock.patch.dict(os.environ, {"XDG_CONFIG_HOME": "/tmp/xdg-test"}):
            self.assertEqual(pf.config_dir(), Path("/tmp/xdg-test/uni-vpn"))

    def test_config_dir_default(self):
        with mock.patch.dict(os.environ, {}, clear=False):
            os.environ.pop("XDG_CONFIG_HOME", None)
            self.assertEqual(pf.config_dir(), Path.home() / ".config" / "uni-vpn")

    def test_state_dir_platform(self):
        with mock.patch.object(pf, "IS_MACOS", True):
            self.assertEqual(pf.state_dir(), Path.home() / "Library" / "Logs" / "uni-vpn")
        with mock.patch.object(pf, "IS_MACOS", False), mock.patch.dict(os.environ, {"XDG_STATE_HOME": "/tmp/st"}):
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

    def test_find_binary_searches_path(self):
        self.assertIsNotNone(pf.find_binary("sh"))
        self.assertIsNone(pf.find_binary("definitely-not-a-binary-xyz"))


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
        # "vpn state" braucht 2,2 s (gemessen 2026-09-08) und verzoegert jeden Aufbau; auf Linux
        # legt der Cisco-Client bei Verbindung immer cscotun0 an, das reicht als Erkennung.
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
