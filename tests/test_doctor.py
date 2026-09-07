import tempfile
import unittest
from pathlib import Path

from uni_vpn import doctor


def write_config(text='user = "ab1"\n'):
    path = Path(tempfile.mkdtemp()) / "config.toml"
    path.write_text(text)
    return path


class DoctorTests(unittest.TestCase):
    def probes(self, **overrides):
        base = dict(
            find_binary=lambda name, override=None: f"/usr/bin/{name}",
            is_active=lambda: True,
            port_in_use=lambda port: True,
            keyring_probe=lambda user: "present",
            cisco_installed=lambda: False,
            cisco_connected=lambda: False,
            api_get=lambda cfg, path: {"state": "idle", "message": "Nicht verbunden", "version": "0.1.0"},
            python_version=(3, 12, 3),
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
        checks = doctor.run_checks(write_config(), **self.probes(is_active=lambda: False, api_get=lambda c, p: (_ for _ in ()).throw(doctor.cli.DaemonUnreachable("x"))))
        self.assertEqual(self.by_name(checks, "Dienst").status, "fail")
        self.assertEqual(self.by_name(checks, "Port 1080").status, "fail")
        self.assertIn("belegt", self.by_name(checks, "Port 1080").detail)

    def test_keyring_states(self):
        for state, expected in (("missing", "fail"), ("locked", "warn"), ("error:kaputt", "fail"), ("present", "ok")):
            checks = doctor.run_checks(write_config(), **self.probes(keyring_probe=lambda u, s=state: s))
            self.assertEqual(self.by_name(checks, "Keyring").status, expected, state)

    def test_cisco_connected_warns(self):
        checks = doctor.run_checks(write_config(), **self.probes(cisco_installed=lambda: True, cisco_connected=lambda: True))
        self.assertEqual(self.by_name(checks, "Cisco Secure Client").status, "warn")

    def test_daemon_error_state_warns(self):
        checks = doctor.run_checks(write_config(), **self.probes(api_get=lambda c, p: {"state": "auth_failed", "message": "Anmeldung abgelehnt", "version": "0.1.0"}))
        self.assertEqual(self.by_name(checks, "Daemon").status, "warn")
        self.assertIn("Anmeldung abgelehnt", self.by_name(checks, "Daemon").detail)

    def test_format(self):
        text = doctor.format_checks([doctor.Check("A", "ok", "gut"), doctor.Check("B", "fail", "schlecht"), doctor.Check("C", "warn", "naja")])
        self.assertEqual(text.splitlines(), ["[OK] A: gut", "[!!] B: schlecht", "[..] C: naja"])
