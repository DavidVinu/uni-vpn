import argparse
import io
import os
import shutil
import subprocess
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
        for name, value in (("IS_WINDOWS", False), ("has_desktop", lambda: False),
                            ("open_url", mock.Mock(side_effect=AssertionError("browser opened in a test")))):
            patch = mock.patch.object(pf, name, value)
            patch.start()
            self.addCleanup(patch.stop)
        self.stored = []
        self.stored_totp = []
        self.installed = []
        self.keyring = {"password": "missing", "totp": "missing"}
        self.proxy_calls = []
        self.proxy_result = "ok"
        # Guard: no test may touch the machine's real proxy setting (this happened on
        # 2026-09-08, when a test called setup() without proxy_install).
        for name in ("install", "uninstall", "refresh", "state", "current", "apply", "restore"):
            patch = mock.patch.object(sysproxy, name, side_effect=AssertionError(f"sysproxy.{name} called in a test"))
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
        return "pw" if "password" in prompt else "gezd gnbv gy3t qojq gezd gnbv gy3t qojq"

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
        self.assertIn(os.path.join("bin", "uni-vpn"), link.read_text())
        self.assertTrue(os.access(link, os.X_OK))
        self.assertEqual(self.stored, [("ab123", "pw")])
        self.assertEqual(self.stored_totp, [("ab123", "base32:GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ")])
        self.assertIn("Check code", out)
        recorded = setup.recorded()
        self.assertIn(cfg, recorded)
        self.assertIn(link, recorded)
        self.assertIn(self.installed[0], recorded)
        self.assertTrue(setup.apport_ignore_path().exists())
        self.assertIn("/usr/bin/openconnect", setup.apport_ignore_path().read_text())
        self.assertEqual(self.proxy_calls, [1081])
        self.assertIn("Proxy rule registered with the system", out)
        self.assertNotIn("chrome://extensions", out)
        self.assertIn("http://127.0.0.1:1081/", out)

    def test_invalid_config_does_not_stop_an_update(self):
        cfg = self.home / ".config" / "uni-vpn" / "config.toml"
        cfg.parent.mkdir(parents=True)
        cfg.write_text('user = "ab123"\nsocks_port = "x"\n')
        rc, out = self.run_setup(user="cd456")
        self.assertEqual(rc, 0, out)
        self.assertIn("invalid", out)
        self.assertEqual(len(self.installed), 1)
        self.assertEqual(self.proxy_calls, [1081])
        self.assertEqual(self.stored, [])
        self.assertIn('socks_port = "x"', cfg.read_text())

    def test_invalid_config_keeps_the_ports_the_daemon_uses(self):
        cfg = self.home / ".config" / "uni-vpn" / "config.toml"
        cfg.parent.mkdir(parents=True)
        cfg.write_text('user = "ab123"\nhttp_port = 1091\nsocks_port = 1090\nidle_minutes = "x"\n')
        rc, out = self.run_setup()
        self.assertEqual(rc, 0, out)
        self.assertEqual(self.proxy_calls, [1091])

    def test_proxy_unavailable_prints_manual_pac_url(self):
        self.proxy_result = "unavailable"
        rc, out = self.run_setup(user="ab123")
        self.assertEqual(rc, 0)
        self.assertIn("http://127.0.0.1:1081/proxy.pac", out)
        self.assertIn("by hand", out)

    def test_proxy_replaced_is_mentioned(self):
        self.proxy_result = "replaced"
        rc, out = self.run_setup(user="ab123")
        self.assertEqual(rc, 0)
        self.assertIn("replaced", out)

    def test_failed_proxy_registration_is_not_reported_as_success(self):
        self.proxy_result = "failed"
        rc, out = self.run_setup(user="ab123")
        self.assertEqual(rc, 0)
        self.assertNotIn("Proxy rule registered", out)
        self.assertIn("http://127.0.0.1:1081/proxy.pac", out)

    def test_user_option_on_a_rerun_changes_the_university_id(self):
        self.run_setup(user="ab123")
        rc, out = self.run_setup(user="cd456")
        self.assertEqual(rc, 0, out)
        self.assertEqual(setup.config.load(setup.config.default_path()).user, "cd456")

    def test_path_hint_on_linux_mentions_logging_in_again(self):
        with mock.patch.dict(os.environ, {"PATH": "/usr/bin"}):
            _, out = self.run_setup(user="ab123")
        self.assertIn("logging out and in", out)

    def test_macos_command_goes_to_homebrew_bin_and_uninstall_removes_it(self):
        brew_bin = self.home / "homebrew" / "bin"
        brew_bin.mkdir(parents=True)
        with mock.patch.object(pf, "IS_MACOS", True), \
                mock.patch.object(setup, "MACOS_BIN_DIRS", (str(self.home / "missing"), str(brew_bin))):
            self.assertEqual(setup.command_path(), brew_bin / "uni-vpn")
            with redirect_stdout(StringIO()):
                setup.install_command(False, lambda path: setup.record([path]))
        self.assertTrue((brew_bin / "uni-vpn").exists())
        self.assertIn(brew_bin / "uni-vpn", setup.recorded())
        with redirect_stdout(StringIO()):
            setup.uninstall(self.args(), input_fn=lambda p: "n", service_uninstall=lambda run=None: True,
                            proxy_uninstall=lambda: "unset")
        self.assertFalse((brew_bin / "uni-vpn").exists())

    def test_windows_wrapper_uses_the_oem_code_page(self):
        text = '@echo off\r\n"C:\\Users\\J\u00fcrgen\\python.exe" "x" %*\r\n'
        self.assertEqual(setup.cmd_bytes(text, codec="cp850"), text.encode("cp850"))
        fallback = setup.cmd_bytes(text, codec="ascii")
        self.assertTrue(fallback.startswith(b"@chcp 65001 >nul\r\n@echo off"))
        self.assertIn("J\u00fcrgen".encode("utf-8"), fallback)

    def test_no_terminal_is_a_clear_error(self):
        from uni_vpn import cli

        out = StringIO()
        with redirect_stdout(out), mock.patch.object(setup, "setup", side_effect=EOFError):
            rc = cli.main(["setup"])
        self.assertEqual(rc, 1)
        self.assertIn("No terminal", out.getvalue())

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
        self.assertIn("would", out)
        self.assertIn("TOTP", out)
        self.assertEqual(self.proxy_calls, [])
        self.assertIn("proxy rule", out)

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
        self.assertLess(out.index("mfa.uni-heidelberg.de"), out.index("Check code"))

    def test_invalid_totp_input_is_reported_and_setup_continues(self):
        rc, out = self.run_setup(user="ab123", getpass_fn=lambda p: "pw" if "password" in p else "0189")
        self.assertEqual(rc, 0, out)
        self.assertEqual(self.stored_totp, [])
        self.assertIn("That is not the secret", out)
        self.assertIn("uni-vpn totp", out)

    def test_empty_totp_input_hints_at_later_command(self):
        rc, out = self.run_setup(user="ab123", getpass_fn=lambda p: "pw" if "password" in p else "")
        self.assertEqual(rc, 0, out)
        self.assertEqual(self.stored_totp, [])
        self.assertIn("uni-vpn totp", out)

    def test_dry_run_tolerates_missing_binaries(self):
        with mock.patch.object(pf, "find_binary", lambda name, override=None: None):
            rc, out = self.run_setup(dry_run=True, user="ab123")
        self.assertEqual(rc, 0)
        self.assertIn("would require", out)

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
        self.assertIn("Service could not be loaded: Failed to connect to bus", out)
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
        self.assertIn("Service could not be loaded: Bootstrap failed", out)

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
        # Start 0.0, deadline 5.0: sleeps after 1.0, 2.0 and 4.0, gives up at 5.5.
        clock = iter([0.0, 1.0, 2.0, 4.0, 5.5])
        sleeps = []
        ready = setup.wait_for_port(1081, port_open=lambda port: False, timeout=5, step=0.25,
                                    sleep=sleeps.append, clock=lambda: next(clock))
        self.assertFalse(ready)
        self.assertEqual(sleeps, [0.25] * 3)

    def test_existing_secrets_not_asked_again(self):
        out = StringIO()
        with redirect_stdout(out):
            rc = setup.setup(self.args(user="ab123"), input_fn=lambda p: "x", getpass_fn=lambda p: (_ for _ in ()).throw(AssertionError("must not ask")),
                             service_install=self.fake_service_install, store=self.stored.append,
                             store_totp=self.stored_totp.append, proxy_install=self.fake_proxy_install,
                             keyring_probe=lambda user, kind="password": "present", run_doctor=False)
        self.assertEqual(rc, 0)
        self.assertIn("already in the keyring", out.getvalue())


