import json
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

from uni_vpn import pac


class DomainListTests(unittest.TestCase):
    def test_defaults(self):
        self.assertEqual(pac.DEFAULT_DOMAINS,
                         ["sogo.uni-heidelberg.de", "elearning-med.uni-heidelberg.de", "cip.dmed.uni-heidelberg.de"])

    def test_parse_normalizes_and_reports_errors_with_line_numbers(self):
        text = "Sogo.Uni-Heidelberg.DE\n# Kommentar\n\n*.example.org  # Wildcard\nsogo.uni-heidelberg.de\nnicht gueltig\nhttp://x.y\n"
        domains, errors = pac.parse_domain_list(text)
        self.assertEqual(domains, ["sogo.uni-heidelberg.de", "example.org"])
        self.assertEqual(len(errors), 2)
        self.assertIn("Zeile 6", errors[0])
        self.assertIn("Zeile 7", errors[1])

    def test_parse_rejects_single_label_and_too_long(self):
        domains, errors = pac.parse_domain_list("localhost\n" + "a" * 64 + ".de\n")
        self.assertEqual(domains, [])
        self.assertEqual(len(errors), 2)

    def test_matches_host_and_subdomains_only(self):
        d = ["sogo.uni-heidelberg.de", "example.org"]
        self.assertTrue(pac.matches("sogo.uni-heidelberg.de", d))
        self.assertTrue(pac.matches("SOGO.uni-heidelberg.de.", d))
        self.assertTrue(pac.matches("mail.example.org", d))
        self.assertFalse(pac.matches("notsogo.uni-heidelberg.de", d))
        self.assertFalse(pac.matches("uni-heidelberg.de", d))
        self.assertFalse(pac.matches("", d))


class BuildPacTests(unittest.TestCase):
    def test_pac_lists_domains_and_port(self):
        text = pac.build_pac(["sogo.uni-heidelberg.de", "example.org"], 1080)
        self.assertIn("function FindProxyForURL(url, host)", text)
        self.assertIn(json.dumps(["sogo.uni-heidelberg.de", "example.org"]), text)
        self.assertIn('"SOCKS5 127.0.0.1:1080"', text)
        self.assertIn('"DIRECT"', text)

    @unittest.skipUnless(shutil.which("node"), "node fehlt")
    def test_pac_evaluates_like_the_matcher(self):
        script = pac.build_pac(["sogo.uni-heidelberg.de", "example.org"], 1080) + """
const cases = ["sogo.uni-heidelberg.de", "SOGO.uni-heidelberg.de.", "mail.example.org", "notsogo.uni-heidelberg.de", "uni-heidelberg.de", "ifconfig.me"];
console.log(JSON.stringify(cases.map(h => FindProxyForURL("https://" + h + "/", h))));
"""
        out = subprocess.run(["node", "-e", script], capture_output=True, text=True, check=True).stdout
        self.assertEqual(json.loads(out), ["SOCKS5 127.0.0.1:1080", "SOCKS5 127.0.0.1:1080", "SOCKS5 127.0.0.1:1080",
                                           "DIRECT", "DIRECT", "DIRECT"])


class DomainFileTests(unittest.TestCase):
    def setUp(self):
        self.path = Path(tempfile.mkdtemp()) / "domains.txt"

    def test_missing_file_gives_defaults(self):
        self.assertEqual(pac.read_domains(self.path), pac.DEFAULT_DOMAINS)

    def test_roundtrip_keeps_order_and_comment_header(self):
        pac.write_domains(self.path, ["example.org", "sogo.uni-heidelberg.de"])
        text = self.path.read_text(encoding="utf-8")
        self.assertTrue(text.startswith("#"), text)
        self.assertEqual(pac.read_domains(self.path), ["example.org", "sogo.uni-heidelberg.de"])

    def test_invalid_lines_are_skipped_when_reading(self):
        self.path.write_text("sogo.uni-heidelberg.de\nkaputt\n", encoding="utf-8")
        self.assertEqual(pac.read_domains(self.path), ["sogo.uni-heidelberg.de"])

    def test_empty_file_means_no_domains_not_defaults(self):
        self.path.write_text("# nichts\n", encoding="utf-8")
        self.assertEqual(pac.read_domains(self.path), [])
