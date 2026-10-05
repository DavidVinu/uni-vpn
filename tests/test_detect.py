import unittest
import urllib.error
import urllib.request
import xml.etree.ElementTree as ET
from pathlib import Path

from uni_vpn import detect
from uni_vpn import universities as unis

FIXTURES = Path(__file__).parent / "fixtures" / "detect"


def fixture(name):
    """Replies recorded on 2026-10-05 with the init request below and no credentials."""
    return (FIXTURES / name).read_bytes()


class ParseTests(unittest.TestCase):
    def parse(self, name, host="vpn.example.edu"):
        return detect.parse_reply(fixture(name), host)

    def test_heidelberg_is_a_password_form_although_it_names_sso(self):
        d = self.parse("heidelberg.xml")
        self.assertTrue(d.cisco)
        self.assertFalse(d.saml)
        self.assertEqual([f["name"] for f in d.fields], ["username", "password"])
        self.assertEqual(d.groups, [])
        self.assertEqual(d.suggestion()["mfa"], "none", "the OTP form comes only after the password")
        self.assertIn("Benutzernamen", d.message)

    def test_bremen_groups_and_second_password(self):
        d = self.parse("bremen.xml")
        self.assertEqual(d.groups, ["Tunnel-All-Traffic", "Tunnel-Uni-Bremen"])
        self.assertEqual(d.group, "Tunnel-All-Traffic")
        self.assertTrue(d.second_password)
        self.assertEqual(d.suggestion(), {"host": "vpn.example.edu", "usergroup": "", "authgroup": "Tunnel-All-Traffic",
                                          "mfa": "totp_field"})

    def test_ethz_realm_groups(self):
        d = self.parse("ethz.xml")
        self.assertEqual(d.groups, ["staff-net", "student-net"])
        self.assertEqual(d.suggestion()["authgroup"], "staff-net")
        self.assertEqual(d.suggestion()["mfa"], "totp_field")

    def test_marburg_appends_the_code_which_the_form_does_not_show(self):
        d = self.parse("marburg.xml")
        self.assertEqual(d.group, "unimr-vpn-staff-Passwort+2FA")
        self.assertEqual(len(d.groups), 4)
        self.assertFalse(d.second_password)
        self.assertEqual(d.suggestion()["mfa"], "none")

    def test_muenster_and_kassel_without_groups(self):
        self.assertTrue(self.parse("muenster.xml").second_password)
        d = self.parse("kassel.xml")
        self.assertEqual(d.groups, [])
        self.assertEqual(d.group, "")
        self.assertEqual(d.message, "")
        self.assertEqual(d.suggestion()["authgroup"], "")

    def test_stanford_default_group_is_saml_but_group_stanford_is_not(self):
        default = self.parse("stanford.xml")
        self.assertTrue(default.saml)
        self.assertEqual(default.group, "CardinalKey")
        self.assertIn("Stanford", default.groups)
        self.assertEqual(default.suggestion()["mfa"], "saml")
        stanford = self.parse("stanford-group-stanford.xml")
        self.assertFalse(stanford.saml)
        self.assertEqual(stanford.group, "Stanford")

    def test_fu_berlin_is_saml(self):
        d = self.parse("fu-berlin.xml")
        self.assertTrue(d.saml)
        self.assertEqual(d.fields, [])

    def test_other_replies_are_not_cisco(self):
        for data in (b"<html><body>Welcome</body></html>", b"not xml", b"<?xml version='1.0'?><other/>",
                     b'<!DOCTYPE x [<!ENTITY a "aaaa">]><config-auth>&a;</config-auth>',
                     b"<config-auth>" + b"x" * (detect.MAX_REPLY + 1) + b"</config-auth>"):
            d = detect.parse_reply(data, "www.example.edu")
            self.assertFalse(d.cisco, data[:40])
            self.assertTrue(d.reachable)
            self.assertIn("Cisco", d.error)

    def test_as_dict_is_json_ready(self):
        data = self.parse("bremen.xml").as_dict()
        self.assertEqual(data["suggestion"]["mfa"], "totp_field")
        self.assertEqual(data["fields"][2], {"name": "secondary_password", "type": "password", "label": "Password:"})