class GuiSetupTests(SetupHarness):
    def run_gui(self, opened=True, **kwargs):
        self.urls = []

        def open_url(url):
            self.urls.append(url)
            return opened

        out = StringIO()
        with redirect_stdout(out):
            rc = setup.setup(self.args(**kwargs), input_fn=lambda p: (_ for _ in ()).throw(AssertionError("must not ask")),
                             getpass_fn=lambda p: (_ for _ in ()).throw(AssertionError("must not ask")),
                             service_install=self.fake_service_install, store=self.stored.append,
                             store_totp=self.stored_totp.append, proxy_install=self.fake_proxy_install,
                             keyring_probe=lambda user, kind="password": "missing", run_doctor=False,
                             port_open=lambda port: True, open_url=open_url, has_desktop=lambda: True)
        return rc, out.getvalue()

    def test_desktop_setup_asks_nothing_and_opens_the_assistant(self):
        rc, out = self.run_gui()
        self.assertEqual(rc, 0, out)
        self.assertEqual(self.urls, ["http://127.0.0.1:1081/"])
        self.assertFalse((self.home / ".config" / "uni-vpn" / "config.toml").exists())
        self.assertEqual(self.proxy_calls, [1081])
        self.assertEqual(self.stored, [])
        desktop = self.home / ".local" / "share" / "applications" / "uni-vpn.desktop"
        self.assertIn("Exec=xdg-open http://127.0.0.1:1081/", desktop.read_text())
        recorded = setup.recorded()
        self.assertIn(desktop, recorded)
        self.assertIn(self.home / ".config" / "uni-vpn" / "config.toml", recorded)

    def test_given_id_is_prefilled_in_the_assistant(self):
        rc, out = self.run_gui(user="ab123")
        self.assertEqual(rc, 0, out)
        self.assertEqual(self.urls, ["http://127.0.0.1:1081/?user=ab123"])
        self.assertFalse((self.home / ".config" / "uni-vpn" / "config.toml").exists())

    def test_without_a_browser_it_says_where_to_go(self):
        rc, out = self.run_gui(opened=False)
        self.assertEqual(rc, 0)
        self.assertIn("Open http://127.0.0.1:1081/ in a browser", out)

    def test_no_gui_flag_asks_in_the_terminal(self):
        out = StringIO()
        with redirect_stdout(out):
            rc = setup.setup(self.args(no_gui=True), input_fn=lambda p: "ab123", getpass_fn=self.answer,
                             service_install=self.fake_service_install,
                             store=lambda user, pw: self.stored.append((user, pw)),
                             store_totp=lambda user, token: self.stored_totp.append((user, token)),
                             keyring_probe=lambda user, kind="password": "missing", proxy_install=self.fake_proxy_install,
                             run_doctor=False, port_open=lambda port: True, has_desktop=lambda: True)
        self.assertEqual(rc, 0, out.getvalue())
        self.assertEqual(self.stored, [("ab123", "pw")])

    def test_service_that_never_answers_is_reported_instead_of_opening_the_browser(self):
        out = StringIO()
        with redirect_stdout(out), mock.patch.object(setup, "wait_for_port", return_value=False), \
                mock.patch.object(doctor, "run_checks", return_value=[doctor.Check("Service", "fail", "not running")]):
            rc = setup.setup(self.args(), service_install=self.fake_service_install, proxy_install=self.fake_proxy_install,
                             run_doctor=True, port_open=lambda port: False,
                             open_url=mock.Mock(side_effect=AssertionError("browser opened")), has_desktop=lambda: True)
        self.assertEqual(rc, 1)
        self.assertIn("uni-vpn log", out.getvalue())
        self.assertIn("[!!] Service: not running", out.getvalue())

    def test_invalid_university_id_is_refused(self):
        rc, out = self.run_setup(user='ab"1')
        self.assertEqual(rc, 1)
        self.assertIn("Not a university ID", out)


