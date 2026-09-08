import argparse
import os
import sys
import tempfile
import unittest
from io import StringIO
from contextlib import redirect_stdout
from pathlib import Path
from unittest import mock

from uni_vpn import doctor, service, sysproxy
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
        self.stored_totp = []
        self.installed = []
        self.keyring = {"password": "missing", "totp": "missing"}
        self.proxy_calls = []
        self.proxy_result = "ok"
        # Schutz: kein Test darf die echte Proxy-Einstellung des Rechners anfassen (passiert am
        # 2026-09-08, als ein Test setup() ohne proxy_install aufrief).
        for name in ("install", "uninstall", "refresh", "state", "current", "apply", "restore"):
            patch = mock.patch.object(sysproxy, name, side_effect=AssertionError(f"sysproxy.{name} im Test aufgerufen"))
            patch.start()
            self.addCleanup(patch.stop)

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

    def answer(self, prompt):
        return "pw" if "Passwort" in prompt else "gezd gnbv gy3t qojq gezd gnbv gy3t qojq"

    def fake_proxy_install(self, http_port):
        self.proxy_calls.append(http_port)
        return self.proxy_result

    def run_setup(self, service_install=None, run_doctor=False, port_open=lambda port: True, getpass_fn=None, **kwargs):
        out = StringIO()
        with redirect_stdout(out):
            rc = setup.setup(self.args(**kwargs), input_fn=lambda prompt: "ab123", getpass_fn=getpass_fn or self.answer,
                             service_install=service_install or self.fake_service_install,
                             store=lambda user, pw: self.stored.append((user, pw)),
                             store_totp=lambda user, token: self.stored_totp.append((user, token)),
                             keyring_probe=lambda user, kind="password": self.keyring[kind],
                             proxy_install=self.fake_proxy_install,
                             run_doctor=run_doctor, port_open=port_open)
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
        self.assertEqual(self.stored_totp, [("ab123", "base32:GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ")])
        self.assertIn("Kontrollcode", out)
        recorded = setup.recorded()
        self.assertIn(cfg, recorded)
        self.assertIn(link, recorded)
        self.assertIn(self.installed[0], recorded)
        self.assertTrue(setup.apport_ignore_path().exists())
        self.assertIn("/usr/bin/openconnect", setup.apport_ignore_path().read_text())
        self.assertEqual(self.proxy_calls, [1081])
        self.assertIn("Proxy-Regel im System eingetragen", out)
        self.assertNotIn("chrome://extensions", out)
        self.assertIn("http://127.0.0.1:1081/", out)

    def test_proxy_unavailable_prints_manual_pac_url(self):
        self.proxy_result = "unavailable"
        rc, out = self.run_setup(user="ab123")
        self.assertEqual(rc, 0)
        self.assertIn("http://127.0.0.1:1081/proxy.pac", out)
        self.assertIn("Von Hand", out)

    def test_proxy_replaced_is_mentioned(self):
        self.proxy_result = "replaced"
        rc, out = self.run_setup(user="ab123")
        self.assertEqual(rc, 0)
        self.assertIn("ersetzt", out)

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
        self.assertEqual(self.stored_totp, [])
        self.assertIn("wuerde", out)
        self.assertIn("TOTP", out)
        self.assertEqual(self.proxy_calls, [])
        self.assertIn("Proxy", out)

    def test_totp_asked_alone_when_password_present(self):
        self.keyring["password"] = "present"
        prompts = []

        def ask(prompt):
            prompts.append(prompt)
            return self.answer(prompt)

        rc, out = self.run_setup(user="ab123", getpass_fn=ask)
        self.assertEqual(rc, 0, out)
        self.assertEqual(self.stored, [])
        self.assertEqual(len(prompts), 1)
        self.assertIn("TOTP", prompts[0])
        self.assertEqual(self.stored_totp, [("ab123", "base32:GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ")])

    def test_totp_hint_names_portal_before_asking(self):
        rc, out = self.run_setup(user="ab123")
        self.assertEqual(rc, 0)
        self.assertIn("mfa.uni-heidelberg.de", out)
        self.assertLess(out.index("mfa.uni-heidelberg.de"), out.index("Kontrollcode"))

    def test_invalid_totp_input_is_reported_and_setup_continues(self):
        rc, out = self.run_setup(user="ab123", getpass_fn=lambda p: "pw" if "Passwort" in p else "0189")
        self.assertEqual(rc, 0, out)
        self.assertEqual(self.stored_totp, [])
        self.assertIn("Base32", out)
        self.assertIn("uni-vpn totp", out)

    def test_empty_totp_input_hints_at_later_command(self):
        rc, out = self.run_setup(user="ab123", getpass_fn=lambda p: "pw" if "Passwort" in p else "")
        self.assertEqual(rc, 0, out)
        self.assertEqual(self.stored_totp, [])
        self.assertIn("uni-vpn totp", out)

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

    def test_service_load_failure_reports_and_keeps_records(self):
        unit = self.home / ".config" / "systemd" / "user" / "uni-vpn.service"

        def broken_install(dry_run=False, run=None):
            unit.parent.mkdir(parents=True, exist_ok=True)
            unit.write_text("unit")
            raise service.ServiceError("Failed to connect to bus: No medium found", files=[unit])

        rc, out = self.run_setup(service_install=broken_install)
        self.assertEqual(rc, 1)
        self.assertIn("Dienst konnte nicht geladen werden: Failed to connect to bus", out)
        self.assertNotIn("Traceback", out)
        recorded = setup.recorded()
        self.assertIn(self.home / ".config" / "uni-vpn" / "config.toml", recorded)
        self.assertIn(self.home / ".local" / "bin" / "uni-vpn", recorded)
        self.assertIn(setup.apport_ignore_path(), recorded)
        self.assertIn(unit, recorded)

    def test_service_failure_without_files_still_reports(self):
        def broken_install(dry_run=False, run=None):
            raise service.ServiceError("Bootstrap failed: 5: Input/output error")

        rc, out = self.run_setup(service_install=broken_install)
        self.assertEqual(rc, 1)
        self.assertIn("Dienst konnte nicht geladen werden: Bootstrap failed", out)

    def test_setup_waits_for_http_port_before_doctor(self):
        seen = []
        answers = iter([False, False, True])

        def port_open(port):
            seen.append(port)
            return next(answers)

        with mock.patch.object(doctor, "run_checks", return_value=[]) as checks, \
             mock.patch.object(setup.time, "sleep") as sleep:
            rc, _ = self.run_setup(user="ab123", run_doctor=True, port_open=port_open)
        self.assertEqual(rc, 0)
        self.assertEqual(seen, [1081, 1081, 1081])
        self.assertEqual(sleep.call_count, 2)
        sleep.assert_called_with(0.25)
        checks.assert_called_once()

    def test_wait_for_port_gives_up_after_timeout(self):
        # Start 0.0, Frist 5.0: nach 1.0, 2.0 und 4.0 wird geschlafen, bei 5.5 aufgegeben.
        clock = iter([0.0, 1.0, 2.0, 4.0, 5.5])
        sleeps = []
        ready = setup.wait_for_port(1081, port_open=lambda port: False, timeout=5, step=0.25,
                                    sleep=sleeps.append, clock=lambda: next(clock))
        self.assertFalse(ready)
        self.assertEqual(sleeps, [0.25] * 3)

    def test_existing_secrets_not_asked_again(self):
        out = StringIO()
        with redirect_stdout(out):
            rc = setup.setup(self.args(user="ab123"), input_fn=lambda p: "x", getpass_fn=lambda p: (_ for _ in ()).throw(AssertionError("darf nicht fragen")),
                             service_install=self.fake_service_install, store=self.stored.append,
                             store_totp=self.stored_totp.append, proxy_install=self.fake_proxy_install,
                             keyring_probe=lambda user, kind="password": "present", run_doctor=False)
        self.assertEqual(rc, 0)
        self.assertIn("bereits im Keyring", out.getvalue())


