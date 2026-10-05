import asyncio
import logging
import os
import socket
import stat
import subprocess
import sys
import tempfile
import time
import unittest
from pathlib import Path
from unittest import mock

from uni_vpn import tunnel as tn
from uni_vpn.config import Config

from tests import posix_only

FAKE = str(Path(__file__).parent / "fake_openconnect.py")
WRAPPER = "/nonexistent/uni-vpn-ocproxy"


class ClassifyTests(unittest.TestCase):
    def test_markers(self):
        self.assertEqual(tn.classify_line("User input required in non-interactive mode")[0], "auth_failed")
        self.assertIn("uni-vpn log", tn.classify_line("User input required in non-interactive mode")[1])
        self.assertIn("password", tn.classify_line("Failed to complete authentication")[1])
        self.assertIn("HostScan", tn.classify_line("Error: Server asked us to run CSD hostscan.")[1])
        self.assertEqual(tn.classify_line("SAML authentication required")[0], "auth_failed")
        self.assertEqual(tn.classify_line("Server certificate verify failed")[0], "error")
        self.assertIsNone(tn.classify_line("Connected as 10.0.0.2"))

    def test_wrong_password_and_input_required_share_message(self):
        # Finding 6: with --non-inter a wrong password first produces
        # "User input required", then "Failed to complete authentication".
        # Both markers must yield the same honest message.
        input_required = tn.classify_line("User input required in non-interactive mode")
        auth_failed = tn.classify_line("Failed to complete authentication")
        self.assertEqual(input_required[0], "auth_failed")
        self.assertEqual(auth_failed[0], "auth_failed")
        self.assertEqual(input_required[1], auth_failed[1])
        self.assertIn("password", input_required[1])
        self.assertIn("uni-vpn password", input_required[1])
        self.assertIn("uni-vpn log", input_required[1])

    def test_invalid_soft_token_string_names_the_command(self):
        # Real openconnect 9.12 with a broken secret file: "Invalid base32 token string",
        # then "Soft token string is invalid", exit code 1 before any network contact.
        state, message = tn.classify_line("Soft token string is invalid")
        self.assertEqual(state, "auth_failed")
        self.assertIn("uni-vpn totp", message)

    def test_rejected_soft_token_names_the_second_factor(self):
        # If a server shows the OTP form again, openconnect tries two codes and then
        # reports "switching to manual entry"; "User input required" and
        # "Failed to complete authentication" follow. The first match counts.
        state, message = tn.classify_line("Server is rejecting the soft token; switching to manual entry")
        self.assertEqual(state, "auth_failed")
        self.assertIn("One-time code", message)
        self.assertIn("uni-vpn totp", message)
        self.assertIn("clock", message)

    def test_login_failed_before_otp_means_password(self):
        # Measured on 2026-09-08: the ASA rejects a wrong password before it asks for the OTP.
        seq = tn.Classifier()
        self.assertIsNone(seq.feed("Bitte geben Sie ihren Benutzernamen und ihr Passwort ein."))
        state, message = seq.feed("Login failed.")
        self.assertEqual(state, "auth_failed")
        self.assertIn("password", message)
        self.assertIn("uni-vpn password", message)
        self.assertNotIn("One-time code", message)

    def test_login_failed_after_otp_means_second_factor(self):
        # Measured on 2026-09-08: with a wrong secret the OTP prompt comes first, openconnect
        # generates the code, then "Login failed." and the form starts over.
        seq = tn.Classifier()
        seq.feed("Bitte zweiten Faktor eingeben (OTP) / Please enter second factor (OTP).")
        self.assertIsNone(seq.feed("Generating OATH TOTP token code"))
        state, message = seq.feed("Login failed.")
        self.assertEqual(state, "auth_failed")
        self.assertIn("One-time code", message)
        self.assertIn("uni-vpn totp", message)
        self.assertNotIn("uni-vpn password", message)

    def test_login_failed_is_worded_by_the_second_factor(self):
        for mfa, expected in (("none", tn.PASSWORD_REJECTED), ("totp_field", tn.PASSWORD_REJECTED),
                              ("totp_append", tn.APPEND_REJECTED), ("duo_push", tn.DUO_REJECTED)):
            seq = tn.Classifier(mfa)
            self.assertEqual(seq.feed("Login failed."), ("auth_failed", expected), mfa)
        self.assertIn("clock", tn.APPEND_REJECTED)
        self.assertIn("Duo", tn.DUO_REJECTED)
        for message in (tn.APPEND_REJECTED, tn.DUO_REJECTED):
            self.assertIn("uni-vpn password", message)
            self.assertNotIn("totp", message.lower(), "the page would offer the TOTP fix")

    def test_unsupported_login_methods_say_so_without_naming_a_university(self):
        for line in ("SAML authentication required", "Opening external browser for authentication"):
            state, message = tn.classify_line(line)
            self.assertEqual(state, "auth_failed")
            self.assertIn("browser", message)
            self.assertIn("not support", message)
        self.assertIn("not support", tn.classify_line("Error: Server asked us to run CSD hostscan.")[1])
        for _needle, _state, message in tn.MARKERS:
            self.assertNotIn("Heidelberg", message)
            self.assertNotIn("needs an update", message)

    def test_tunnel_classifier_follows_the_profile(self):
        t = tn.Tunnel(Config(user="u", mfa="duo_push"), FAKE, WRAPPER, logging.getLogger("t"), token_dir=Path(tempfile.mkdtemp()))
        self.assertEqual(t.classifier.mfa, "duo_push")

    def test_classifier_keeps_first_verdict(self):
        seq = tn.Classifier()
        seq.feed("Generating OATH TOTP token code")
        first = seq.feed("Login failed.")
        self.assertEqual(seq.feed("User input required in non-interactive mode"), first)
        self.assertEqual(seq.verdict, first)


