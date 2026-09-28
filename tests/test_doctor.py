import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from uni_vpn import doctor
from uni_vpn import platform as pf


def write_config(text='user = "ab1"\n'):
    path = Path(tempfile.mkdtemp()) / "config.toml"
    path.write_text(text)
    return path


def unreachable(cfg, path):
    raise doctor.cli.DaemonUnreachable("x")


STATUS = {"state": "idle", "message": "Nicht verbunden", "version": "0.1.0", "protocol": 1}


class DoctorTests(unittest.TestCase):
    def setUp(self):
        self.commands = []

    def fake_run(self, cmd, **kwargs):
        self.commands.append(cmd)
        return subprocess.CompletedProcess(cmd, 1, "", "")

    def probes(self, **overrides):
        base = dict(
            find_binary=lambda name, override=None: f"/usr/bin/{name}",
            is_active=lambda: True,
            port_in_use=lambda port: True,
            keyring_probe=lambda user, kind="password": "present",
            proxy_state=lambda port: "ok",
            cisco_installed=lambda: False,
            cisco_connected=lambda: False,
            api_get=lambda cfg, path: dict(STATUS),
            python_version=(3, 12, 3),
            run=self.fake_run,
        )
        base.update(overrides)
        return base

    def by_name(self, checks, name):
        return next(c for c in checks if c.name == name)

    def test_all_ok(self):
        checks = doctor.run_checks(write_config(), **self.probes())
        self.assertFalse([c for c in checks if c.status == "fail"], doctor.format_checks(checks))

    def test_old_python_fails(self):
        checks = doctor.run_checks(write_config(), **self.probes(python_version=(3, 9, 6)))
        self.assertEqual(self.by_name(checks, "Python").status, "fail")

    def test_missing_binary_fails(self):
        checks = doctor.run_checks(write_config(), **self.probes(find_binary=lambda n, override=None: None))
        self.assertEqual(self.by_name(checks, "openconnect").status, "fail")
        self.assertIn("install.sh", self.by_name(checks, "openconnect").detail)

    def test_config_error_fails_and_uses_defaults(self):
        checks = doctor.run_checks(write_config("user = 1\n"), **self.probes())
        self.assertEqual(self.by_name(checks, "Konfiguration").status, "fail")
        self.assertIn("Zeile 1", self.by_name(checks, "Konfiguration").detail)

    def test_service_inactive_and_port_busy(self):
        checks = doctor.run_checks(write_config(), **self.probes(is_active=lambda: False, api_get=unreachable))
        self.assertEqual(self.by_name(checks, "Dienst").status, "fail")
        self.assertEqual(self.by_name(checks, "Port 1080").status, "fail")
        self.assertIn("belegt von anderem Prozess", self.by_name(checks, "Port 1080").detail)
        self.assertEqual(self.by_name(checks, "Daemon").status, "fail")

    def test_manual_daemon_counts_as_own_ports(self):
        checks = doctor.run_checks(write_config(), **self.probes(is_active=lambda: False))
        self.assertEqual(self.by_name(checks, "Dienst").status, "warn")
        self.assertIn("Daemon laeuft, aber nicht als Dienst", self.by_name(checks, "Dienst").detail)
        for port in ("Port 1080", "Port 1081"):
            self.assertEqual(self.by_name(checks, port).status, "ok", port)
            self.assertIn("gebunden (uni-vpn)", self.by_name(checks, port).detail)
        self.assertEqual(self.by_name(checks, "Daemon").status, "ok")
        self.assertFalse([c for c in self.commands if c[0] in ("ss", "lsof")])

    def test_foreign_daemon_without_protocol_is_not_ours(self):
        checks = doctor.run_checks(write_config(), **self.probes(is_active=lambda: False,
                                                                 api_get=lambda c, p: {"state": "idle", "message": "m", "version": "0"}))
        self.assertEqual(self.by_name(checks, "Port 1080").status, "fail")

    def test_port_owner_from_ss_on_linux(self):
        def run(cmd, **kwargs):
            self.commands.append(cmd)
            if cmd[0] == "ss":
                return subprocess.CompletedProcess(cmd, 0, "State  Recv-Q Send-Q Local Address:Port Peer Address:Port Process\n"
                                                           'LISTEN 0      4096   127.0.0.1:1080     0.0.0.0:*     users:(("tor",pid=4242,fd=6))\n', "")
            return subprocess.CompletedProcess(cmd, 1, "", "")

        with mock.patch.object(pf, "IS_MACOS", False):
            checks = doctor.run_checks(write_config(), **self.probes(is_active=lambda: False, api_get=unreachable, run=run))
        detail = self.by_name(checks, "Port 1080").detail
        self.assertIn("tor", detail)
        self.assertIn("pid=4242", detail)
        self.assertIn(["ss", "-ltnp", "sport = :1080"], self.commands)

    def test_port_owner_from_lsof_on_macos(self):
        def run(cmd, **kwargs):
            self.commands.append(cmd)
            if cmd[0] == "lsof":
                return subprocess.CompletedProcess(cmd, 0, "COMMAND PID USER FD TYPE DEVICE SIZE/OFF NODE NAME\n"
                                                           "tor 4242 x 6u IPv4 0x1 0t0 TCP 127.0.0.1:1080 (LISTEN)\n", "")
            return subprocess.CompletedProcess(cmd, 1, "", "")

        with mock.patch.object(pf, "IS_MACOS", True):
            checks = doctor.run_checks(write_config(), **self.probes(is_active=lambda: False, api_get=unreachable, run=run))
        self.assertIn("tor 4242", self.by_name(checks, "Port 1080").detail)
        self.assertIn(["lsof", "-nP", "-iTCP:1080", "-sTCP:LISTEN"], self.commands)

    def test_port_owner_lookup_failure_keeps_hint(self):
        def run(cmd, **kwargs):
            raise FileNotFoundError(cmd[0])

        checks = doctor.run_checks(write_config(), **self.probes(is_active=lambda: False, api_get=unreachable, run=run))
        detail = self.by_name(checks, "Port 1080").detail
        self.assertEqual(self.by_name(checks, "Port 1080").status, "fail")
        self.assertIn("belegt von anderem Prozess", detail)
        self.assertIn("config.toml", detail)

    def test_openconnect_version_in_detail(self):
        def run(cmd, **kwargs):
            self.commands.append((cmd, kwargs.get("timeout")))
            if cmd == ["/usr/bin/openconnect", "--version"]:
                return subprocess.CompletedProcess(cmd, 0, "OpenConnect version v9.12\nUsing GnuTLS 3.8.3. Features present: ...\n", "")
            return subprocess.CompletedProcess(cmd, 1, "", "")

        checks = doctor.run_checks(write_config(), **self.probes(run=run))
        detail = self.by_name(checks, "openconnect").detail
        self.assertEqual(detail, "/usr/bin/openconnect, OpenConnect version v9.12")
        self.assertIn((["/usr/bin/openconnect", "--version"], 5), self.commands)

    def test_openconnect_version_failure_keeps_path(self):
        def run(cmd, **kwargs):
            raise subprocess.TimeoutExpired(cmd, 5)

        checks = doctor.run_checks(write_config(), **self.probes(run=run))
        self.assertEqual(self.by_name(checks, "openconnect").status, "ok")
        self.assertEqual(self.by_name(checks, "openconnect").detail, "/usr/bin/openconnect")

    def test_keyring_states(self):
        for state, expected in (("missing", "fail"), ("locked", "warn"), ("error:kaputt", "fail"), ("present", "ok")):
            checks = doctor.run_checks(write_config(), **self.probes(keyring_probe=lambda u, kind="password", s=state: s))
            self.assertEqual(self.by_name(checks, "Keyring").status, expected, state)
            self.assertEqual(self.by_name(checks, "Zweiter Faktor").status, expected, state)

    def test_second_factor_missing_names_command(self):
        def probe(user, kind="password"):
            return "missing" if kind == "totp" else "present"

        checks = doctor.run_checks(write_config(), **self.probes(keyring_probe=probe))
        self.assertEqual(self.by_name(checks, "Keyring").status, "ok")
        self.assertEqual(self.by_name(checks, "Zweiter Faktor").status, "fail")
        self.assertIn("uni-vpn totp", self.by_name(checks, "Zweiter Faktor").detail)

    def test_keyring_state_probes_requested_kind(self):
        seen = []

        async def fake_get(user, kind, timeout):
            seen.append((user, kind))
            raise doctor.credentials.TotpMissing("x") if kind == "totp" else doctor.credentials.PasswordMissing("x")

        with mock.patch.object(doctor.credentials, "get_secret", fake_get):
            self.assertEqual(doctor.keyring_state("ab1"), "missing")
            self.assertEqual(doctor.keyring_state("ab1", kind="totp"), "missing")
        self.assertEqual(seen, [("ab1", "password"), ("ab1", "totp")])

    def test_proxy_states(self):
        for state, expected, needle in (("ok", "ok", "proxy.pac"), ("unset", "fail", "install.sh"),
                                        ("foreign", "warn", "andere"), ("unavailable", "warn", "http://127.0.0.1:1081/proxy.pac")):
            checks = doctor.run_checks(write_config(), **self.probes(proxy_state=lambda port, s=state: s))
            check = self.by_name(checks, "Proxy-Regel")
            self.assertEqual(check.status, expected, state)
            self.assertIn(needle, check.detail, state)

    def test_cisco_connected_warns(self):
        checks = doctor.run_checks(write_config(), **self.probes(cisco_installed=lambda: True, cisco_connected=lambda: True))
        self.assertEqual(self.by_name(checks, "Cisco Secure Client").status, "warn")

    def test_daemon_error_state_warns(self):
        checks = doctor.run_checks(write_config(), **self.probes(api_get=lambda c, p: {"state": "auth_failed", "message": "Anmeldung abgelehnt", "version": "0.1.0", "protocol": 1}))
        self.assertEqual(self.by_name(checks, "Daemon").status, "warn")
        self.assertIn("Anmeldung abgelehnt", self.by_name(checks, "Daemon").detail)

    def test_format(self):
        text = doctor.format_checks([doctor.Check("A", "ok", "gut"), doctor.Check("B", "fail", "schlecht"), doctor.Check("C", "warn", "naja")])
        self.assertEqual(text.splitlines(), ["[OK] A: gut", "[!!] B: schlecht", "[..] C: naja"])
