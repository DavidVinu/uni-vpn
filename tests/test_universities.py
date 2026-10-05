import copy
import json
import unittest
from pathlib import Path

from uni_vpn import universities as unis

ENTRY = {"id": "example", "name": "Example University", "host": "vpn.example.edu"}


def registry_with(**changes):
    entry = dict(ENTRY, **changes)
    return {"version": 1, "universities": [entry]}


class RegistryTests(unittest.TestCase):
    def test_shipped_registry_loads(self):
        profiles = unis.load()
        self.assertEqual(set(profiles), {
            "heidelberg", "ethz", "bremen", "muenster", "marburg", "stanford", "harvard-fasrc", "stuttgart",
            "bonn", "mannheim", "kassel", "tu-dresden", "fu-berlin", "oxford"})

    def test_heidelberg_keeps_todays_values(self):
        hd = unis.get("heidelberg")
        self.assertEqual(hd.host, "vpn-ac.uni-heidelberg.de")
        self.assertEqual(hd.useragent, "AnyConnect Linux_64 5.1.18.314")
        self.assertEqual(hd.mfa, "totp_field")
        self.assertFalse(hd.no_external_auth, "today's command line has no --no-external-auth")
        self.assertEqual((hd.usergroup, hd.authgroup, hd.username_suffix, hd.os), ("", "", "", ""))
        self.assertEqual(hd.mfa_portal_url, "https://mfa.uni-heidelberg.de/")
        self.assertEqual(hd.default_domains,
                         ("sogo.uni-heidelberg.de", "elearning-med.uni-heidelberg.de", "cip.dmed.uni-heidelberg.de"))
        self.assertIn("{portal}", hd.mfa_steps[0])

    def test_only_heidelberg_is_verified(self):
        self.assertEqual([p.id for p in unis.registry().values() if p.verified], ["heidelberg"])

    def test_saml_universities_are_marked(self):
        self.assertEqual(sorted(p.id for p in unis.registry().values() if p.mfa == "saml"), ["fu-berlin", "oxford"])

    def test_profiles_from_the_probes(self):
        self.assertEqual(unis.get("marburg").authgroup, "unimr-vpn-staff-Passwort+2FA")
        self.assertEqual(unis.get("marburg").mfa, "totp_append")
        self.assertEqual(unis.get("stanford").authgroup, "Stanford")
        self.assertEqual(unis.get("stanford").mfa, "duo_push")
        self.assertEqual(unis.get("ethz").username_suffix, "@staff-net.ethz.ch")
        self.assertEqual(unis.get("kassel").os, "win")

    def test_new_entries_default_to_no_external_auth(self):
        self.assertTrue(unis.get("bonn").no_external_auth)
        self.assertTrue(unis.parse(registry_with())["example"].no_external_auth)

    def test_other_and_unknown_ids(self):
        self.assertEqual(unis.get("other").id, "other")
        self.assertEqual(unis.get("other").host, "")
        self.assertIsNone(unis.get("nowhere"))

    def test_public_list_is_sorted_and_has_no_notes(self):
        entries = unis.public_list()
        names = [e["name"].lower() for e in entries]
        self.assertEqual(names, sorted(names))
        self.assertFalse([e for e in entries if "notes" in e])
        json.dumps(entries)  # served as JSON as it is

    def test_search_ignores_case_and_umlauts_and_prefers_an_exact_id(self):
        self.assertEqual([p.id for p in unis.search("Zürich")], ["ethz"])
        self.assertEqual([p.id for p in unis.search("zuerich")], ["ethz"])
        self.assertEqual([p.id for p in unis.search("MÜNSTER")], ["muenster"])
        self.assertEqual([p.id for p in unis.search("fasrc")], ["harvard-fasrc"])
        self.assertEqual([p.id for p in unis.search("bonn")], ["bonn"])
        self.assertEqual(unis.search("   "), [])


class ReadmeTests(unittest.TestCase):
    def test_readme_lists_every_university(self):
        readme = (Path(__file__).parent.parent / "README.md").read_text(encoding="utf-8")
        for profile in unis.registry().values():
            self.assertIn(f"| {profile.name} | {profile.host} |", readme, profile.id)


class ParseTests(unittest.TestCase):
    def assert_refused(self, data, text):
        with self.assertRaises(ValueError) as cm:
            unis.parse(data)
        self.assertIn(text, str(cm.exception))

    def test_refuses_broken_entries(self):
        self.assert_refused(registry_with(mfa="sms"), "mfa")
        self.assert_refused(registry_with(host="not a host"), "VPN address")
        self.assert_refused(registry_with(colour="red"), "colour")
        self.assert_refused(registry_with(id="other"), "invalid id")
        self.assert_refused(registry_with(id="Bad_Id"), "invalid id")
        self.assert_refused(registry_with(default_domains=["localhost"]), "localhost")
        self.assert_refused(registry_with(no_external_auth="yes"), "true or false")
        self.assert_refused(registry_with(authgroup='a"b'), "quotes")
        self.assert_refused(registry_with(name=""), "'name' is required")
        self.assert_refused({"version": 2, "universities": []}, "version")

    def test_refuses_duplicate_ids(self):
        data = registry_with()
        data["universities"].append(copy.deepcopy(data["universities"][0]))
        self.assert_refused(data, "duplicate")


class CheckFieldTests(unittest.TestCase):
    def test_normalizes_and_names_the_field(self):
        self.assertEqual(unis.check_field("host", " VPN.Example.EDU. "), "vpn.example.edu")
        self.assertEqual(unis.check_field("usergroup", "/exchange/"), "exchange")
        self.assertEqual(unis.check_field("host", "vpn.example.edu:8443"), "vpn.example.edu:8443")
        self.assertEqual(unis.check_field("host", "https://vpn.example.edu/", strict=False), "https://vpn.example.edu/")
        for name, value in (("host", "vpn example"), ("host", "vpn.example.edu/staff"), ("mfa", "sms"), ("os", "beos"), ("usergroup", "a b"),
                            ("mfa_portal_url", "http://x.example"), ("authgroup", "a\nb"), ("useragent", 5),
                            ("no_external_auth", "true"), ("colour", "red")):
            with self.assertRaises(unis.FieldError) as cm:
                unis.check_field(name, value)
            self.assertEqual(cm.exception.field, name)

    def test_group_names_with_spaces_and_plus_are_fine(self):
        self.assertEqual(unis.check_field("authgroup", "RWTH-VPN (Split Tunnel)"), "RWTH-VPN (Split Tunnel)")
        self.assertEqual(unis.check_field("authgroup", "unimr-vpn-staff-Passwort+2FA"), "unimr-vpn-staff-Passwort+2FA")


if __name__ == "__main__":
    unittest.main()