TOKEN = "base32:GEZDGNBVGY3TQOJQ"


class ProfileCommandTests(unittest.TestCase):
    def make(self, **fields):
        cfg = Config(user="ab123", **fields)
        return tn.Tunnel(cfg, "/usr/bin/openconnect", WRAPPER, logging.getLogger("t"), token_dir=Path(tempfile.mkdtemp()))

    def test_heidelberg_command_is_unchanged(self):
        # The command line from before profiles existed, argument for argument.
        self.assertEqual(self.make().command(4321), [
            "/usr/bin/openconnect", "--protocol=anyconnect", "--useragent=AnyConnect Linux_64 5.1.18.314",
            "--user=ab123", "--passwd-on-stdin", "--non-inter", "--no-dtls", "--force-dpd=30",
            "--reconnect-timeout=60", "--script-tun", f"--script=exec {WRAPPER} 4321", "vpn-ac.uni-heidelberg.de"])

    def test_bundled_openconnect_on_macos_gets_the_system_certificates(self):
        package = tempfile.mkdtemp()
        ca = Path(package) / "cert.pem"
        ca.write_text("")
        bundled = os.path.join(package, "arm64", "bin", "openconnect")
        cfg = Config(user="ab123")
        with mock.patch.object(tn.pf, "IS_MACOS", True), mock.patch.object(tn.pf, "PACKAGE_DIR", package), \
                mock.patch.object(tn.pf, "MACOS_CA_FILE", str(ca)):
            cmd = tn.Tunnel(cfg, bundled, WRAPPER, logging.getLogger("t")).command(1)
            self.assertIn(f"--cafile={ca}", cmd)
            self.assertEqual(cmd[-1], cfg.host)
            # Homebrew's openconnect finds its own certificate file.
            cmd = tn.Tunnel(cfg, "/opt/homebrew/bin/openconnect", WRAPPER, logging.getLogger("t")).command(1)
            self.assertFalse(any(arg.startswith("--cafile") for arg in cmd))

    def test_profile_flags(self):
        cmd = self.make(host="sslvpn.ethz.ch", authgroup="staff-net", usergroup="exchange", os="win",
                        useragent="AnyConnect", username_suffix="@staff-net.ethz.ch", no_external_auth=True).command(1)
        for arg in ("--authgroup=staff-net", "--usergroup=exchange", "--os=win", "--useragent=AnyConnect",
                    "--user=ab123@staff-net.ethz.ch", "--no-external-auth", "--non-inter"):
            self.assertIn(arg, cmd)
        self.assertEqual(cmd[-1], "sslvpn.ethz.ch")

    def test_group_names_with_spaces_stay_one_argument(self):
        cmd = self.make(authgroup="RWTH-VPN (Split Tunnel)").command(1)
        self.assertIn("--authgroup=RWTH-VPN (Split Tunnel)", cmd)

    def test_duo_push_drops_non_inter(self):
        self.assertNotIn("--non-inter", self.make(mfa="duo_push").command(1))

    def test_stdin_per_mode(self):
        with mock.patch.object(tn.totp_mod, "code", return_value="123456"):
            self.assertEqual(self.make(mfa="none").stdin_bytes(b"pw", None), b"pw\n")
            self.assertEqual(self.make(mfa="totp_field").stdin_bytes(b"pw", TOKEN), b"pw\n")
            self.assertEqual(self.make(mfa="duo_push").stdin_bytes(b"pw", None), b"pw\npush\n")
            self.assertEqual(self.make(mfa="totp_append").stdin_bytes(b"pw", TOKEN), b"pw123456\n")
            self.assertEqual(self.make(mfa="totp_append", totp_separator=",").stdin_bytes(b"pw", TOKEN), b"pw,123456\n")

    def test_totp_append_records_when_the_code_was_made(self):
        t = self.make(mfa="totp_append")
        t.stdin_bytes(b"pw", TOKEN)
        self.assertLess(abs(t.otp_generated_at - time.time()), 5)
        with self.assertRaises(ValueError):
            self.make(mfa="totp_append").stdin_bytes(b"pw", None)