class UninstallTests(SetupHarness):
    def test_uninstall_removes_recorded_files(self):
        self.run_setup()
        deleted = []
        deleted_totp = []
        removed_service = []
        proxy_restored = []
        out = StringIO()
        with redirect_stdout(out):
            rc = setup.uninstall(self.args(yes=True), input_fn=lambda p: "j",
                                 service_uninstall=lambda run=None: removed_service.append(True),
                                 delete=lambda user: deleted.append(user) or True,
                                 delete_totp=lambda user: deleted_totp.append(user) or True,
                                 proxy_uninstall=lambda: proxy_restored.append(True) or True)
        self.assertEqual(rc, 0)
        self.assertEqual(removed_service, [True])
        self.assertEqual(deleted, ["ab123"])
        self.assertEqual(deleted_totp, ["ab123"])
        self.assertEqual(proxy_restored, [True])
        self.assertIn("Proxy-Einstellung wiederhergestellt", out.getvalue())
        self.assertNotIn("Extension", out.getvalue())
        self.assertFalse((self.home / ".config" / "uni-vpn" / "config.toml").exists())
        self.assertFalse((self.home / ".local" / "bin" / "uni-vpn").exists())
        self.assertFalse((self.home / ".config" / "uni-vpn" / setup.INSTALLED_FILES).exists())

    def test_uninstall_keeps_keyring_when_declined(self):
        self.run_setup()
        deleted = []
        with redirect_stdout(StringIO()):
            setup.uninstall(self.args(), input_fn=lambda p: "n", service_uninstall=lambda run=None: None,
                            delete=lambda user: deleted.append(user) or True,
                            delete_totp=lambda user: deleted.append(("totp", user)) or True,
                            proxy_uninstall=lambda: True)
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