class UninstallTests(SetupHarness):
    def test_uninstall_removes_recorded_files(self):
        self.run_setup()
        deleted = []
        deleted_totp = []
        removed_service = []
        proxy_restored = []
        out = StringIO()
        with redirect_stdout(out):
            rc = setup.uninstall(self.args(yes=True), input_fn=lambda p: "y",
                                 service_uninstall=lambda run=None: removed_service.append(True),
                                 delete=lambda user: deleted.append(user) or True,
                                 delete_totp=lambda user: deleted_totp.append(user) or True,
                                 proxy_uninstall=lambda: proxy_restored.append(True) or "restored")
        self.assertEqual(rc, 0)
        self.assertEqual(removed_service, [True])
        self.assertEqual(deleted, ["ab123"])
        self.assertEqual(deleted_totp, ["ab123"])
        self.assertEqual(proxy_restored, [True])
        self.assertIn("Proxy setting restored", out.getvalue())
        self.assertNotIn("Extension", out.getvalue())
        self.assertFalse((self.home / ".config" / "uni-vpn" / "config.toml").exists())
        self.assertFalse((self.home / ".local" / "bin" / "uni-vpn").exists())
        self.assertFalse((self.home / ".config" / "uni-vpn" / setup.INSTALLED_FILES).exists())

    def test_windows_uninstall_without_admin_rights_changes_nothing(self):
        self.run_setup()
        from uni_vpn import windows

        out = StringIO()
        with redirect_stdout(out), mock.patch.object(pf, "IS_WINDOWS", True), \
                mock.patch.object(windows, "is_admin", return_value=False):
            rc = setup.uninstall(self.args(yes=True), input_fn=lambda p: "y",
                                 service_uninstall=mock.Mock(side_effect=AssertionError("service removed")),
                                 proxy_uninstall=mock.Mock(side_effect=AssertionError("proxy restored")))
        self.assertEqual(rc, 1)
        self.assertIn("install.ps1", out.getvalue())
        self.assertTrue((self.home / ".config" / "uni-vpn" / "config.toml").exists())

    def test_uninstall_keeps_keyring_when_declined(self):
        self.run_setup()
        deleted = []
        with redirect_stdout(StringIO()):
            setup.uninstall(self.args(), input_fn=lambda p: "n", service_uninstall=lambda run=None: None,
                            delete=lambda user: deleted.append(user) or True,
                            delete_totp=lambda user: deleted.append(("totp", user)) or True,
                            proxy_uninstall=lambda: "restored")
        self.assertEqual(deleted, [])

    def uninstall(self, input_fn=lambda p: "n", service_result=True, proxy_result="restored", **kwargs):
        out = StringIO()
        with redirect_stdout(out):
            rc = setup.uninstall(self.args(**kwargs), input_fn=input_fn, service_uninstall=lambda run=None: service_result,
                                 delete=lambda user: True, delete_totp=lambda user: True,
                                 proxy_uninstall=lambda: proxy_result)
        return rc, out.getvalue()

    def test_keyring_question_without_terminal_means_no(self):
        self.run_setup()

        def no_terminal(prompt):
            raise EOFError

        rc, out = self.uninstall(input_fn=no_terminal)
        self.assertEqual(rc, 0, out)
        self.assertNotIn("Password deleted", out)

    def test_failures_are_reported_not_hidden(self):
        self.run_setup()
        rc, out = self.uninstall(service_result=False, proxy_result="failed")
        self.assertEqual(rc, 1)
        self.assertIn("Service could not be removed", out)
        self.assertNotIn("Service removed", out)
        self.assertIn("Proxy setting could not be restored", out)
        self.assertNotIn("Proxy setting restored", out)

    def test_locked_daemon_lock_does_not_crash(self):
        self.run_setup()
        lock = pf.config_dir() / "daemon.lock"
        lock.write_text("")
        real_unlink = Path.unlink

        def unlink(path, missing_ok=False):
            if path == lock:
                raise PermissionError(13, "The process cannot access the file")
            return real_unlink(path, missing_ok=missing_ok)

        with mock.patch.object(Path, "unlink", unlink):
            rc, out = self.uninstall()
        self.assertEqual(rc, 0, out)

    def get_sh_app(self, git=False):
        app = self.home / ".local" / "share" / "uni-vpn" / "app"
        (app / "uni_vpn").mkdir(parents=True)
        (app / "uni_vpn" / "__init__.py").write_text("")
        if git:
            (app / ".git").mkdir()
        patch = mock.patch.object(pf, "repo_root", return_value=app.resolve())
        patch.start()
        self.addCleanup(patch.stop)
        os.environ.pop("XDG_DATA_HOME", None)  # restored by the harness
        return app

    def test_removes_the_get_sh_copy(self):
        self.run_setup()
        app = self.get_sh_app()
        rc, out = self.uninstall()
        self.assertEqual(rc, 0, out)
        self.assertFalse(app.exists())
        self.assertFalse(app.parent.exists())

    def test_keeps_a_git_clone_and_the_copy_after_a_failure(self):
        self.run_setup()
        app = self.get_sh_app(git=True)
        self.uninstall()
        self.assertTrue(app.exists())
        shutil.rmtree(app / ".git")
        rc, _ = self.uninstall(service_result=False)
        self.assertEqual(rc, 1)
        self.assertTrue(app.exists())


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