@posix_only
class TunnelTests(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        self.cfg = Config(user="u", host="vpn.example")
        self.log = logging.getLogger("test")
        tmp = Path(tempfile.mkdtemp())
        self.pwfile = tmp / "pw"
        self.tokenfile = tmp / "token"
        self.token_dir = tmp / "state"
        self.token_dir.mkdir()
        self.env = mock.patch.dict(os.environ, {"FAKE_PASSWORD_FILE": str(self.pwfile), "FAKE_TOKEN_FILE": str(self.tokenfile),
                                                "FAKE_MODE": "ok", "FAKE_DELAY": "0.2"})
        self.env.start()

    def tearDown(self):
        self.env.stop()

    def make(self):
        return tn.Tunnel(self.cfg, FAKE, WRAPPER, self.log, token_dir=self.token_dir)

    def token_files(self):
        return sorted(p.name for p in self.token_dir.iterdir())

    def test_command(self):
        cmd = self.make().command(4321)
        self.assertEqual(cmd[0], FAKE)
        self.assertIn("--protocol=anyconnect", cmd)
        self.assertIn("--user=u", cmd)
        self.assertIn("--passwd-on-stdin", cmd)
        self.assertIn("--non-inter", cmd)
        self.assertIn("--no-dtls", cmd)
        # "exec" in front: openconnect runs the value via /bin/sh -c, and dash would otherwise
        # leave an sh running next to ocproxy (seen in the process list on 2026-09-08).
        self.assertIn(f"--script=exec {WRAPPER} 4321", cmd)
        self.assertEqual(cmd[-1], "vpn.example")
        self.assertNotIn("--dump-http-traffic", cmd)
        self.assertFalse([a for a in cmd if a.startswith("--token")])

    def test_command_with_token_file_enables_totp(self):
        t = self.make()
        t.token_file = self.token_dir / "totp-1"
        cmd = t.command(4321)
        self.assertIn("--token-mode=totp", cmd)
        self.assertIn(f"--token-secret=@{self.token_dir / 'totp-1'}", cmd)
        # The secret itself never appears on the command line.
        self.assertFalse([a for a in cmd if "base32" in a])

    async def test_totp_secret_reaches_openconnect_by_file_and_is_removed_when_ready(self):
        t = self.make()
        await t.start(b"secret", totp="base32:GEZDGNBVGY3TQOJQ")
        self.assertEqual(len(self.token_files()), 1)
        self.assertTrue(self.token_files()[0].startswith("totp-"))
        self.assertEqual(stat.S_IMODE((self.token_dir / self.token_files()[0]).stat().st_mode), 0o600)
        self.assertTrue(await t.wait_ready(3))
        self.assertEqual(self.tokenfile.read_text(), "base32:GEZDGNBVGY3TQOJQ\n")
        self.assertEqual(self.token_files(), [], "secret file must be gone once connected")
        await t.stop(2)

    async def test_token_file_removed_when_openconnect_exits_early(self):
        os.environ["FAKE_MODE"] = "auth_fail"
        t = self.make()
        await t.start(b"x", totp="base32:GEZDGNBVGY3TQOJQ")
        self.assertFalse(await t.wait_ready(3))
        await asyncio.sleep(0.1)
        self.assertEqual(self.token_files(), [])

    async def test_token_file_removed_on_stop_before_ready(self):
        os.environ["FAKE_MODE"] = "never_ready"
        t = self.make()
        await t.start(b"x", totp="base32:GEZDGNBVGY3TQOJQ")
        self.assertEqual(len(self.token_files()), 1)
        await t.stop(1)
        self.assertEqual(self.token_files(), [])

    async def test_without_totp_no_token_file(self):
        t = self.make()
        await t.start(b"secret")
        self.assertTrue(await t.wait_ready(3))
        self.assertEqual(self.token_files(), [])
        self.assertFalse(self.tokenfile.exists())
        await t.stop(2)

    async def test_rejected_soft_token_is_classified(self):
        os.environ["FAKE_MODE"] = "totp_rejected"
        t = self.make()
        await t.start(b"x", totp="base32:GEZDGNBVGY3TQOJQ")
        self.assertFalse(await t.wait_ready(3))
        self.assertEqual(t.returncode, 1)
        self.assertEqual(t.classification[0], "auth_failed")
        self.assertIn("One-time code", t.classification[1])
        self.assertNotIn("uni-vpn password", t.classification[1])
        self.assertIsNotNone(t.otp_generated_at)
        self.assertLess(abs(t.otp_generated_at - time.time()), 10)

    async def test_only_totp_field_writes_a_token_file(self):
        for mfa in ("none", "duo_push", "totp_append"):
            self.cfg.mfa = mfa
            t = self.make()
            await t.start(b"secret", totp=TOKEN)
            self.assertEqual(self.token_files(), [], mfa)
            self.assertFalse([a for a in t.command(1) if a.startswith("--token")], mfa)
            self.assertTrue(await t.wait_ready(3))
            await t.stop(2)

    async def test_duo_push_sends_push_as_the_second_line(self):
        self.cfg.mfa = "duo_push"
        t = self.make()
        await t.start(b"secret")
        self.assertTrue(await t.wait_ready(3))
        await t.stop(2)
        self.assertEqual(self.pwfile.read_text().splitlines(), ["secret", "push"])

    async def test_otp_generated_at_is_none_without_token(self):
        t = self.make()
        await t.start(b"secret")
        self.assertTrue(await t.wait_ready(3))
        await t.stop(2)
        self.assertIsNone(t.otp_generated_at)

    def test_remove_stale_token_files(self):
        (self.token_dir / "totp-123").write_text("old")
        (self.token_dir / "totp-456").write_text("old")
        (self.token_dir / "daemon.log").write_text("stays")
        removed = tn.remove_stale_token_files(self.token_dir)
        self.assertEqual(removed, 2)
        self.assertEqual(self.token_files(), ["daemon.log"])
        self.assertEqual(tn.remove_stale_token_files(self.token_dir / "does-not-exist"), 0)

    async def test_start_ready_stop(self):
        t = self.make()
        await t.start(b"secret")
        self.assertTrue(await t.wait_ready(3))
        self.assertTrue(tn.port_open(t.port))
        self.assertEqual(self.pwfile.read_text(), "secret\n")
        await t.stop(2)
        self.assertTrue(t.exited.is_set())
        self.assertEqual(t.returncode, 0)
        self.assertTrue(t.stopped_by_us)
        self.assertFalse(tn.port_open(t.port))

    async def test_auth_fail(self):
        os.environ["FAKE_MODE"] = "auth_fail"
        t = self.make()
        await t.start(b"x")
        self.assertFalse(await t.wait_ready(3))
        self.assertTrue(t.exited.is_set())
        self.assertEqual(t.returncode, 1)
        self.assertEqual(t.classification[0], "auth_failed")
        self.assertIn("uni-vpn password", t.classification[1])
        self.assertNotIn("One-time code", t.classification[1])
        self.assertTrue(any("Failed to complete" in line for line in t.stderr_tail))

    async def test_input_required_message(self):
        os.environ["FAKE_MODE"] = "input_required"
        t = self.make()
        await t.start(b"x")
        self.assertFalse(await t.wait_ready(3))
        self.assertEqual(t.classification[0], "auth_failed")
        self.assertIn("uni-vpn log", t.classification[1])
        # Finding 6: a wrong password produces the same sequence of lines.
        self.assertIn("password", t.classification[1])

    def test_script_value_survives_sh_with_spaces_and_quote(self):
        # Finding 5: openconnect runs --script via /bin/sh -c.
        base = Path(tempfile.mkdtemp()) / "with spaces"
        base.mkdir()
        wrapper = base / "o'proxy wrapper"
        argfile = base / "args"
        wrapper.write_text(f"#!/bin/sh\nprintf '%s\\n' \"$@\" > '{argfile}'\n")
        wrapper.chmod(0o755)
        t = tn.Tunnel(self.cfg, FAKE, str(wrapper), self.log)
        cmd = t.command(4321)
        script = [a for a in cmd if a.startswith("--script=")][0][len("--script="):]
        result = subprocess.run(["/bin/sh", "-c", script], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(argfile.read_text(), "4321\n")

    def test_kill_wrapper_argv(self):
        # Finding 4: pattern without a leading hyphen, "--" before the pattern,
        # only our own processes, return code in the log.
        t = self.make()
        t.port = 4321
        with mock.patch.object(tn.subprocess, "run") as run:
            run.return_value = subprocess.CompletedProcess(args=[], returncode=1)
            with self.assertLogs(self.log, "WARNING") as logs:
                t._kill_wrapper()
        run.assert_called_once_with(
            ["pkill", "-9", "-U", str(os.getuid()), "-f", "--", "ocproxy -D 127.0.0.1:4321 "],
            check=False,
        )
        self.assertTrue(any("1" in line and "pkill" in line for line in logs.output), logs.output)

    def test_kill_wrapper_kills_port_holder(self):
        # Finding 4: a process whose command line looks like the wrapper exec and that
        # holds the port must be gone after _kill_wrapper().
        port = tn.free_port()
        code = (
            "import socket, sys, time\n"
            "s = socket.socket()\n"
            "s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)\n"
            "s.bind(('127.0.0.1', int(sys.argv[3].split(':')[1])))\n"
            "s.listen(16)\n"
            "while True:\n"
            "    c, _ = s.accept()\n"
            "    c.close()\n"
        )
        dummy = subprocess.Popen(
            [sys.executable, "-c", code, "ocproxy", "-D", f"127.0.0.1:{port}", "-k", "30"],
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
        )
        try:
            deadline = time.monotonic() + 3
            while not tn.port_open(port) and time.monotonic() < deadline:
                time.sleep(0.05)
            self.assertTrue(tn.port_open(port), "dummy does not hold the port")
            t = self.make()
            t.port = port
            t._kill_wrapper()
            deadline = time.monotonic() + 2
            while tn.port_open(port) and time.monotonic() < deadline:
                time.sleep(0.05)
            self.assertFalse(tn.port_open(port), "port still open after _kill_wrapper()")
            self.assertEqual(dummy.wait(timeout=2), -9)
        finally:
            if dummy.poll() is None:
                dummy.kill()
                dummy.wait()

    async def test_ignore_sigterm_gets_killed(self):
        os.environ["FAKE_MODE"] = "ignore_sigterm"
        t = self.make()
        await t.start(b"x")
        self.assertTrue(await t.wait_ready(3))
        await t.stop(0.5)
        self.assertTrue(t.exited.is_set())
        self.assertEqual(t.returncode, -9)

    async def test_never_ready_timeout(self):
        os.environ["FAKE_MODE"] = "never_ready"
        t = self.make()
        await t.start(b"x")
        self.assertFalse(await t.wait_ready(0.6))
        self.assertFalse(t.exited.is_set())
        await t.stop(1)
        self.assertTrue(t.exited.is_set())

    async def test_missing_binary(self):
        t = tn.Tunnel(self.cfg, "/nonexistent/openconnect", WRAPPER, self.log)
        with self.assertRaises(OSError):
            await t.start(b"x")

    async def test_reconnect_sends_sigusr2(self):
        t = self.make()
        await t.start(b"x")
        self.assertTrue(await t.wait_ready(3))
        t.reconnect()
        await asyncio.sleep(0.3)
        self.assertTrue(any("SIGUSR2" in line for line in t.stderr_tail))
        await t.stop(2)


class PortTests(unittest.TestCase):
    def test_free_port_is_closed(self):
        port = tn.free_port()
        self.assertFalse(tn.port_open(port))
        s = socket.socket()
        s.bind(("127.0.0.1", port))
        s.listen(1)
        self.assertTrue(tn.port_open(port))
        s.close()
