import tempfile
import unittest
from pathlib import Path

from uni_vpn import config
from uni_vpn import universities as unis


class LoadTests(unittest.TestCase):
    def write(self, text):
        path = Path(tempfile.mkdtemp()) / "config.toml"
        path.write_text(text, encoding="utf-8")
        return path

    def test_defaults_and_user(self):
        cfg = config.load(self.write('user = "ab123"\n'))
        self.assertEqual(cfg.user, "ab123")
        self.assertEqual(cfg.host, "vpn-ac.uni-heidelberg.de")
        self.assertEqual(cfg.socks_port, 1080)
        self.assertEqual(cfg.http_port, 1081)
        self.assertEqual(cfg.idle_minutes, 15)
        self.assertEqual(cfg.backoff, [5, 10, 20, 40, 80, 300])

    def test_missing_user(self):
        with self.assertRaises(config.ConfigError) as cm:
            config.load(self.write('host = "x"\n'))
        self.assertIn("user", str(cm.exception))

    def test_unknown_key_reports_line(self):
        with self.assertRaises(config.ConfigError) as cm:
            config.load(self.write('user = "a"\nfoo = 1\n'))
        self.assertEqual(cm.exception.line, 2)

    def test_syntax_error_reports_line(self):
        with self.assertRaises(config.ConfigError) as cm:
            config.load(self.write('user = "a"\nport = \n'))
        self.assertEqual(cm.exception.line, 2)

    def test_wrong_type(self):
        with self.assertRaises(config.ConfigError) as cm:
            config.load(self.write('user = "a"\nsocks_port = "x"\n'))
        self.assertEqual(cm.exception.line, 2)

    def test_timing_table(self):
        cfg = config.load(self.write('user = "a"\n[timing]\nready_timeout = 2.5\nbackoff = [0.1, 0.2]\n'))
        self.assertEqual(cfg.ready_timeout, 2.5)
        self.assertEqual(cfg.backoff, [0.1, 0.2])

    def test_same_ports_rejected(self):
        with self.assertRaises(config.ConfigError):
            config.load(self.write('user = "a"\nsocks_port = 5\nhttp_port = 5\n'))

    def test_missing_file(self):
        with self.assertRaises(config.ConfigError) as cm:
            config.load(Path(tempfile.mkdtemp()) / "nope.toml")
        self.assertIn("missing", str(cm.exception))

    def test_write_initial_roundtrip(self):
        path = Path(tempfile.mkdtemp()) / "config.toml"
        config.write_initial(path, user="ab123")
        cfg = config.load(path)
        self.assertEqual(cfg.user, "ab123")
        self.assertEqual(cfg.path, path)


