import argparse
import os
import sys
import tempfile
import unittest
from io import StringIO
from contextlib import redirect_stdout
from pathlib import Path
from unittest import mock

from uni_vpn import platform as pf
from uni_vpn import setup


class SetupHarness(unittest.TestCase):
    def setUp(self):
        self.home = Path(tempfile.mkdtemp())
        self.env = mock.patch.dict(os.environ, {"HOME": str(self.home), "XDG_CONFIG_HOME": str(self.home / ".config"),
                                                "XDG_STATE_HOME": str(self.home / ".local" / "state")})
        self.env.start()
        self.home_patch = mock.patch.object(Path, "home", return_value=self.home)
        self.home_patch.start()
        self.macos = mock.patch.object(pf, "IS_MACOS", False)
        self.macos.start()
        self.find = mock.patch.object(pf, "find_binary", lambda name, override=None: f"/usr/bin/{name}")
        self.find.start()
        self.stored = []
        self.installed = []

    def tearDown(self):
        for p in (self.find, self.macos, self.home_patch, self.env):
            p.stop()

    def fake_service_install(self, dry_run=False, run=None):
        target = self.home / ".config" / "systemd" / "user" / "uni-vpn.service"
        if not dry_run:
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text("unit")
        self.installed.append(target)
        return [target]

    def args(self, **kwargs):
        base = {"dry_run": False, "user": None, "yes": False}
        base.update(kwargs)
        return argparse.Namespace(**base)

    def run_setup(self, **kwargs):
        out = StringIO()
        with redirect_stdout(out):
            rc = setup.setup(self.args(**kwargs), input_fn=lambda prompt: "ab123", getpass_fn=lambda prompt: "pw",
                             service_install=self.fake_service_install,
                             store=lambda user, pw: self.stored.append((user, pw)),
                             keyring_probe=lambda user: "missing", run_doctor=False)
        return rc, out.getvalue()


class SetupTests(SetupHarness):
    def test_setup_creates_everything(self):
        rc, out = self.run_setup()
        self.assertEqual(rc, 0, out)
        cfg = self.home / ".config" / "uni-vpn" / "config.toml"
        self.assertTrue(cfg.exists())
        self.assertIn('user = "ab123"', cfg.read_text())
        link = self.home / ".local" / "bin" / "uni-vpn"
        self.assertTrue(link.exists())
        self.assertIn(sys.executable, link.read_text())
        self.assertIn("bin/uni-vpn", link.read_text())
        self.assertTrue(os.access(link, os.X_OK))
        self.assertEqual(self.stored, [("ab123", "pw")])
        recorded = setup.recorded()
        self.assertIn(cfg, recorded)
        self.assertIn(link, recorded)
        self.assertIn(self.installed[0], recorded)
        self.assertTrue(setup.apport_ignore_path().exists())
        self.assertIn("/usr/bin/openconnect", setup.apport_ignore_path().read_text())

    def test_setup_is_idempotent(self):
        self.run_setup()
        rc, _ = self.run_setup()
        self.assertEqual(rc, 0)
        self.assertEqual(len(setup.recorded()), len(set(setup.recorded())))

    def test_dry_run_changes_nothing(self):
        rc, out = self.run_setup(dry_run=True, user="ab123")
        self.assertEqual(rc, 0)
        self.assertFalse((self.home / ".config" / "uni-vpn").exists())
        self.assertEqual(self.stored, [])
        self.assertIn("wuerde", out)

    def test_dry_run_tolerates_missing_binaries(self):
        with mock.patch.object(pf, "find_binary", lambda name, override=None: None):
            rc, out = self.run_setup(dry_run=True, user="ab123")
        self.assertEqual(rc, 0)
        self.assertIn("voraussetzen", out)

    def test_missing_binary_aborts(self):
        with mock.patch.object(pf, "find_binary", lambda name, override=None: None):
            rc, out = self.run_setup()
        self.assertEqual(rc, 1)
        self.assertIn("openconnect", out)

    def test_existing_password_not_asked_again(self):
        out = StringIO()
        with redirect_stdout(out):
            rc = setup.setup(self.args(user="ab123"), input_fn=lambda p: "x", getpass_fn=lambda p: (_ for _ in ()).throw(AssertionError("darf nicht fragen")),
                             service_install=self.fake_service_install, store=self.stored.append,
                             keyring_probe=lambda user: "present", run_doctor=False)
        self.assertEqual(rc, 0)


class UninstallTests(SetupHarness):
    def test_uninstall_removes_recorded_files(self):
        self.run_setup()
        deleted = []
        removed_service = []
        out = StringIO()
        with redirect_stdout(out):
            rc = setup.uninstall(self.args(yes=True), input_fn=lambda p: "j",
                                 service_uninstall=lambda run=None: removed_service.append(True),
                                 delete=lambda user: deleted.append(user) or True)
        self.assertEqual(rc, 0)
        self.assertEqual(removed_service, [True])
        self.assertEqual(deleted, ["ab123"])
        self.assertFalse((self.home / ".config" / "uni-vpn" / "config.toml").exists())
        self.assertFalse((self.home / ".local" / "bin" / "uni-vpn").exists())
        self.assertFalse((self.home / ".config" / "uni-vpn" / setup.INSTALLED_FILES).exists())
        self.assertIn("Extension", out.getvalue())

    def test_uninstall_keeps_keyring_when_declined(self):
        self.run_setup()
        deleted = []
        with redirect_stdout(StringIO()):
            setup.uninstall(self.args(), input_fn=lambda p: "n", service_uninstall=lambda run=None: None,
                            delete=lambda user: deleted.append(user) or True)
        self.assertEqual(deleted, [])


class ApportTests(SetupHarness):
    def test_merges_into_existing_file(self):
        path = setup.apport_ignore_path()
        path.write_text('<?xml version="1.0"?>\n<apport><ignore program="/usr/bin/other" mtime="1"/></apport>\n')
        setup.ensure_apport_ignore("/bin/sh")
        text = path.read_text()
        self.assertIn("/usr/bin/other", text)
        self.assertIn("/bin/sh", text)
        setup.ensure_apport_ignore("/bin/sh")
        self.assertEqual(text.count("/bin/sh"), path.read_text().count("/bin/sh"))
