import tempfile
import unittest
from pathlib import Path

from uni_vpn import config


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
        self.assertIn("fehlt", str(cm.exception))

    def test_write_initial_roundtrip(self):
        path = Path(tempfile.mkdtemp()) / "config.toml"
        config.write_initial(path, user="ab123")
        cfg = config.load(path)
        self.assertEqual(cfg.user, "ab123")
        self.assertEqual(cfg.path, path)


if __name__ == "__main__":
    unittest.main()