class UniversityTests(unittest.TestCase):
    def write(self, text):
        path = Path(tempfile.mkdtemp()) / "config.toml"
        path.write_text(text, encoding="utf-8")
        return path

    def test_without_university_it_is_heidelberg(self):
        # Every config.toml written before profiles existed looks like this.
        cfg = config.load(self.write('host = "vpn-ac.uni-heidelberg.de"\nuser = "ab123"\n'))
        self.assertEqual(cfg.university, "heidelberg")
        self.assertEqual(cfg.mfa, "totp_field")
        self.assertTrue(cfg.needs_totp)
        self.assertFalse(cfg.no_external_auth)
        self.assertEqual(cfg.useragent, "AnyConnect Linux_64 5.1.18.314")
        self.assertEqual(cfg.mfa_portal_url, "https://mfa.uni-heidelberg.de/")
        self.assertEqual(cfg.login_name, "ab123")

    def test_default_config_equals_the_heidelberg_profile(self):
        reference = config.Config()
        config.apply_profile(reference, unis.get("heidelberg"))
        self.assertEqual(config.Config(), reference)

    def test_profile_values_apply(self):
        cfg = config.load(self.write('university = "ethz"\nuser = "jdoe"\n'))
        self.assertEqual(cfg.host, "sslvpn.ethz.ch")
        self.assertEqual(cfg.authgroup, "staff-net")
        self.assertEqual(cfg.useragent, "AnyConnect")
        self.assertTrue(cfg.no_external_auth)
        self.assertEqual(cfg.login_name, "jdoe@staff-net.ethz.ch")
        self.assertEqual(cfg.university_name, "ETH Zurich")
        self.assertEqual(cfg.default_domains, [])

    def test_overrides_win_wherever_they_stand(self):
        cfg = config.load(self.write('user = "jdoe"\nauthgroup = "student-net"\nuniversity = "ethz"\n'
                                     'username_suffix = "@student-net.ethz.ch"\nno_external_auth = false\n'))
        self.assertEqual(cfg.authgroup, "student-net")
        self.assertEqual(cfg.login_name, "jdoe@student-net.ethz.ch")
        self.assertFalse(cfg.no_external_auth)

    def test_a_typed_realm_replaces_the_suffix(self):
        cfg = config.load(self.write('university = "stuttgart"\nuser = "st123456@stud.uni-stuttgart.de"\n'))
        self.assertEqual(cfg.login_name, "st123456@stud.uni-stuttgart.de")
        self.assertFalse(cfg.needs_totp)

    def test_unknown_university_reports_its_line(self):
        with self.assertRaises(config.ConfigError) as cm:
            config.load(self.write('user = "a"\nuniversity = "nowhere"\n'))
        self.assertEqual(cm.exception.line, 2)
        self.assertIn("nowhere", str(cm.exception))

    def test_bad_profile_values_report_their_line(self):
        for line in ('mfa = "sms"', 'os = "beos"', "no_external_auth = 1", 'authgroup = "a\\"b"', 'host = "a b"'):
            with self.assertRaises(config.ConfigError, msg=line) as cm:
                config.load(self.write(f'user = "a"\n{line}\n'))
            self.assertEqual(cm.exception.line, 2, line)

    def test_hand_written_hosts_keep_working(self):
        cfg = config.load(self.write('user = "a"\nhost = "https://vpn-ac.uni-heidelberg.de/"\n'))
        self.assertEqual(cfg.host, "https://vpn-ac.uni-heidelberg.de/")

    def test_other_needs_a_host(self):
        with self.assertRaises(config.ConfigError) as cm:
            config.load(self.write('university = "other"\nuser = "a"\n'))
        self.assertIn("host", str(cm.exception))
        cfg = config.load(self.write('university = "other"\nuser = "a"\nhost = "vpn.example.edu"\n'))
        self.assertEqual(cfg.mfa, "none")
        self.assertTrue(cfg.no_external_auth)

    def test_user_with_a_realm_is_valid(self):
        self.assertTrue(config.valid_user("st123456@stud.uni-stuttgart.de"))
        for user in ("a@", "@b", "a@b@c", 'a"b', "a b", "a@b c"):
            self.assertFalse(config.valid_user(user), user)

    def test_write_initial_writes_university_and_overrides(self):
        path = Path(tempfile.mkdtemp()) / "config.toml"
        config.write_initial(path, user="ab123", university="other",
                             overrides={"host": "VPN.Example.edu", "authgroup": "Staff (Split)", "mfa": "none",
                                        "no_external_auth": False})
        text = path.read_text()
        self.assertIn('university = "other"', text)
        cfg = config.load(path)
        self.assertEqual((cfg.host, cfg.authgroup, cfg.mfa, cfg.no_external_auth),
                         ("vpn.example.edu", "Staff (Split)", "none", False))

    def test_write_initial_for_a_listed_university_writes_no_host(self):
        # Without a host line a fix in universities.json reaches existing installs.
        path = Path(tempfile.mkdtemp()) / "config.toml"
        config.write_initial(path, user="ab123", university="bonn")
        self.assertNotIn("host", path.read_text())
        self.assertEqual(config.load(path).host, "unibn-vpn.uni-bonn.de")

    def test_write_initial_refuses_bad_profiles(self):
        path = Path(tempfile.mkdtemp()) / "config.toml"
        for university, overrides in (("nowhere", {}), ("other", {}), ("other", {"host": "vpn.example.edu", "mfa": "sms"})):
            with self.assertRaises(config.ConfigError):
                config.write_initial(path, user="ab123", university=university, overrides=overrides)
        self.assertFalse(path.exists())

    def test_profile_config_names_the_field(self):
        with self.assertRaises(unis.FieldError) as cm:
            config.profile_config("other", {"host": "vpn.example.edu", "authgroup": "a\nb"})
        self.assertEqual(cm.exception.field, "authgroup")
        with self.assertRaises(unis.FieldError) as cm:
            config.profile_config("nowhere")
        self.assertEqual(cm.exception.field, "university")


class SetValuesTests(unittest.TestCase):
    def test_sets_several_keys_and_keeps_the_rest(self):
        path = Path(tempfile.mkdtemp()) / "config.toml"
        path.write_text('# mine\nuser = "ab1"  # comment\nno_external_auth = true\n[timing]\ntick = 5\n', encoding="utf-8")
        config.set_values(path, {"user": "cd2", "university": "bonn", "no_external_auth": False})
        text = path.read_text()
        self.assertIn("# mine", text)
        self.assertIn("# comment", text)
        cfg = config.load(path)
        self.assertEqual((cfg.user, cfg.university, cfg.no_external_auth, cfg.tick), ("cd2", "bonn", False, 5))


class SetUserTests(unittest.TestCase):
    def test_unknown_forms_are_refused_instead_of_breaking_the_file(self):
        for line in ('user = """ab1"""', '"user" = "ab1"'):
            path = Path(tempfile.mkdtemp()) / "config.toml"
            original = f'{line}\nhttp_port = 1081\n'
            path.write_text(original, encoding="utf-8")
            with self.assertRaises(config.ConfigError):
                config.set_user(path, "zz9")
            self.assertEqual(path.read_text(encoding="utf-8"), original)

    def test_replaces_double_and_single_quoted_values(self):
        for line in ('user = "ab123"', "user = 'ab123'", "user='ab123'  # mine"):
            path = Path(tempfile.mkdtemp()) / "config.toml"
            path.write_text(f'host = "x"\n{line}\nhttp_port = 1081\n[timing]\ntick = 5\n', encoding="utf-8")
            config.set_user(path, "cd456")
            text = path.read_text(encoding="utf-8")
            self.assertEqual(text.count("user"), 1, text)
            self.assertEqual(config.load(path).user, "cd456")
            self.assertEqual(config.load(path).http_port, 1081)

    def test_adds_the_key_when_missing(self):
        path = Path(tempfile.mkdtemp()) / "config.toml"
        path.write_text('host = "x"\n[timing]\ntick = 5\n', encoding="utf-8")
        config.set_user(path, "cd456")
        self.assertEqual(config.load(path).user, "cd456")
        self.assertEqual(config.load(path).tick, 5)


if __name__ == "__main__":
    unittest.main()