class RequestTests(unittest.TestCase):
    def test_init_request_is_what_openconnect_sends(self):
        root = ET.fromstring(detect.init_request("vpn.example.edu", "staff", "A&B <x>"))
        self.assertEqual(root.get("type"), "init")
        self.assertEqual(root.findtext("group-access"), "https://vpn.example.edu/staff")
        self.assertEqual(root.findtext("group-select"), "A&B <x>")
        self.assertEqual(root.findtext("capabilities/auth-method"), "single-sign-on-v2")
        self.assertIsNone(ET.fromstring(detect.init_request("vpn.example.edu")).find("group-select"))

    def test_probe_posts_with_the_anyconnect_headers(self):
        requests = []

        def opener(request, timeout):
            requests.append((request, timeout))
            return fixture("bremen.xml")

        d = detect.probe("VPN.Uni-Bremen.de", group="Tunnel-Uni-Bremen", opener=opener)
        request, timeout = requests[0]
        self.assertEqual(request.get_method(), "POST")
        self.assertEqual(request.full_url, "https://vpn.uni-bremen.de/")
        self.assertEqual(request.get_header("User-agent"), "AnyConnect Linux_64 5.1.18.314")
        self.assertEqual(request.get_header("X-aggregate-auth"), "1")
        self.assertEqual(request.get_header("X-transcend-version"), "1")
        self.assertIn(b"<group-select>Tunnel-Uni-Bremen</group-select>", request.data)
        self.assertEqual(timeout, 10)
        self.assertEqual(d.host, "vpn.uni-bremen.de")
        self.assertTrue(d.second_password)

    def test_unreachable_and_http_errors(self):
        def refused(request, timeout):
            raise urllib.error.URLError("Connection refused")

        def not_found(request, timeout):
            raise urllib.error.HTTPError(request.full_url, 404, "Not Found", {}, None)

        d = detect.probe("vpn.example.edu", opener=refused)
        self.assertFalse(d.reachable)
        self.assertIn("Can't reach vpn.example.edu", d.error)
        d = detect.probe("vpn.example.edu", opener=not_found)
        self.assertTrue(d.reachable)

    def test_invalid_input_raises_before_any_request(self):
        def opener(request, timeout):
            raise AssertionError("no request for invalid input")

        for host, group in (("vpn example", ""), ("vpn.example.edu", 'a"b'), ("", "")):
            with self.assertRaises(unis.FieldError):
                detect.probe(host, group=group, opener=opener)

    def test_split_address(self):
        self.assertEqual(detect.split_address("https://vpn.uni-muenster.de/exchange"), ("vpn.uni-muenster.de", "exchange"))
        self.assertEqual(detect.split_address(" vpn.Example.edu "), ("vpn.example.edu", ""))
        self.assertEqual(detect.split_address("http://vpn.example.edu/"), ("vpn.example.edu", ""))
        with self.assertRaises(unis.FieldError) as cm:
            detect.split_address("vpn.example.edu/a b")
        self.assertEqual(cm.exception.field, "usergroup")


class RedirectTests(unittest.TestCase):
    def test_redirect_posts_the_same_body_again(self):
        handler = detect._RepostRedirects()
        request = urllib.request.Request("https://vpn.uni-bremen.de/", data=b"<init/>", method="POST",
                                         headers={"User-Agent": "AnyConnect", "X-Aggregate-Auth": "1"})
        new = handler.redirect_request(request, None, 302, "Temporary moved", {}, "https://vpn3.uni-bremen.de/")
        self.assertEqual(new.get_method(), "POST")
        self.assertEqual(new.data, b"<init/>")
        self.assertEqual(new.host, "vpn3.uni-bremen.de")
        self.assertEqual(new.get_header("User-agent"), "AnyConnect")
        self.assertIsNone(new.get_header("Host"))

    def test_redirect_away_from_https_is_refused(self):
        handler = detect._RepostRedirects()
        request = urllib.request.Request("https://vpn.example.edu/", data=b"x", method="POST")
        self.assertIsNone(handler.redirect_request(request, None, 302, "Found", {}, "http://vpn.example.edu/"))
        self.assertEqual(handler.max_redirections, 3)


if __name__ == "__main__":
    unittest.main()