class UpdateTests(unittest.TestCase):
    def setUp(self):
        # Windows updates through get.ps1 (own test below); the rest is the POSIX path.
        patcher = mock.patch.object(pf, "IS_WINDOWS", False)
        patcher.start()
        self.addCleanup(patcher.stop)

    def archive(self, files):
        import zipfile

        buf = io.BytesIO()
        with zipfile.ZipFile(buf, "w") as zf:
            zf.writestr("uni-vpn-main/", "")
            for name, data in files.items():
                zf.writestr("uni-vpn-main/" + name, data)
        return self.opener(buf.getvalue())

    def opener(self, payload):
        class Response(io.BytesIO):
            def __enter__(self):
                return self

            def __exit__(self, *exc):
                return False

        return lambda url, timeout=None: Response(payload)

    def test_download_overwrites_program_files(self):
        target = Path(tempfile.mkdtemp())
        (target / "uni_vpn").mkdir()
        (target / "uni_vpn" / "__init__.py").write_text("old")
        (target / "keep.txt").write_text("mine")
        opener = self.archive({"uni_vpn/__init__.py": "new", "bin/uni-vpn": "#!/bin/sh\n"})
        setup.download_release(target, opener=opener)
        self.assertEqual((target / "uni_vpn" / "__init__.py").read_text(), "new")
        self.assertEqual((target / "keep.txt").read_text(), "mine")
        if os.name == "posix":
            self.assertTrue(os.access(target / "bin" / "uni-vpn", os.X_OK))

    def test_download_refuses_foreign_or_escaping_archives(self):
        target = Path(tempfile.mkdtemp()) / "app"
        target.mkdir()
        with self.assertRaises(ValueError):
            setup.download_release(target, opener=self.archive({"README.md": "x"}))
        with self.assertRaises(ValueError):
            setup.download_release(target, opener=self.archive({"uni_vpn/__init__.py": "x", "../evil": "x"}))
        self.assertFalse((target.parent / "evil").exists())

    def files(self, root):
        return {p.relative_to(root).as_posix(): p.read_text() for p in root.rglob("*") if p.is_file() and not p.is_symlink()}

    def test_broken_downloads_are_reported_and_change_nothing(self):
        import zipfile

        target = Path(tempfile.mkdtemp()) / "app"
        (target / "uni_vpn").mkdir(parents=True)
        (target / "uni_vpn" / "__init__.py").write_text("old")
        (target / "uni_vpn" / "b.py").write_text("old")
        empty = io.BytesIO()
        zipfile.ZipFile(empty, "w").close()
        good = self.archive({"uni_vpn/__init__.py": "new", "uni_vpn/b.py": "new-content-2"})(None).getvalue()
        damaged = good.replace(b"new-content-2", b"bad-content-2")  # stored, so the CRC no longer matches
        self.assertNotEqual(good, damaged)
        for payload in (b"not a zip", empty.getvalue(), damaged):
            with self.assertRaises(ValueError):
                setup.download_release(target, opener=self.opener(payload))
            self.assertEqual(self.files(target), {"uni_vpn/__init__.py": "old", "uni_vpn/b.py": "old"})
        self.assertEqual(sorted(p.name for p in target.parent.iterdir()), ["app"])

    def test_files_removed_upstream_are_deleted_only_under_uni_vpn_and_bin(self):
        base = Path(tempfile.mkdtemp())
        target = base / "app"
        for name in ("uni_vpn/__init__.py", "uni_vpn/gone.py", "uni_vpn/sub/gone.py", "uni_vpn/__pycache__/x.pyc",
                     "bin/old-tool", "docs/notes.md", "config.local"):
            (target / name).parent.mkdir(parents=True, exist_ok=True)
            (target / name).write_text("old")
        outside = base / "outside.py"
        outside.write_text("mine")
        if os.name == "posix":
            (target / "uni_vpn" / "link.py").symlink_to(outside)
        setup.download_release(target, opener=self.archive({"uni_vpn/__init__.py": "new", "bin/uni-vpn": "#!/bin/sh\n"}))
        self.assertEqual(self.files(target), {"uni_vpn/__init__.py": "new", "uni_vpn/__pycache__/x.pyc": "old",
                                              "bin/uni-vpn": "#!/bin/sh\n", "docs/notes.md": "old", "config.local": "old"})
        self.assertFalse((target / "uni_vpn" / "sub").exists())
        self.assertEqual(outside.read_text(), "mine")
        if os.name == "posix":
            self.assertTrue((target / "uni_vpn" / "link.py").is_symlink())

    def test_update_reports_a_broken_download(self):
        root = Path(tempfile.mkdtemp())
        out = StringIO()
        with mock.patch.object(pf, "repo_root", return_value=root), redirect_stdout(out), \
                mock.patch.object(setup.service, "control", side_effect=AssertionError("restarted")):
            rc = setup.update(argparse.Namespace(dry_run=False),
                              download=lambda target: setup.download_release(target, opener=self.opener(b"junk")))
        self.assertEqual(rc, 1)
        self.assertIn("Update failed", out.getvalue())

    def test_failed_restart_is_not_reported_as_success(self):
        root = Path(tempfile.mkdtemp())
        out = StringIO()
        with mock.patch.object(pf, "repo_root", return_value=root), redirect_stdout(out), \
                mock.patch.object(setup.service, "control", lambda action, run=None: 5):
            rc = setup.update(argparse.Namespace(dry_run=False), download=lambda target: None)
        self.assertEqual(rc, 5)
        self.assertNotIn("Service restarted", out.getvalue())
        self.assertIn("failed", out.getvalue())

    def test_update_on_windows_goes_through_get_ps1(self):
        calls = []

        def run(cmd, **kwargs):
            calls.append((cmd, kwargs.get("env", {}).get("UNI_VPN_UPDATE")))
            return subprocess.CompletedProcess(cmd, 0)

        with mock.patch.object(pf, "IS_WINDOWS", True):
            rc = setup.update(argparse.Namespace(dry_run=False), run=run,
                              download=lambda target: self.fail("Program Files needs administrator rights"))
        self.assertEqual(rc, 0)
        self.assertEqual(calls[0][0][0], "powershell")
        self.assertIn(setup.GET_PS1_URL, calls[0][0][-1])
        self.assertEqual(calls[0][1], "1")

    def test_update_without_git_downloads_and_restarts(self):
        root = Path(tempfile.mkdtemp())
        calls = []
        with mock.patch.object(pf, "repo_root", return_value=root), \
                mock.patch.object(setup.service, "control", lambda action, run=None: calls.append(action) or 0), \
                redirect_stdout(StringIO()):
            rc = setup.update(argparse.Namespace(dry_run=False), download=lambda target: calls.append(target))
        self.assertEqual(rc, 0)
        self.assertEqual(calls, [root, "restart"])
