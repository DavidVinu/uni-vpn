# Other Cisco Universities Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** uni-vpn works for any Cisco AnyConnect university with a password login: a curated profile registry picked in setup, gateway detection for universities that are not listed, and per-profile openconnect flags and second factors, while Heidelberg installs keep working unchanged.

**Architecture:** `uni_vpn/universities.json` holds the profiles, `uni_vpn/universities.py` validates and searches them. `config.load()` copies the profile named by `university` (default `heidelberg`) into `Config` and applies overrides from `config.toml`. `Tunnel.auth_args()` and `Tunnel.stdin_bytes()` turn the profile into openconnect flags and stdin, the daemon reads the TOTP secret only when the mode needs it, and `uni_vpn/detect.py` probes a gateway's XML login form for the setup assistant's "Not listed" path.

**Tech Stack:** Python 3.11+ standard library only (`json`, `tomllib`, `urllib.request`, `xml.etree.ElementTree`, `unittest`), plain HTML and JavaScript in `uni_vpn/ui/index.html`, openconnect 9.x.

**Spec:** `docs/superpowers/specs/2026-10-05-multi-university-design.md`

## Global Constraints

- Python >= 3.11, no third-party packages, neither in `uni_vpn/` nor in `tests/`.
- Heidelberg stays byte for byte the same: a `config.toml` without `university` loads as Heidelberg, and its openconnect command line is exactly today's list (Task 3 has a test that compares the whole list).
- SAML is not implemented: `mfa = "saml"` is refused in setup, daemon and doctor with "This university signs in through a browser (SAML), uni-vpn does not support that yet".
- Secrets never on the command line: the TOTP secret goes through the 0600 token file (`totp_field`) or is turned into a code inside uni-vpn (`totp_append`).
- English only. No em-dashes anywhere (code, comments, UI, docs); use hyphens, commas or parentheses.
- Minimal user-facing text in the GUI; hints only behind the info icon.
- Commit messages: terse, lowercase, one line, no Co-Authored-By or any Claude attribution.
- Tests: `python3 -m unittest discover -s tests -t . -v` (as in `.github/workflows/ci.yml`) must pass after every task. Baseline on 2026-10-05 at `bf27028` (PR #4 head), Linux, Python 3.11.15: `Ran 340 tests ... OK (skipped=17)`. After Task 10: `Ran 442 tests ... OK (skipped=17)`.
- CI also runs `python3 -m compileall -q uni_vpn bin/uni-vpn tests`, `bash -n install.sh get.sh`, `./install.sh --dry-run --user citest` and the PowerShell parser on `install.ps1`; keep them green.
- Work in a branch off `claude/cross-platform-installers-vxzill` (PR #4), since this builds on its `ui/index.html`, `wintunnel.py` and installers.

## How the code blocks are meant

New files are given in full. Changes to existing files are given as unified diffs against the state after the previous task; apply them with `git apply` (save the block to a file) or by hand. Every diff was produced from a working implementation in which each task's tests failed before and passed after the task, and the whole suite passed after each task.

---

## File Structure

| Path | Responsibility | Task |
|---|---|---|
| `uni_vpn/universities.json` | The profile registry (data only) | 1 |
| `uni_vpn/universities.py` | `Profile`, `check_field`, `FieldError`, `parse`/`load`/`registry`/`get`, `public_list`, `search` | 1 |
| `uni_vpn/config.py` | `university` key, profile fields on `Config`, `needs_totp`, `login_name`, `profile_config`, `write_initial(..., university, overrides)`, `set_values` | 2 |
| `uni_vpn/tunnel.py` | `auth_args`, `token_args`, `stdin_bytes`, MFA-aware `Classifier`, generic messages | 3 |
| `uni_vpn/wintunnel.py` | uses `auth_args`/`token_args` instead of its own copy | 3 |
| `tests/fake_openconnect.py` | records every stdin line and the argv | 3 |
| `uni_vpn/daemon.py`, `uni_vpn/pac.py` | TOTP optional, SAML refusal, profile domains, status fields, `complete_setup(..., university, overrides)` | 4 |
| `uni_vpn/detect.py`, `tests/fixtures/detect/*.xml` | gateway probe and parser, recorded replies | 5 |
| `uni_vpn/httpapi.py` | `GET /universities.json`, `POST /api/detect`, `POST /api/setup` with profile | 6 |
| `uni_vpn/ui/index.html` | university step with search, detection, adaptive steps, portal link and steps from the profile | 7 |
| `uni_vpn/setup.py`, `uni_vpn/cli.py`, `install.sh`, `install.ps1` | terminal university question, `--university`, TOTP only when needed | 8 |
| `uni_vpn/doctor.py` | second-factor check by mode, openconnect version for `--no-external-auth` | 9 |
| `README.md` | generalized, table of universities | 10 |

---

### Task 1: University registry

**Files:**
- Create: `uni_vpn/universities.json`
- Create: `uni_vpn/universities.py`
- Test: `tests/test_universities.py`

**Interfaces:**
- Produces: `DEFAULT_ID = "heidelberg"`, `OTHER_ID = "other"`, `DEFAULT_USERAGENT`, `MFA_MODES`, `TOTP_MODES = ("totp_field", "totp_append")`, `OS_VALUES`, `PROFILE_FIELDS: dict[str, type]` (the ten overridable fields), `class FieldError(ValueError)` with `.field: str`, frozen dataclass `Profile` (fields as in the spec; tuples for `mfa_steps`, `default_domains`, `aliases`) with `public() -> dict`, `OTHER: Profile`, `check_field(name: str, value, strict: bool = True)` returning the normalized value, `parse(data: dict) -> dict[str, Profile]` (raises `ValueError`), `load(path=REGISTRY_FILE)`, `registry()` (cached), `get(uid: str) -> Profile | None` (`OTHER` for `"other"`), `public_list() -> list[dict]` (sorted by name, no `notes`), `fold(text) -> str`, `search(text) -> list[Profile]`.

- [ ] **Step 1: Write the failing test**

Create `tests/test_universities.py`:

```python
import copy
import json
import unittest

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
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `python3 -m unittest tests.test_universities -v`
Expected: FAIL with `ImportError: cannot import name 'universities' from 'uni_vpn'`.

- [ ] **Step 3: Write the registry**

Create `uni_vpn/universities.json`. Values come from `findings.md`, corrected by the gateway probes in spec section 2 (Marburg's group is `...Passwort+2FA`, Stanford needs the group `Stanford` because the default group is SAML). Heidelberg keeps `no_external_auth: false` so its command line does not change. `mfa_steps` carries the step list that `index.html` shows today.

```json
{
  "version": 1,
  "universities": [
    {
      "id": "heidelberg",
      "name": "Heidelberg University",
      "country": "DE",
      "host": "vpn-ac.uni-heidelberg.de",
      "useragent": "AnyConnect Linux_64 5.1.18.314",
      "no_external_auth": false,
      "mfa": "totp_field",
      "mfa_portal_url": "https://mfa.uni-heidelberg.de/",
      "mfa_steps": [
        "Open the {portal}. It only opens on campus or with Cisco VPN.",
        "Choose \"Soft-Token (zeitbasiert)\", then \"Einrichten\".",
        "Click \"Tokendetails einblenden\" and copy the line starting with otpauth://.",
        "Paste it below, then type the code shown here into the portal's \"Testen\" field."
      ],
      "default_domains": ["sogo.uni-heidelberg.de", "elearning-med.uni-heidelberg.de", "cip.dmed.uni-heidelberg.de"],
      "aliases": ["Universitaet Heidelberg", "Ruprecht-Karls-Universitaet", "URZ"],
      "verified": true,
      "notes": "Tested end to end, see docs/e2e.md. The OTP form comes after the password form."
    },
    {
      "id": "ethz",
      "name": "ETH Zurich",
      "country": "CH",
      "host": "sslvpn.ethz.ch",
      "authgroup": "staff-net",
      "username_suffix": "@staff-net.ethz.ch",
      "useragent": "AnyConnect",
      "mfa": "totp_field",
      "aliases": ["ETH Zuerich", "Eidgenoessische Technische Hochschule"],
      "notes": "Students: authgroup = \"student-net\", username_suffix = \"@student-net.ethz.ch\"."
    },
    {
      "id": "bremen",
      "name": "University of Bremen",
      "country": "DE",
      "host": "vpn.uni-bremen.de",
      "useragent": "AnyConnect",
      "mfa": "totp_field",
      "aliases": ["Universitaet Bremen"],
      "notes": "Without 2FA the second field stays empty, which uni-vpn cannot send yet."
    },
    {
      "id": "muenster",
      "name": "University of Muenster",
      "country": "DE",
      "host": "vpn.uni-muenster.de",
      "mfa": "totp_field",
      "aliases": ["Universitaet Muenster", "WWU"]
    },
    {
      "id": "marburg",
      "name": "University of Marburg",
      "country": "DE",
      "host": "vpn.uni-marburg.de",
      "authgroup": "unimr-vpn-staff-Passwort+2FA",
      "mfa": "totp_append",
      "aliases": ["Philipps-Universitaet Marburg"],
      "notes": "Students: authgroup = \"unimr-vpn-students-Passwort+2FA\"."
    },
    {
      "id": "stanford",
      "name": "Stanford University",
      "country": "US",
      "host": "su-vpn.stanford.edu",
      "authgroup": "Stanford",
      "mfa": "duo_push",
      "notes": "The default group CardinalKey is SAML; the group Stanford is a password form with Duo."
    },
    {
      "id": "harvard-fasrc",
      "name": "Harvard FAS Research Computing",
      "country": "US",
      "host": "vpn.rc.fas.harvard.edu",
      "username_suffix": "@fasrc",
      "mfa": "totp_field",
      "aliases": ["Harvard University", "FASRC"]
    },
    {
      "id": "stuttgart",
      "name": "University of Stuttgart",
      "country": "DE",
      "host": "vpn.tik.uni-stuttgart.de",
      "username_suffix": "@uni-stuttgart.de",
      "mfa": "none",
      "aliases": ["Universitaet Stuttgart"],
      "notes": "Students type the whole name, st123456@stud.uni-stuttgart.de."
    },
    {
      "id": "bonn",
      "name": "University of Bonn",
      "country": "DE",
      "host": "unibn-vpn.uni-bonn.de",
      "useragent": "AnyConnect",
      "mfa": "none",
      "aliases": ["Universitaet Bonn"]
    },
    {
      "id": "mannheim",
      "name": "University of Mannheim",
      "country": "DE",
      "host": "vpn.uni-mannheim.de",
      "mfa": "none",
      "aliases": ["Universitaet Mannheim"]
    },
    {
      "id": "kassel",
      "name": "University of Kassel",
      "country": "DE",
      "host": "univpn.uni-kassel.de",
      "useragent": "AnyConnect Windows 5.1.18.314",
      "os": "win",
      "mfa": "none",
      "aliases": ["Universitaet Kassel"],
      "notes": "Needs a Windows client identity; the exact user agent is a guess."
    },
    {
      "id": "tu-dresden",
      "name": "TU Dresden",
      "country": "DE",
      "host": "vpn2.zih.tu-dresden.de",
      "mfa": "none",
      "aliases": ["Technische Universitaet Dresden", "ZIH"]
    },
    {
      "id": "fu-berlin",
      "name": "Freie Universitaet Berlin",
      "country": "DE",
      "host": "vpn.fu-berlin.de",
      "useragent": "AnyConnectLinux",
      "mfa": "saml",
      "aliases": ["FU Berlin"]
    },
    {
      "id": "oxford",
      "name": "University of Oxford",
      "country": "GB",
      "host": "vpn.ox.ac.uk",
      "mfa": "saml"
    }
  ]
}
```

- [ ] **Step 4: Write the module**

Create `uni_vpn/universities.py`:

```python
"""University profiles (universities.json): gateway, groups, client quirks, second factor."""

from __future__ import annotations

import functools
import json
import re
import unicodedata
from dataclasses import dataclass, fields
from pathlib import Path

REGISTRY_FILE = Path(__file__).resolve().parent / "universities.json"
DEFAULT_ID = "heidelberg"  # config.toml without "university"
OTHER_ID = "other"  # a university that is not listed: everything comes from config.toml
DEFAULT_USERAGENT = "AnyConnect Linux_64 5.1.18.314"
MFA_MODES = ("none", "totp_field", "totp_append", "duo_push", "saml")
TOTP_MODES = ("totp_field", "totp_append")
OS_VALUES = ("", "linux", "linux-64", "win", "mac-intel", "android", "apple-ios")
OS_CHOICES = ", ".join(v or '""' for v in OS_VALUES)
# Profile fields that config.toml may override, with their type.
PROFILE_FIELDS: dict[str, type] = {
    "host": str, "usergroup": str, "authgroup": str, "username_suffix": str, "useragent": str,
    "os": str, "no_external_auth": bool, "mfa": str, "totp_separator": str, "mfa_portal_url": str,
}

ID_RE = re.compile(r"^[a-z0-9][a-z0-9-]{0,39}$")
_LABEL = r"[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?"
DOMAIN_RE = re.compile(rf"^(?=.{{1,253}}$){_LABEL}(?:\.{_LABEL})+$")
HOST_RE = re.compile(rf"^(?=.{{1,253}}(?::|$)){_LABEL}(?:\.{_LABEL})+(?::[0-9]{{1,5}})?$")
# config.toml written by hand before profiles existed: anything openconnect accepts (a URL too).
LOOSE_HOST_RE = re.compile(r'^[^\s"\\]{1,255}$')
USERGROUP_RE = re.compile(r"^[A-Za-z0-9._~+-]{0,64}(?:/[A-Za-z0-9._~+-]{1,64}){0,3}$")
# Printable, no quotes or backslashes: these go into argv and config.toml as they are.
TEXT_RE = re.compile(r'^[^\x00-\x1f\x7f"\\]{0,128}$')
URL_RE = re.compile(r"^https://[A-Za-z0-9.-]+(?::[0-9]{1,5})?(?:/[A-Za-z0-9._~%/?&=+-]*)?$")


class FieldError(ValueError):
    """A profile value that is not allowed; .field names it for the setup assistant."""

    def __init__(self, field: str, message: str):
        super().__init__(message)
        self.field = field


@dataclass(frozen=True)
class Profile:
    id: str
    name: str
    country: str = ""
    host: str = ""
    usergroup: str = ""
    authgroup: str = ""
    username_suffix: str = ""
    useragent: str = DEFAULT_USERAGENT
    os: str = ""
    no_external_auth: bool = True
    mfa: str = "none"
    totp_separator: str = ""
    mfa_portal_url: str = ""
    mfa_steps: tuple[str, ...] = ()
    default_domains: tuple[str, ...] = ()
    aliases: tuple[str, ...] = ()
    verified: bool = False
    notes: str = ""

    def public(self) -> dict:
        """What the setup assistant gets: everything except the notes for contributors."""
        data = {f.name: getattr(self, f.name) for f in fields(self) if f.name != "notes"}
        for key in ("mfa_steps", "default_domains", "aliases"):
            data[key] = list(data[key])
        return data


OTHER = Profile(id=OTHER_ID, name="Other university")


def check_field(name: str, value, strict: bool = True):
    """Validate one profile value; returns it normalized. Raises FieldError.

    strict=False is for config.toml: a host written by hand before profiles existed may be
    anything openconnect accepts (a URL, a single name), so only whitespace and quotes are refused."""
    kind = PROFILE_FIELDS.get(name)
    if kind is None:
        raise FieldError(name, f"unknown setting '{name}'")
    if not isinstance(value, kind) or (kind is not bool and isinstance(value, bool)):
        raise FieldError(name, f"'{name}' must be {'true or false' if kind is bool else 'text'}")
    if kind is bool:
        return value
    if name == "host" and not strict:
        if not LOOSE_HOST_RE.fullmatch(value):
            raise FieldError(name, "'host' must not be empty or contain spaces or quotes")
    elif name == "host":
        value = value.strip().lower().rstrip(".")
        if not HOST_RE.fullmatch(value):
            raise FieldError(name, "Enter the VPN address, for example vpn.example.edu")
    elif name == "usergroup":
        value = value.strip().strip("/")
        if not USERGROUP_RE.fullmatch(value):
            raise FieldError(name, "The path after the address may only contain letters, digits and . _ ~ + -")
    elif name == "mfa":
        if value not in MFA_MODES:
            raise FieldError(name, f"'mfa' must be one of {', '.join(MFA_MODES)}")
    elif name == "os":
        if value not in OS_VALUES:
            raise FieldError(name, f"'os' must be one of {OS_CHOICES}")
    elif name == "mfa_portal_url":
        if value and not URL_RE.fullmatch(value):
            raise FieldError(name, "'mfa_portal_url' must be an https:// address")
    elif not TEXT_RE.fullmatch(value):
        raise FieldError(name, f"'{name}' must not contain quotes, backslashes or line breaks")
    return value


def parse(data: dict) -> dict[str, Profile]:
    """The registry from the decoded JSON. Raises ValueError naming the entry."""
    if not isinstance(data, dict) or data.get("version") != 1 or not isinstance(data.get("universities"), list):
        raise ValueError("universities.json: expected {\"version\": 1, \"universities\": [...]}")
    known = {f.name for f in fields(Profile)}
    profiles: dict[str, Profile] = {}
    for number, entry in enumerate(data["universities"], start=1):
        where = f"universities.json entry {number}"
        if not isinstance(entry, dict):
            raise ValueError(f"{where}: not an object")
        unknown = set(entry) - known
        if unknown:
            raise ValueError(f"{where}: unknown key {sorted(unknown)[0]!r}")
        uid = entry.get("id")
        if not isinstance(uid, str) or not ID_RE.fullmatch(uid) or uid == OTHER_ID:
            raise ValueError(f"{where}: invalid id {uid!r}")
        if uid in profiles:
            raise ValueError(f"{where}: duplicate id {uid!r}")
        values = dict(entry)
        for key in ("name", "host"):
            if not isinstance(values.get(key), str) or not values[key].strip():
                raise ValueError(f"{uid}: '{key}' is required")
        for key in ("country", "notes"):
            if not isinstance(values.get(key, ""), str):
                raise ValueError(f"{uid}: '{key}' must be text")
        if not isinstance(values.get("verified", False), bool):
            raise ValueError(f"{uid}: 'verified' must be true or false")
        for key in ("mfa_steps", "default_domains", "aliases"):
            items = values.get(key, [])
            if not isinstance(items, list) or not all(isinstance(i, str) for i in items):
                raise ValueError(f"{uid}: '{key}' must be a list of text")
            values[key] = tuple(items)
        for domain in values["default_domains"]:
            if not DOMAIN_RE.fullmatch(domain):
                raise ValueError(f"{uid}: '{domain}' is not a host name")
        try:
            for key in PROFILE_FIELDS:
                if key in values:
                    values[key] = check_field(key, values[key])
        except FieldError as exc:
            raise ValueError(f"{uid}: {exc}") from None
        profiles[uid] = Profile(**values)
    return profiles


def load(path: Path = REGISTRY_FILE) -> dict[str, Profile]:
    return parse(json.loads(path.read_text(encoding="utf-8")))


@functools.lru_cache(maxsize=1)
def registry() -> dict[str, Profile]:
    return load()


def get(uid: str) -> Profile | None:
    """The profile for an id from config.toml; OTHER for "other", None if unknown."""
    if uid == OTHER_ID:
        return OTHER
    return registry().get(uid)


def public_list() -> list[dict]:
    return [p.public() for p in sorted(registry().values(), key=lambda p: p.name.lower())]


def fold(text: str) -> str:
    """Lower case without accents, so "zuerich", "zurich" and "Zürich" find the same entries."""
    plain = unicodedata.normalize("NFKD", text).encode("ascii", "ignore").decode()
    return plain.lower().replace("ue", "u").replace("oe", "o").replace("ae", "a")


def search(text: str) -> list[Profile]:
    """Profiles whose id, name, alias or host contains the text; an exact id wins."""
    query = fold(text.strip())
    if not query:
        return []
    if query in registry():
        return [registry()[query]]
    return [p for p in sorted(registry().values(), key=lambda p: p.name.lower())
            if any(query in fold(t) for t in (p.id, p.name, p.host, *p.aliases))]
```

Note: `OS_CHOICES` is precomputed because Python 3.11 does not allow a backslash inside an f-string expression.

- [ ] **Step 5: Run the tests**

Run: `python3 -m unittest tests.test_universities -v`
Expected: PASS (13 tests).

- [ ] **Step 6: Run the whole suite**

Run: `python3 -m unittest discover -s tests -t .`
Expected: `OK (skipped=17)`.

- [ ] **Step 7: Commit**

```bash
git add uni_vpn/universities.json uni_vpn/universities.py tests/test_universities.py
git commit -m "add university profile registry"
```

---

### Task 2: Profiles in config.toml

**Files:**
- Modify: `uni_vpn/config.py`
- Test: `tests/test_config.py`

**Interfaces:**
- Consumes: `universities.get`, `check_field(..., strict=False)` for `config.toml`, `check_field` (strict) for setup input, `FieldError`, `PROFILE_FIELDS`, `TOTP_MODES`, `DEFAULT_USERAGENT`.
- Produces: `Config` gains `university`, `university_name`, `usergroup`, `authgroup`, `username_suffix`, `os`, `no_external_auth`, `mfa`, `totp_separator`, `mfa_portal_url`, `mfa_steps: list[str]`, `default_domains: list[str]` (defaults equal the Heidelberg profile); properties `needs_totp -> bool` and `login_name -> str`. Module functions: `apply_profile(cfg, profile)`, `copy_profile(target: Config, source: Config)`, `check_overrides(values: dict) -> dict` (raises `FieldError`), `profile_config(university: str, overrides: dict | None = None) -> Config` (raises `FieldError` with field `university` or `host`), `toml_value(value) -> str`, `write_initial(path, user, university=DEFAULT_ID, overrides=None)`, `set_values(path, values: dict)`; `set_user` now delegates to `set_values`; `valid_user` accepts `name@realm`.

Notes for the implementer:
- `load()` reads `university` first, wherever it stands in the file, applies the profile, then the other keys. A `host` in `config.toml` is only checked loosely (`strict=False`) so hand-written values such as `https://vpn-ac.uni-heidelberg.de/` keep working; setup input is checked strictly.
- The new template no longer writes `host`: a listed university gets its host from the registry, so a registry fix reaches existing installs. Old files still contain `host = "vpn-ac.uni-heidelberg.de"`, which equals the profile.
- `set_values` keeps the line-preserving approach of the old `set_user` and still refuses forms it cannot rewrite (checked with `tomllib` before writing).

- [ ] **Step 1: Write the failing tests**

Apply to `tests/test_config.py`:

````diff
diff --git a/tests/test_config.py b/tests/test_config.py
--- a/tests/test_config.py
+++ b/tests/test_config.py
@@ -3,6 +3,7 @@ import unittest
 from pathlib import Path
 
 from uni_vpn import config
+from uni_vpn import universities as unis
 
 
 class LoadTests(unittest.TestCase):
@@ -62,6 +63,125 @@ class LoadTests(unittest.TestCase):
         self.assertEqual(cfg.path, path)
 
 
+class UniversityTests(unittest.TestCase):
+    def write(self, text):
+        path = Path(tempfile.mkdtemp()) / "config.toml"
+        path.write_text(text, encoding="utf-8")
+        return path
+
+    def test_without_university_it_is_heidelberg(self):
+        # Every config.toml written before profiles existed looks like this.
+        cfg = config.load(self.write('host = "vpn-ac.uni-heidelberg.de"\nuser = "ab123"\n'))
+        self.assertEqual(cfg.university, "heidelberg")
+        self.assertEqual(cfg.mfa, "totp_field")
+        self.assertTrue(cfg.needs_totp)
+        self.assertFalse(cfg.no_external_auth)
+        self.assertEqual(cfg.useragent, "AnyConnect Linux_64 5.1.18.314")
+        self.assertEqual(cfg.mfa_portal_url, "https://mfa.uni-heidelberg.de/")
+        self.assertEqual(cfg.login_name, "ab123")
+
+    def test_default_config_equals_the_heidelberg_profile(self):
+        reference = config.Config()
+        config.apply_profile(reference, unis.get("heidelberg"))
+        self.assertEqual(config.Config(), reference)
+
+    def test_profile_values_apply(self):
+        cfg = config.load(self.write('university = "ethz"\nuser = "jdoe"\n'))
+        self.assertEqual(cfg.host, "sslvpn.ethz.ch")
+        self.assertEqual(cfg.authgroup, "staff-net")
+        self.assertEqual(cfg.useragent, "AnyConnect")
+        self.assertTrue(cfg.no_external_auth)
+        self.assertEqual(cfg.login_name, "jdoe@staff-net.ethz.ch")
+        self.assertEqual(cfg.university_name, "ETH Zurich")
+        self.assertEqual(cfg.default_domains, [])
+
+    def test_overrides_win_wherever_they_stand(self):
+        cfg = config.load(self.write('user = "jdoe"\nauthgroup = "student-net"\nuniversity = "ethz"\n'
+                                     'username_suffix = "@student-net.ethz.ch"\nno_external_auth = false\n'))
+        self.assertEqual(cfg.authgroup, "student-net")
+        self.assertEqual(cfg.login_name, "jdoe@student-net.ethz.ch")
+        self.assertFalse(cfg.no_external_auth)
+
+    def test_a_typed_realm_replaces_the_suffix(self):
+        cfg = config.load(self.write('university = "stuttgart"\nuser = "st123456@stud.uni-stuttgart.de"\n'))
+        self.assertEqual(cfg.login_name, "st123456@stud.uni-stuttgart.de")
+        self.assertFalse(cfg.needs_totp)
+
+    def test_unknown_university_reports_its_line(self):
+        with self.assertRaises(config.ConfigError) as cm:
+            config.load(self.write('user = "a"\nuniversity = "nowhere"\n'))
+        self.assertEqual(cm.exception.line, 2)
+        self.assertIn("nowhere", str(cm.exception))
+
+    def test_bad_profile_values_report_their_line(self):
+        for line in ('mfa = "sms"', 'os = "beos"', "no_external_auth = 1", 'authgroup = "a\\"b"', 'host = "a b"'):
+            with self.assertRaises(config.ConfigError, msg=line) as cm:
+                config.load(self.write(f'user = "a"\n{line}\n'))
+            self.assertEqual(cm.exception.line, 2, line)
+
+    def test_hand_written_hosts_keep_working(self):
+        cfg = config.load(self.write('user = "a"\nhost = "https://vpn-ac.uni-heidelberg.de/"\n'))
+        self.assertEqual(cfg.host, "https://vpn-ac.uni-heidelberg.de/")
+
+    def test_other_needs_a_host(self):
+        with self.assertRaises(config.ConfigError) as cm:
+            config.load(self.write('university = "other"\nuser = "a"\n'))
+        self.assertIn("host", str(cm.exception))
+        cfg = config.load(self.write('university = "other"\nuser = "a"\nhost = "vpn.example.edu"\n'))
+        self.assertEqual(cfg.mfa, "none")
+        self.assertTrue(cfg.no_external_auth)
+
+    def test_user_with_a_realm_is_valid(self):
+        self.assertTrue(config.valid_user("st123456@stud.uni-stuttgart.de"))
+        for user in ("a@", "@b", "a@b@c", 'a"b', "a b", "a@b c"):
+            self.assertFalse(config.valid_user(user), user)
+
+    def test_write_initial_writes_university_and_overrides(self):
+        path = Path(tempfile.mkdtemp()) / "config.toml"
+        config.write_initial(path, user="ab123", university="other",
+                             overrides={"host": "VPN.Example.edu", "authgroup": "Staff (Split)", "mfa": "none",
+                                        "no_external_auth": False})
+        text = path.read_text()
+        self.assertIn('university = "other"', text)
+        cfg = config.load(path)
+        self.assertEqual((cfg.host, cfg.authgroup, cfg.mfa, cfg.no_external_auth),
+                         ("vpn.example.edu", "Staff (Split)", "none", False))
+
+    def test_write_initial_for_a_listed_university_writes_no_host(self):
+        # Without a host line a fix in universities.json reaches existing installs.
+        path = Path(tempfile.mkdtemp()) / "config.toml"
+        config.write_initial(path, user="ab123", university="bonn")
+        self.assertNotIn("host", path.read_text())
+        self.assertEqual(config.load(path).host, "unibn-vpn.uni-bonn.de")
+
+    def test_write_initial_refuses_bad_profiles(self):
+        path = Path(tempfile.mkdtemp()) / "config.toml"
+        for university, overrides in (("nowhere", {}), ("other", {}), ("other", {"host": "vpn.example.edu", "mfa": "sms"})):
+            with self.assertRaises(config.ConfigError):
+                config.write_initial(path, user="ab123", university=university, overrides=overrides)
+        self.assertFalse(path.exists())
+
+    def test_profile_config_names_the_field(self):
+        with self.assertRaises(unis.FieldError) as cm:
+            config.profile_config("other", {"host": "vpn.example.edu", "authgroup": "a\nb"})
+        self.assertEqual(cm.exception.field, "authgroup")
+        with self.assertRaises(unis.FieldError) as cm:
+            config.profile_config("nowhere")
+        self.assertEqual(cm.exception.field, "university")
+
+
+class SetValuesTests(unittest.TestCase):
+    def test_sets_several_keys_and_keeps_the_rest(self):
+        path = Path(tempfile.mkdtemp()) / "config.toml"
+        path.write_text('# mine\nuser = "ab1"  # comment\nno_external_auth = true\n[timing]\ntick = 5\n', encoding="utf-8")
+        config.set_values(path, {"user": "cd2", "university": "bonn", "no_external_auth": False})
+        text = path.read_text()
+        self.assertIn("# mine", text)
+        self.assertIn("# comment", text)
+        cfg = config.load(path)
+        self.assertEqual((cfg.user, cfg.university, cfg.no_external_auth, cfg.tick), ("cd2", "bonn", False, 5))
+
+
 class SetUserTests(unittest.TestCase):
     def test_unknown_forms_are_refused_instead_of_breaking_the_file(self):
         for line in ('user = """ab1"""', '"user" = "ab1"'):
````

- [ ] **Step 2: Run them to make sure they fail**

Run: `python3 -m unittest tests.test_config -v`
Expected: FAIL, among others `ConfigError: config.toml line 1: unknown key 'university'` and `TypeError: write_initial() got an unexpected keyword argument 'university'`.

- [ ] **Step 3: Implement**

Apply to `uni_vpn/config.py`:

````diff
diff --git a/uni_vpn/config.py b/uni_vpn/config.py
--- a/uni_vpn/config.py
+++ b/uni_vpn/config.py
@@ -2,13 +2,18 @@
 
 from __future__ import annotations
 
+import json
 import re
 import tomllib
 from dataclasses import dataclass, field
 from pathlib import Path
 
+from . import universities as unis
+
 DEFAULT_HOST = "vpn-ac.uni-heidelberg.de"
-DEFAULT_USERAGENT = "AnyConnect Linux_64 5.1.18.314"
+DEFAULT_USERAGENT = unis.DEFAULT_USERAGENT
+# Without "university" in config.toml everything is as before: Heidelberg.
+_DEFAULT = unis.get(unis.DEFAULT_ID)
 
 
 class ConfigError(Exception):
@@ -24,12 +29,25 @@ class ConfigError(Exception):
 
 @dataclass
 class Config:
-    host: str = DEFAULT_HOST
+    host: str = _DEFAULT.host
     user: str = ""
     idle_minutes: float = 15.0
     socks_port: int = 1080
     http_port: int = 1081
-    useragent: str = DEFAULT_USERAGENT
+    useragent: str = _DEFAULT.useragent
+    # Profile (universities.json), each field can be overridden in config.toml
+    university: str = unis.DEFAULT_ID
+    university_name: str = _DEFAULT.name
+    usergroup: str = _DEFAULT.usergroup
+    authgroup: str = _DEFAULT.authgroup
+    username_suffix: str = _DEFAULT.username_suffix
+    os: str = _DEFAULT.os
+    no_external_auth: bool = _DEFAULT.no_external_auth
+    mfa: str = _DEFAULT.mfa
+    totp_separator: str = _DEFAULT.totp_separator
+    mfa_portal_url: str = _DEFAULT.mfa_portal_url
+    mfa_steps: list[str] = field(default_factory=lambda: list(_DEFAULT.mfa_steps))
+    default_domains: list[str] = field(default_factory=lambda: list(_DEFAULT.default_domains))
     openconnect: str | None = None
     ocproxy: str | None = None
     # [timing], all in seconds
@@ -45,6 +63,55 @@ class Config:
     backoff: list[float] = field(default_factory=lambda: [5, 10, 20, 40, 80, 300])
     path: Path | None = None
 
+    @property
+    def needs_totp(self) -> bool:
+        return self.mfa in unis.TOTP_MODES
+
+    @property
+    def login_name(self) -> str:
+        """The user name openconnect sends: with the profile's realm unless the user typed one."""
+        if "@" in self.user or not self.username_suffix:
+            return self.user
+        return self.user + self.username_suffix
+
+
+PROFILE_ATTRS = ("university", "university_name", *unis.PROFILE_FIELDS, "mfa_steps", "default_domains")
+
+
+def apply_profile(cfg: Config, profile: unis.Profile) -> None:
+    cfg.university = profile.id
+    cfg.university_name = profile.name
+    for name in unis.PROFILE_FIELDS:
+        setattr(cfg, name, getattr(profile, name))
+    cfg.mfa_steps = list(profile.mfa_steps)
+    cfg.default_domains = list(profile.default_domains)
+
+
+def copy_profile(target: Config, source: Config) -> None:
+    """Take over university and profile fields, for example after the setup assistant wrote them."""
+    for name in PROFILE_ATTRS:
+        value = getattr(source, name)
+        setattr(target, name, list(value) if isinstance(value, list) else value)
+
+
+def check_overrides(values: dict) -> dict:
+    """Profile fields from the setup assistant or config.toml, normalized. Raises unis.FieldError."""
+    return {key: unis.check_field(key, value) for key, value in values.items()}
+
+
+def profile_config(university: str, overrides: dict | None = None) -> Config:
+    """The profile part of a Config for a university id plus overrides. Raises unis.FieldError."""
+    profile = unis.get(university) if isinstance(university, str) else None
+    if profile is None:
+        raise unis.FieldError("university", f"Unknown university {university!r}")
+    cfg = Config(user="")
+    apply_profile(cfg, profile)
+    for key, value in check_overrides(overrides or {}).items():
+        setattr(cfg, key, value)
+    if not cfg.host:
+        raise unis.FieldError("host", "Enter the VPN address, for example vpn.example.edu")
+    return cfg
+
 
 _NUMBER = (int, float)
 TOP_KEYS: dict[str, tuple[type, ...]] = {
@@ -56,6 +123,8 @@ TOP_KEYS: dict[str, tuple[type, ...]] = {
     "useragent": (str,),
     "openconnect": (str,),
     "ocproxy": (str,),
+    "university": (str,),
+    **{key: (kind,) for key, kind in unis.PROFILE_FIELDS.items()},
 }
 TIMING_KEYS: dict[str, tuple[type, ...]] = {
     "ready_timeout": _NUMBER,
@@ -109,6 +178,13 @@ def load(path: Path | None = None) -> Config:
         raise ConfigError(str(exc), line=int(match.group(1)) if match else None) from None
 
     cfg = Config(path=path)
+    university = data.get("university", unis.DEFAULT_ID)
+    _check("university", university, (str,), text, None)
+    profile = unis.get(university)
+    if profile is None:
+        raise ConfigError(f"unknown university '{university}', see uni_vpn/universities.json",
+                          line=_line_of_key(text, "university"))
+    apply_profile(cfg, profile)
     for key, value in data.items():
         if key == "timing":
             if not isinstance(value, dict):
@@ -124,10 +200,19 @@ def load(path: Path | None = None) -> Config:
         if key not in TOP_KEYS:
             raise ConfigError(f"unknown key '{key}'", line=_line_of_key(text, key))
         _check(key, value, TOP_KEYS[key], text, None)
+        if key == "university":
+            continue
+        if key in unis.PROFILE_FIELDS:
+            try:
+                value = unis.check_field(key, value, strict=False)
+            except unis.FieldError as exc:
+                raise ConfigError(str(exc), line=_line_of_key(text, key)) from None
         setattr(cfg, key, value)
 
     if not cfg.user:
         raise ConfigError("'user' (university ID) is missing")
+    if not cfg.host:
+        raise ConfigError("'host' is missing, it is needed with university = \"other\"")
     for name in ("socks_port", "http_port"):
         port = getattr(cfg, name)
         if not 1 <= port <= 65535:
@@ -142,26 +227,39 @@ def load(path: Path | None = None) -> Config:
 
 
 TEMPLATE = """# uni-vpn configuration
-host = "{host}"
+university = "{university}"  # profile from uni_vpn/universities.json, "other" if not listed
 user = "{user}"          # university ID
-idle_minutes = 15        # tear the tunnel down after this many minutes without traffic
+{overrides}idle_minutes = 15        # tear the tunnel down after this many minutes without traffic
 socks_port = 1080        # SOCKS5 proxy for the browser
 http_port = 1081         # status page http://127.0.0.1:1081
-# useragent = "{useragent}"
 # openconnect = "/usr/sbin/openconnect"
 # ocproxy = "/usr/bin/ocproxy"
 """
 
 
-def write_initial(path: Path, user: str, host: str = DEFAULT_HOST) -> None:
+def toml_value(value) -> str:
+    if isinstance(value, bool):
+        return "true" if value else "false"
+    # A JSON string is a valid TOML basic string.
+    return json.dumps(str(value), ensure_ascii=False)
+
+
+def write_initial(path: Path, user: str, university: str = unis.DEFAULT_ID, overrides: dict | None = None) -> None:
     if not valid_user(user):
         raise ConfigError(f"invalid university ID: {user!r}")
+    try:
+        overrides = check_overrides(overrides or {})
+        profile_config(university, overrides)
+    except unis.FieldError as exc:
+        raise ConfigError(str(exc)) from None
+    lines = "".join(f"{key} = {toml_value(value)}\n" for key, value in overrides.items())
     path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
-    path.write_text(TEMPLATE.format(host=host, user=user, useragent=DEFAULT_USERAGENT), encoding="utf-8")
+    path.write_text(TEMPLATE.format(university=university, user=user, overrides=lines), encoding="utf-8")
     path.chmod(0o600)
 
 
-USER_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
+# "ab123", or with a realm the university asks for: "st123456@stud.uni-stuttgart.de"
+USER_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,63}(?:@[A-Za-z0-9][A-Za-z0-9.-]{0,252})?$")
 
 
 def valid_user(user: str) -> bool:
@@ -172,21 +270,32 @@ def set_user(path: Path, user: str) -> None:
     """Write a new university ID into an existing config.toml, keeping everything else."""
     if not valid_user(user):
         raise ConfigError(f"invalid university ID: {user!r}")
-    text = path.read_text(encoding="utf-8")
-    # A basic ("...") or literal ('...') string, as written by hand.
-    new, count = re.subn(r"""(?m)^(\s*user\s*=\s*)(?:"(?:[^"\\\n]|\\.)*"|'[^'\n]*')""",
-                         lambda m: f'{m.group(1)}"{user}"', text, count=1)
-    if not count:
-        head, sep, rest = text.partition("\n[")
-        new = head.rstrip("\n") + f'\nuser = "{user}"\n' + (sep.lstrip("\n") and "\n[" + rest)
+    set_values(path, {"user": user})
+
+
+# A basic ("...") or literal ('...') string or a boolean, as written by hand.
+_VALUE = r"""(?:"(?:[^"\\\n]|\\.)*"|'[^'\n]*'|true|false)"""
+
+
+def set_values(path: Path, values: dict) -> None:
+    """Set top-level keys in an existing config.toml, keeping comments, order and everything else."""
+    text = new = path.read_text(encoding="utf-8")
+    for key, value in values.items():
+        literal = toml_value(value)
+        new, count = re.subn(rf"(?m)^(\s*{re.escape(key)}\s*=\s*){_VALUE}",
+                             lambda m: m.group(1) + literal, new, count=1)
+        if not count:
+            head, sep, rest = new.partition("\n[")
+            new = head.rstrip("\n") + f"\n{key} = {literal}\n" + (sep.lstrip("\n") and "\n[" + rest)
     # Forms the regex does not know ('''...''', "user" = ...) must not end in a broken file.
     try:
-        written = tomllib.loads(new).get("user")
+        written = tomllib.loads(new)
     except tomllib.TOMLDecodeError:
-        written = None
-    if written != user:
-        raise ConfigError(f"could not change the university ID in {path}, please edit it by hand")
-    path.write_text(new, encoding="utf-8")
+        written = {}
+    if any(written.get(key) != value for key, value in values.items()):
+        raise ConfigError(f"could not change {', '.join(values)} in {path}, please edit it by hand")
+    if new != text:
+        path.write_text(new, encoding="utf-8")
 
 
 _PORT_LINE = re.compile(r"^\s*(socks_port|http_port)\s*=\s*([0-9]{1,5})\s*(#.*)?$")
````

- [ ] **Step 4: Run the tests**

Run: `python3 -m unittest tests.test_config -v`
Expected: PASS (27 tests).

- [ ] **Step 5: Run the whole suite**

Run: `python3 -m unittest discover -s tests -t .`
Expected: `OK (skipped=17)`. The setup, daemon and doctor tests still pass because `Config()` equals the Heidelberg profile.

- [ ] **Step 6: Commit**

```bash
git add uni_vpn/config.py tests/test_config.py
git commit -m "config: university profile with per-field overrides"
```

---

### Task 3: openconnect command, stdin and error texts per profile

**Files:**
- Modify: `uni_vpn/tunnel.py`
- Modify: `uni_vpn/wintunnel.py`
- Modify: `tests/fake_openconnect.py`
- Test: `tests/test_tunnel.py`, `tests/test_wintunnel.py`

**Interfaces:**
- Consumes: `Config.mfa`, `Config.login_name`, `Config.authgroup`, `Config.usergroup`, `Config.os`, `Config.no_external_auth`, `Config.totp_separator`; `totp.code(token) -> str`.
- Produces: `Tunnel.auth_args() -> list[str]`, `Tunnel.token_args() -> list[str]`, `Tunnel.stdin_bytes(password: bytes, totp: str | None) -> bytes` (sets `otp_generated_at` for `totp_append`, raises `ValueError` without a secret there), `Classifier(mfa: str = "totp_field")`, constants `APPEND_REJECTED`, `DUO_REJECTED`, `LOGIN_REJECTED`, `SAML_REQUIRED`, `HOSTSCAN_REQUIRED`; `tunnel.totp_mod` is the imported `totp` module (tests patch `tunnel.totp_mod.code`). The fake openconnect writes every stdin line to `FAKE_PASSWORD_FILE` and the argv as one JSON list per start to `FAKE_ARGS_FILE`.

Notes for the implementer:
- Flag order matters for the Heidelberg regression test: `--protocol`, `--useragent`, `--user`, `--passwd-on-stdin`, `--non-inter`, then the optional profile flags, then the transport flags, `--script...`, token flags, host.
- `duo_push` drops `--non-inter`: openconnect reads only the first stdin line for `--passwd-on-stdin` and asks for the second password through its prompt, which `--non-inter` refuses. stdin is closed right after writing, so any further prompt ends at EOF instead of hanging. Unverified against a real Duo gateway (spec, open points).
- Only `totp_field` writes the token file. `totp_append` computes the code at start and sends `password + separator + code` as the one stdin line.
- The fake now reads stdin to EOF (`sys.stdin.read()`); that works because `Tunnel.start` closes stdin after writing.

- [ ] **Step 1: Write the failing tests**

Apply to `tests/test_tunnel.py`:

````diff
diff --git a/tests/test_tunnel.py b/tests/test_tunnel.py
--- a/tests/test_tunnel.py
+++ b/tests/test_tunnel.py
@@ -82,6 +82,32 @@ class ClassifyTests(unittest.TestCase):
         self.assertIn("uni-vpn totp", message)
         self.assertNotIn("uni-vpn password", message)
 
+    def test_login_failed_is_worded_by_the_second_factor(self):
+        for mfa, expected in (("none", tn.PASSWORD_REJECTED), ("totp_field", tn.PASSWORD_REJECTED),
+                              ("totp_append", tn.APPEND_REJECTED), ("duo_push", tn.DUO_REJECTED)):
+            seq = tn.Classifier(mfa)
+            self.assertEqual(seq.feed("Login failed."), ("auth_failed", expected), mfa)
+        self.assertIn("clock", tn.APPEND_REJECTED)
+        self.assertIn("Duo", tn.DUO_REJECTED)
+        for message in (tn.APPEND_REJECTED, tn.DUO_REJECTED):
+            self.assertIn("uni-vpn password", message)
+            self.assertNotIn("totp", message.lower(), "the page would offer the TOTP fix")
+
+    def test_unsupported_login_methods_say_so_without_naming_a_university(self):
+        for line in ("SAML authentication required", "Opening external browser for authentication"):
+            state, message = tn.classify_line(line)
+            self.assertEqual(state, "auth_failed")
+            self.assertIn("browser", message)
+            self.assertIn("not support", message)
+        self.assertIn("not support", tn.classify_line("Error: Server asked us to run CSD hostscan.")[1])
+        for _needle, _state, message in tn.MARKERS:
+            self.assertNotIn("Heidelberg", message)
+            self.assertNotIn("needs an update", message)
+
+    def test_tunnel_classifier_follows_the_profile(self):
+        t = tn.Tunnel(Config(user="u", mfa="duo_push"), FAKE, WRAPPER, logging.getLogger("t"), token_dir=Path(tempfile.mkdtemp()))
+        self.assertEqual(t.classifier.mfa, "duo_push")
+
     def test_classifier_keeps_first_verdict(self):
         seq = tn.Classifier()
         seq.feed("Generating OATH TOTP token code")
@@ -90,6 +116,52 @@ class ClassifyTests(unittest.TestCase):
         self.assertEqual(seq.verdict, first)
 
 
+TOKEN = "base32:GEZDGNBVGY3TQOJQ"
+
+
+class ProfileCommandTests(unittest.TestCase):
+    def make(self, **fields):
+        cfg = Config(user="ab123", **fields)
+        return tn.Tunnel(cfg, "/usr/bin/openconnect", WRAPPER, logging.getLogger("t"), token_dir=Path(tempfile.mkdtemp()))
+
+    def test_heidelberg_command_is_unchanged(self):
+        # The command line from before profiles existed, argument for argument.
+        self.assertEqual(self.make().command(4321), [
+            "/usr/bin/openconnect", "--protocol=anyconnect", "--useragent=AnyConnect Linux_64 5.1.18.314",
+            "--user=ab123", "--passwd-on-stdin", "--non-inter", "--no-dtls", "--force-dpd=30",
+            "--reconnect-timeout=60", "--script-tun", f"--script=exec {WRAPPER} 4321", "vpn-ac.uni-heidelberg.de"])
+
+    def test_profile_flags(self):
+        cmd = self.make(host="sslvpn.ethz.ch", authgroup="staff-net", usergroup="exchange", os="win",
+                        useragent="AnyConnect", username_suffix="@staff-net.ethz.ch", no_external_auth=True).command(1)
+        for arg in ("--authgroup=staff-net", "--usergroup=exchange", "--os=win", "--useragent=AnyConnect",
+                    "--user=ab123@staff-net.ethz.ch", "--no-external-auth", "--non-inter"):
+            self.assertIn(arg, cmd)
+        self.assertEqual(cmd[-1], "sslvpn.ethz.ch")
+
+    def test_group_names_with_spaces_stay_one_argument(self):
+        cmd = self.make(authgroup="RWTH-VPN (Split Tunnel)").command(1)
+        self.assertIn("--authgroup=RWTH-VPN (Split Tunnel)", cmd)
+
+    def test_duo_push_drops_non_inter(self):
+        self.assertNotIn("--non-inter", self.make(mfa="duo_push").command(1))
+
+    def test_stdin_per_mode(self):
+        with mock.patch.object(tn.totp_mod, "code", return_value="123456"):
+            self.assertEqual(self.make(mfa="none").stdin_bytes(b"pw", None), b"pw\n")
+            self.assertEqual(self.make(mfa="totp_field").stdin_bytes(b"pw", TOKEN), b"pw\n")
+            self.assertEqual(self.make(mfa="duo_push").stdin_bytes(b"pw", None), b"pw\npush\n")
+            self.assertEqual(self.make(mfa="totp_append").stdin_bytes(b"pw", TOKEN), b"pw123456\n")
+            self.assertEqual(self.make(mfa="totp_append", totp_separator=",").stdin_bytes(b"pw", TOKEN), b"pw,123456\n")
+
+    def test_totp_append_records_when_the_code_was_made(self):
+        t = self.make(mfa="totp_append")
+        t.stdin_bytes(b"pw", TOKEN)
+        self.assertLess(abs(t.otp_generated_at - time.time()), 5)
+        with self.assertRaises(ValueError):
+            self.make(mfa="totp_append").stdin_bytes(b"pw", None)
+
+
 @posix_only
 class TunnelTests(unittest.IsolatedAsyncioTestCase):
     def setUp(self):
@@ -184,6 +256,24 @@ class TunnelTests(unittest.IsolatedAsyncioTestCase):
         self.assertIsNotNone(t.otp_generated_at)
         self.assertLess(abs(t.otp_generated_at - time.time()), 10)
 
+    async def test_only_totp_field_writes_a_token_file(self):
+        for mfa in ("none", "duo_push", "totp_append"):
+            self.cfg.mfa = mfa
+            t = self.make()
+            await t.start(b"secret", totp=TOKEN)
+            self.assertEqual(self.token_files(), [], mfa)
+            self.assertFalse([a for a in t.command(1) if a.startswith("--token")], mfa)
+            self.assertTrue(await t.wait_ready(3))
+            await t.stop(2)
+
+    async def test_duo_push_sends_push_as_the_second_line(self):
+        self.cfg.mfa = "duo_push"
+        t = self.make()
+        await t.start(b"secret")
+        self.assertTrue(await t.wait_ready(3))
+        await t.stop(2)
+        self.assertEqual(self.pwfile.read_text().splitlines(), ["secret", "push"])
+
     async def test_otp_generated_at_is_none_without_token(self):
         t = self.make()
         await t.start(b"secret")
````

Apply to `tests/test_wintunnel.py`:

````diff
diff --git a/tests/test_wintunnel.py b/tests/test_wintunnel.py
--- a/tests/test_wintunnel.py
+++ b/tests/test_wintunnel.py
@@ -35,6 +35,17 @@ class ParseTests(unittest.TestCase):
         self.assertIn("--disable-ipv6", cmd)
         self.assertEqual(cmd[-1], "vpn.example")
 
+    def test_command_uses_the_profile_like_the_posix_tunnel(self):
+        cfg = Config(user="jdoe", host="sslvpn.ethz.ch", authgroup="staff-net", username_suffix="@staff-net.ethz.ch",
+                     no_external_auth=True, mfa="duo_push")
+        tunnel = wintunnel.WindowsTunnel(cfg, "openconnect.exe", "x.js", logging.getLogger("t"))
+        cmd = tunnel.command(4000)
+        self.assertEqual(cmd[1:1 + len(tunnel.auth_args())], tunnel.auth_args())
+        self.assertIn("--authgroup=staff-net", cmd)
+        self.assertIn("--user=jdoe@staff-net.ethz.ch", cmd)
+        self.assertNotIn("--non-inter", cmd)
+        self.assertEqual(cmd[-1], "sslvpn.ethz.ch")
+
     def test_password_is_passed_in_the_ansi_code_page(self):
         tunnel = wintunnel.WindowsTunnel(Config(user="ab1", host="vpn.example"), "openconnect.exe", "x.js",
                                          logging.getLogger("t"))
````

Apply to `tests/fake_openconnect.py` (test helper, needed by the new Duo test):

````diff
diff --git a/tests/fake_openconnect.py b/tests/fake_openconnect.py
--- a/tests/fake_openconnect.py
+++ b/tests/fake_openconnect.py
@@ -5,10 +5,12 @@ Environment variables:
   FAKE_MODE           ok (default) | auth_fail | input_required | totp_rejected | never_ready | ignore_sigterm | exit_after_ready
   FAKE_DELAY          seconds until the port listens (default 0.2)
   FAKE_EXIT_AFTER     with exit_after_ready: seconds after becoming ready (default 0.5)
-  FAKE_PASSWORD_FILE  file the password read from stdin is appended to
+  FAKE_PASSWORD_FILE  file every line read from stdin is appended to (password, "push" for Duo)
+  FAKE_ARGS_FILE      file the command line is appended to, one JSON list per start
   FAKE_TOKEN_FILE     file the contents of --token-secret=@file are appended to
                       (read shortly before becoming ready, like openconnect when generating the code)
 """
+import json
 import os
 import signal
 import socket
@@ -43,10 +45,14 @@ def main():
             port = int(arg.split()[-1])
         if arg.startswith("--token-secret=@"):
             token_path = arg[len("--token-secret=@"):]
-    password = sys.stdin.readline()
+    if os.environ.get("FAKE_ARGS_FILE"):
+        with open(os.environ["FAKE_ARGS_FILE"], "a", encoding="utf-8") as handle:
+            handle.write(json.dumps(sys.argv[1:]) + "\n")
+    # uni-vpn closes stdin after writing, so this ends; Duo sends a second line.
+    lines = sys.stdin.read().splitlines()
     if os.environ.get("FAKE_PASSWORD_FILE"):
         with open(os.environ["FAKE_PASSWORD_FILE"], "a", encoding="utf-8") as handle:
-            handle.write(password)
+            handle.write("".join(line + "\n" for line in lines))
     log("POST https://fake.example/")
     mode = os.environ.get("FAKE_MODE", "ok")
     delay = float(os.environ.get("FAKE_DELAY", "0.2"))
````

- [ ] **Step 2: Run them to make sure they fail**

Run: `python3 -m unittest tests.test_tunnel tests.test_wintunnel -v`
Expected: FAIL, among others `AttributeError: module 'uni_vpn.tunnel' has no attribute 'totp_mod'`, `... has no attribute 'APPEND_REJECTED'`, `'WindowsTunnel' object has no attribute 'auth_args'`.

- [ ] **Step 3: Implement in tunnel.py**

````diff
diff --git a/uni_vpn/tunnel.py b/uni_vpn/tunnel.py
--- a/uni_vpn/tunnel.py
+++ b/uni_vpn/tunnel.py
@@ -15,22 +15,30 @@ import time
 from pathlib import Path
 
 from . import platform as pf
+from . import totp as totp_mod
 from .config import Config
 
-# Measured on 2026-09-08 against vpn-ac: the ASA rejects a wrong password with "Login failed."
+# Measured on 2026-09-08 against Heidelberg's ASA: it rejects a wrong password with "Login failed."
 # before it asks for the OTP. With a wrong one-time code the OTP prompt comes first
 # ("Generating OATH TOTP token code"), then "Login failed.". In both cases it shows the form
 # again, stdin is closed, "User input required", then "Failed to complete authentication".
-# So the order decides which factor was wrong.
+# So the order decides which factor was wrong; without a separate OTP step the profile's
+# second factor decides how to word it.
 PASSWORD_REJECTED = "Login rejected: check your password (uni-vpn password)"
 TOTP_REJECTED = (
     "One-time code rejected: check the computer's clock, otherwise re-enter the TOTP secret (uni-vpn totp)"
 )
+APPEND_REJECTED = "Login rejected: check your password, then the computer's clock (uni-vpn password)"
+DUO_REJECTED = ("Login rejected: check your password, or the Duo request was denied or not answered in time "
+                "(uni-vpn password)")
+LOGIN_REJECTED = {"totp_append": APPEND_REJECTED, "duo_push": DUO_REJECTED}
 # Without a preceding "Login failed." the server asked for something uni-vpn cannot fill in.
 AUTH_REJECTED = (
     "Login rejected: check your password (uni-vpn password). "
     "If it is correct, the server asked for something uni-vpn does not know, see uni-vpn log"
 )
+SAML_REQUIRED = "This university signs in through a browser (SAML), uni-vpn does not support that yet"
+HOSTSCAN_REQUIRED = "The server requires HostScan (CSD), uni-vpn does not support that"
 OTP_GENERATED = "Generating OATH TOTP token code"
 LOGIN_FAILED = "Login failed"
 TOKEN_PREFIX = "totp-"
@@ -40,10 +48,10 @@ MARKERS: list[tuple[str, str, str]] = [
     ("Server is rejecting the soft token", "auth_failed", TOTP_REJECTED),
     ("Soft token string is invalid", "auth_failed", "TOTP secret unusable, enter it again (uni-vpn totp)"),
     ("User input required in non-interactive mode", "auth_failed", AUTH_REJECTED),
-    ("Server asked us to run CSD", "auth_failed", "Server requires HostScan, uni-vpn needs an update"),
-    ("Cisco Secure Desktop", "auth_failed", "Server requires HostScan, uni-vpn needs an update"),
-    ("SAML", "auth_failed", "Login method changed (SAML), uni-vpn needs an update"),
-    ("external browser", "auth_failed", "Login method changed, uni-vpn needs an update"),
+    ("Server asked us to run CSD", "auth_failed", HOSTSCAN_REQUIRED),
+    ("Cisco Secure Desktop", "auth_failed", HOSTSCAN_REQUIRED),
+    ("SAML", "auth_failed", SAML_REQUIRED),
+    ("external browser", "auth_failed", SAML_REQUIRED),
     ("Failed to complete authentication", "auth_failed", AUTH_REJECTED),
     ("certificate", "error", "Certificate problem on the server"),
 ]
@@ -60,7 +68,8 @@ def classify_line(line: str) -> tuple[str, str] | None:
 class Classifier:
     """Classifies the openconnect output line by line; the first verdict sticks."""
 
-    def __init__(self) -> None:
+    def __init__(self, mfa: str = "totp_field") -> None:
+        self.mfa = mfa
         self.otp_generated = False
         self.verdict: tuple[str, str] | None = None
 
@@ -71,7 +80,8 @@ class Classifier:
             self.otp_generated = True
             return None
         if LOGIN_FAILED.lower() in line.lower():
-            self.verdict = ("auth_failed", TOTP_REJECTED if self.otp_generated else PASSWORD_REJECTED)
+            message = TOTP_REJECTED if self.otp_generated else LOGIN_REJECTED.get(self.mfa, PASSWORD_REJECTED)
+            self.verdict = ("auth_failed", message)
         else:
             self.verdict = classify_line(line)
         return self.verdict
@@ -128,7 +138,7 @@ class Tunnel:
         self.proc: asyncio.subprocess.Process | None = None
         self.exited = asyncio.Event()
         self.stderr_tail: collections.deque[str] = collections.deque(maxlen=20)
-        self.classifier = Classifier()
+        self.classifier = Classifier(cfg.mfa)
         self.classification: tuple[str, str] | None = None
         self.otp_generated_at: float | None = None  # wall clock time when openconnect generated a code
         self.stopped_by_us = False
@@ -136,14 +146,35 @@ class Tunnel:
         self.ready_at: float | None = None
         self._reader: asyncio.Task | None = None
 
+    def auth_args(self) -> list[str]:
+        """Login options from the university profile; the Windows tunnel uses the same."""
+        cfg = self.cfg
+        args = ["--protocol=anyconnect", f"--useragent={cfg.useragent}", f"--user={cfg.login_name}", "--passwd-on-stdin"]
+        # With Duo the second password ("push") comes through openconnect's prompt, which
+        # --non-inter refuses. stdin is closed after it, so a further prompt ends at EOF.
+        if cfg.mfa != "duo_push":
+            args.append("--non-inter")
+        if cfg.authgroup:
+            args.append(f"--authgroup={cfg.authgroup}")
+        if cfg.usergroup:
+            args.append(f"--usergroup={cfg.usergroup}")
+        if cfg.os:
+            args.append(f"--os={cfg.os}")
+        if cfg.no_external_auth:
+            args.append("--no-external-auth")
+        return args
+
+    def token_args(self) -> list[str]:
+        # The secret is passed via a 0600 file, never via the process list. openconnect
+        # rereads it for every code it generates, so it stays until the tunnel is up.
+        if self.token_file:
+            return ["--token-mode=totp", f"--token-secret=@{self.token_file}"]
+        return []
+
     def command(self, port: int) -> list[str]:
         cmd = [
             self.openconnect,
-            "--protocol=anyconnect",
-            f"--useragent={self.cfg.useragent}",
-            f"--user={self.cfg.user}",
-            "--passwd-on-stdin",
-            "--non-inter",
+            *self.auth_args(),
             "--no-dtls",
             "--force-dpd=30",
             "--reconnect-timeout=60",
@@ -152,11 +183,7 @@ class Tunnel:
             # "exec" so that dash does not leave an sh running next to ocproxy.
             f"--script=exec {shlex.quote(self.wrapper)} {port}",
         ]
-        if self.token_file:
-            # The secret is passed via a 0600 file, never via the process list. openconnect
-            # rereads it for every code it generates, so it stays until the tunnel is up.
-            cmd += ["--token-mode=totp", f"--token-secret=@{self.token_file}"]
-        return cmd + [self.cfg.host]
+        return cmd + self.token_args() + [self.cfg.host]
 
     def _write_token_file(self, totp: str) -> None:
         self.token_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
@@ -193,12 +220,26 @@ class Tunnel:
         """The password as openconnect reads it from stdin."""
         return password
 
-    async def start(self, password: bytes, totp: str | None = None) -> None:
+    def stdin_bytes(self, password: bytes, totp: str | None) -> bytes:
+        """What openconnect reads on stdin, by the profile's second factor."""
+        mfa = self.cfg.mfa
+        if mfa == "totp_append":
+            if not totp:
+                raise ValueError("totp_append needs a TOTP secret")
+            code = totp_mod.code(totp)
+            self.otp_generated_at = time.time()
+            return self._password_bytes(password + self.cfg.totp_separator.encode() + code.encode()) + b"\n"
         data = self._password_bytes(password) + b"\n"
+        if mfa == "duo_push":
+            data += b"push\n"
+        return data
+
+    async def start(self, password: bytes, totp: str | None = None) -> None:
+        data = self.stdin_bytes(password, totp)
         self.port = free_port()
         self.started_at = time.monotonic()
         env = self._env()
-        if totp:
+        if totp and self.cfg.mfa == "totp_field":
             self._write_token_file(totp)
         try:
             self.proc = await asyncio.create_subprocess_exec(
````

- [ ] **Step 4: Share the login flags with the Windows tunnel**

````diff
diff --git a/uni_vpn/wintunnel.py b/uni_vpn/wintunnel.py
--- a/uni_vpn/wintunnel.py
+++ b/uni_vpn/wintunnel.py
@@ -59,11 +59,7 @@ class WindowsTunnel(Tunnel):
     def command(self, port: int) -> list[str]:
         cmd = [
             self.openconnect,
-            "--protocol=anyconnect",
-            f"--useragent={self.cfg.useragent}",
-            f"--user={self.cfg.user}",
-            "--passwd-on-stdin",
-            "--non-inter",
+            *self.auth_args(),
             "--no-dtls",
             "--force-dpd=30",
             "--reconnect-timeout=60",
@@ -73,9 +69,7 @@ class WindowsTunnel(Tunnel):
             # openconnect 9.12 runs it as: cscript.exe "<script>" (the .js association picks JScript)
             f"--script={self.script}",
         ]
-        if self.token_file:
-            cmd += ["--token-mode=totp", f"--token-secret=@{self.token_file}"]
-        return cmd + [self.cfg.host]
+        return cmd + self.token_args() + [self.cfg.host]
 
     def _env(self) -> dict[str, str]:
         env = super()._env()
````

- [ ] **Step 5: Run the tests**

Run: `python3 -m unittest tests.test_tunnel tests.test_wintunnel -v`
Expected: PASS (on Linux 51 tests; some POSIX-only cases are skipped on Windows).

- [ ] **Step 6: Run the whole suite**

Run: `python3 -m unittest discover -s tests -t .`
Expected: `OK (skipped=17)`.

- [ ] **Step 7: Commit**

```bash
git add uni_vpn/tunnel.py uni_vpn/wintunnel.py tests/fake_openconnect.py tests/test_tunnel.py tests/test_wintunnel.py
git commit -m "tunnel: openconnect flags, stdin and error texts from the profile"
```

---

### Task 4: Daemon with optional TOTP, SAML refusal and profile domains

**Files:**
- Modify: `uni_vpn/daemon.py`
- Modify: `uni_vpn/pac.py`
- Test: `tests/test_daemon.py`, `tests/test_pac.py`

**Interfaces:**
- Consumes: `Config.needs_totp`, `Config.mfa`, `Config.default_domains`, `config.check_overrides`, `config.profile_config`, `config.write_initial(path, user, university, overrides)`, `config.set_values`, `config.copy_profile`, `tunnel.SAML_REQUIRED`, `universities.FieldError`.
- Produces: `pac.read_domains(path, defaults: list[str] | None = None)`; `Daemon.complete_setup(user, password, token: str | None, university: str = "heidelberg", overrides: dict | None = None)` raising `FieldError` (fields `user`, `university`, `host`, `usergroup`, `authgroup`, `mfa`, `totp`) or `ValueError`; `/status.json` adds `university`, `university_name`, `mfa`, `mfa_portal_url`, `mfa_steps`.

Notes for the implementer:
- The SAML check sits at the top of the connect loop, before the Cisco and network checks, so nothing is probed or started.
- `_otp_wait()` returns 0 when the mode has no TOTP; the harness patches `_otp_wait`, the new test calls the original through `self.real_otp_wait`.
- `complete_setup` validates the whole profile before touching the keyring, so a bad field leaves nothing behind (the assistant stays the way in). After writing `config.toml` it copies the profile into the running `Config`, so the test connection uses the new host and mode.

- [ ] **Step 1: Write the failing tests**

Apply to `tests/test_daemon.py`:

````diff
diff --git a/tests/test_daemon.py b/tests/test_daemon.py
--- a/tests/test_daemon.py
+++ b/tests/test_daemon.py
@@ -1,4 +1,5 @@
 import asyncio
+import json
 import logging
 import os
 import tempfile
@@ -530,6 +531,80 @@ class FailureTests(DaemonHarness):
         self.assertIsNone(await d.acquire())
 
 
+class ProfileTests(DaemonHarness):
+    async def asyncSetUp(self):
+        await super().asyncSetUp()
+        self.argsfile = Path(tempfile.mkdtemp()) / "args"
+        os.environ["FAKE_ARGS_FILE"] = str(self.argsfile)
+
+    def last_args(self):
+        return json.loads(self.argsfile.read_text().splitlines()[-1])
+
+    async def test_without_totp_no_secret_is_read(self):
+        self.cfg.mfa = "none"
+        self.totp = credentials.TotpMissing("x")
+        d = await self.start_daemon()
+        await d.request_connect()
+        await wait_state(d, dm.State.connected)
+        self.assertEqual(self.pw_lines(), ["pw-s3cret"])
+        self.assertFalse(self.tokenfile.exists())
+        self.assertFalse([a for a in self.last_args() if a.startswith("--token")])
+
+    async def test_saml_is_refused_before_anything_starts(self):
+        self.cfg.mfa = "saml"
+        d = await self.start_daemon()
+        await d.request_connect()
+        await wait_state(d, dm.State.auth_failed)
+        self.assertIn("browser", d.message)
+        self.assertEqual(self.pw_lines(), [])
+        self.assertFalse(self.argsfile.exists())
+
+    async def test_totp_append_sends_password_and_code_in_one_line(self):
+        self.cfg.mfa = "totp_append"
+        with mock.patch("uni_vpn.tunnel.totp_mod.code", return_value="123456"):
+            d = await self.start_daemon()
+            await d.request_connect()
+            await wait_state(d, dm.State.connected)
+        self.assertEqual(self.pw_lines(), ["pw-s3cret123456"])
+        self.assertFalse(self.tokenfile.exists())
+
+    async def test_duo_push_sends_push_and_needs_no_secret(self):
+        self.cfg.mfa = "duo_push"
+        self.totp = credentials.TotpMissing("x")
+        d = await self.start_daemon()
+        await d.request_connect()
+        await wait_state(d, dm.State.connected)
+        self.assertEqual(self.pw_lines(), ["pw-s3cret", "push"])
+        self.assertNotIn("--non-inter", self.last_args())
+
+    async def test_profile_flags_reach_openconnect(self):
+        self.cfg.authgroup = "staff-net"
+        self.cfg.username_suffix = "@staff-net.ethz.ch"
+        d = await self.start_daemon()
+        await d.request_connect()
+        await wait_state(d, dm.State.connected)
+        self.assertIn("--authgroup=staff-net", self.last_args())
+        self.assertIn("--user=u@staff-net.ethz.ch", self.last_args())
+
+    async def test_otp_wait_only_with_totp(self):
+        self.cfg.mfa = "none"
+        d = await self.start_daemon()
+        d.last_otp_step = int(1000.0 // 30)
+        self.assertEqual(self.real_otp_wait(d, now=1000.0), 0)
+
+    async def test_status_names_university_and_second_factor(self):
+        self.cfg.default_domains = ["intranet.example.edu"]
+        d = await self.start_daemon()
+        s = d.status()
+        self.assertEqual(s["university"], "heidelberg")
+        self.assertEqual(s["university_name"], "Heidelberg University")
+        self.assertEqual(s["mfa"], "totp_field")
+        self.assertEqual(s["mfa_portal_url"], "https://mfa.uni-heidelberg.de/")
+        self.assertTrue(s["mfa_steps"])
+        self.assertEqual(s["domains"], ["intranet.example.edu"], "no domains.txt: the profile's defaults")
+        self.assertIn("intranet.example.edu", d.pac())
+
+
 class StatusTests(DaemonHarness):
     async def test_status_keys(self):
         d = await self.start_daemon()
````

Apply to `tests/test_pac.py`:

````diff
diff --git a/tests/test_pac.py b/tests/test_pac.py
--- a/tests/test_pac.py
+++ b/tests/test_pac.py
@@ -62,6 +62,15 @@ class DomainFileTests(unittest.TestCase):
     def test_missing_file_gives_defaults(self):
         self.assertEqual(pac.read_domains(self.path), pac.DEFAULT_DOMAINS)
 
+    def test_missing_file_gives_the_university_defaults(self):
+        self.assertEqual(pac.read_domains(self.path, ["intranet.example.edu"]), ["intranet.example.edu"])
+        self.assertEqual(pac.read_domains(self.path, []), [])
+
+    def test_heidelberg_profile_has_the_same_defaults(self):
+        from uni_vpn import universities
+
+        self.assertEqual(list(universities.get("heidelberg").default_domains), pac.DEFAULT_DOMAINS)
+
     def test_roundtrip_keeps_order_and_comment_header(self):
         pac.write_domains(self.path, ["example.org", "sogo.uni-heidelberg.de"])
         text = self.path.read_text(encoding="utf-8")
````

- [ ] **Step 2: Run them to make sure they fail**

Run: `python3 -m unittest tests.test_daemon tests.test_pac -v`
Expected: FAIL, among others `state is keyring (No TOTP secret stored: uni-vpn totp), expected connected`, `KeyError: 'university'`, `TypeError: read_domains() takes 1 positional argument but 2 were given`.

- [ ] **Step 3: Profile defaults for the domain list**

````diff
diff --git a/uni_vpn/pac.py b/uni_vpn/pac.py
--- a/uni_vpn/pac.py
+++ b/uni_vpn/pac.py
@@ -77,12 +77,13 @@ def domains_path() -> Path:
     return config_dir() / "domains.txt"
 
 
-def read_domains(path: Path) -> list[str]:
-    """If the file is missing, the defaults apply; invalid lines are skipped."""
+def read_domains(path: Path, defaults: list[str] | None = None) -> list[str]:
+    """If the file is missing, the university's defaults apply (without them Heidelberg's);
+    invalid lines are skipped."""
     try:
         text = path.read_text(encoding="utf-8")
     except FileNotFoundError:
-        return list(DEFAULT_DOMAINS)
+        return list(DEFAULT_DOMAINS if defaults is None else defaults)
     domains, _errors = parse_domain_list(text)
     return domains
 
````

- [ ] **Step 4: Daemon**

````diff
diff --git a/uni_vpn/daemon.py b/uni_vpn/daemon.py
--- a/uni_vpn/daemon.py
+++ b/uni_vpn/daemon.py
@@ -14,9 +14,10 @@ from typing import Awaitable, Callable
 from . import PROTOCOL, __version__, credentials, pac, sysproxy
 from . import platform as pf
 from . import config as config_mod
+from . import universities as unis
 from .config import Config
 from .forwarder import Forwarder
-from .tunnel import PasswordEncodingError, Tunnel, remove_stale_token_files
+from .tunnel import SAML_REQUIRED, PasswordEncodingError, Tunnel, remove_stale_token_files
 
 
 class State(str, Enum):
@@ -178,6 +179,11 @@ class Daemon:
             "since": self.since,
             "host": self.cfg.host,
             "user": self.cfg.user,
+            "university": self.cfg.university,
+            "university_name": self.cfg.university_name,
+            "mfa": self.cfg.mfa,
+            "mfa_portal_url": self.cfg.mfa_portal_url,
+            "mfa_steps": list(self.cfg.mfa_steps),
             "socks_port": self.cfg.socks_port,
             "http_port": self.cfg.http_port,
             "idle_minutes": self.cfg.idle_minutes,
@@ -186,7 +192,7 @@ class Daemon:
             "bytes_out": self.forwarder.bytes_out,
             "connects": self.connect_count,
             "last_error": self.last_error,
-            "domains": pac.read_domains(self.domains_path),
+            "domains": pac.read_domains(self.domains_path, self.cfg.default_domains),
             "pac_url": sysproxy.pac_url(self.cfg.http_port),
             "pac_refresh": "manual" if pf.IS_MACOS else "auto",
             "log_tail": list(self.log_tail)[-30:],
@@ -216,13 +222,21 @@ class Daemon:
             return "password"
         return None
 
-    async def complete_setup(self, user: str, password: str, token: str) -> None:
-        """First run from the setup assistant: config.toml, both secrets, then a test connection."""
+    async def complete_setup(self, user: str, password: str, token: str | None,
+                             university: str = unis.DEFAULT_ID, overrides: dict | None = None) -> None:
+        """First run from the setup assistant: config.toml, the secrets, then a test connection.
+        Raises unis.FieldError naming the step to change, ValueError otherwise."""
         user = user.strip()
         if not config_mod.valid_user(user):
-            raise ValueError("Invalid university ID")
+            raise unis.FieldError("user", "Invalid university ID")
         if self.config_error and not self.needs_setup:
             raise ValueError(f"Fix config.toml first: {self.config_error}")
+        overrides = config_mod.check_overrides(overrides or {})
+        profile = config_mod.profile_config(university, overrides)
+        if profile.mfa == "saml":
+            raise unis.FieldError("university", SAML_REQUIRED)
+        if profile.needs_totp and not token:
+            raise unis.FieldError("totp", "Paste the secret first")
         path = self.config_path or config_mod.default_path()
         loop = asyncio.get_running_loop()
         # Secrets first: if the keyring refuses, no config.toml exists yet and the assistant
@@ -231,19 +245,22 @@ class Daemon:
         self.cfg.user = user
         try:
             await loop.run_in_executor(None, self.password_setter, password)
-            await loop.run_in_executor(None, self.totp_setter, token)
+            if profile.needs_totp:
+                await loop.run_in_executor(None, self.totp_setter, token)
             if path.exists():
-                await loop.run_in_executor(None, config_mod.set_user, path, user)
+                values = {"user": user, "university": university, **overrides}
+                await loop.run_in_executor(None, config_mod.set_values, path, values)
             else:
-                await loop.run_in_executor(None, config_mod.write_initial, path, user)
+                await loop.run_in_executor(None, config_mod.write_initial, path, user, university, overrides)
         except config_mod.ConfigError as exc:
             self.cfg.user = previous_user
             raise ValueError(str(exc)) from exc
         except BaseException:
             self.cfg.user = previous_user
             raise
+        config_mod.copy_profile(self.cfg, profile)
         self.config_path = path
-        self.log.info("Setup completed for %s", user)
+        self.log.info("Setup completed for %s (%s)", user, profile.university_name)
         self.needs_setup = False
         self.config_error = None
         await self._secrets_changed()
@@ -265,7 +282,7 @@ class Daemon:
         await self.request_connect()
 
     def pac(self) -> str:
-        return pac.build_pac(pac.read_domains(self.domains_path), self.cfg.socks_port)
+        return pac.build_pac(pac.read_domains(self.domains_path, self.cfg.default_domains), self.cfg.socks_port)
 
     async def set_domains(self, text: str) -> list[str]:
         domains, errors = pac.parse_domain_list(text)
@@ -399,6 +416,9 @@ class Daemon:
             if self._elevated() is False:
                 self._final(State.error, "Needs administrator rights (Wintun): run the installer again")
                 return
+            if cfg.mfa == "saml":
+                self._final(State.auth_failed, SAML_REQUIRED)
+                return
             if await asyncio.get_running_loop().run_in_executor(None, self.cisco_check):
                 self._set(State.blocked, BLOCKED_MESSAGE)
                 await self._sleep(cfg.retry_interval)
@@ -424,17 +444,19 @@ class Daemon:
             except credentials.KeyringError as exc:
                 self._final(State.keyring, str(exc))
                 return
-            try:
-                totp = (await self.totp_getter()).decode("ascii").strip()
-            except credentials.TotpMissing:
-                self._final(State.keyring, "No TOTP secret stored: uni-vpn totp")
-                return
-            except credentials.KeyringLocked:
-                self._final(State.keyring, "Keyring locked, please unlock it and connect again")
-                return
-            except (credentials.KeyringError, UnicodeDecodeError) as exc:
-                self._final(State.keyring, f"TOTP secret unreadable: {exc}")
-                return
+            totp = None
+            if cfg.needs_totp:
+                try:
+                    totp = (await self.totp_getter()).decode("ascii").strip()
+                except credentials.TotpMissing:
+                    self._final(State.keyring, "No TOTP secret stored: uni-vpn totp")
+                    return
+                except credentials.KeyringLocked:
+                    self._final(State.keyring, "Keyring locked, please unlock it and connect again")
+                    return
+                except (credentials.KeyringError, UnicodeDecodeError) as exc:
+                    self._final(State.keyring, f"TOTP secret unreadable: {exc}")
+                    return
 
             self._set(State.connecting, "Connecting")
             try:
@@ -526,6 +548,8 @@ class Daemon:
     def _otp_wait(self, now: float | None = None) -> float:
         """Seconds until the next one-time code window, 0 if the current code is still unused."""
         now = time.time() if now is None else now
+        if not self.cfg.needs_totp:
+            return 0
         if self.last_otp_step is None or int(now // OTP_STEP) != self.last_otp_step:
             return 0
         return OTP_STEP - now % OTP_STEP + 0.5
````

- [ ] **Step 5: Run the tests**

Run: `python3 -m unittest tests.test_daemon tests.test_pac -v`
Expected: PASS.

- [ ] **Step 6: Run the whole suite**

Run: `python3 -m unittest discover -s tests -t .`
Expected: `OK (skipped=17)`. `/api/setup` still requires `secret` until Task 6; its tests are unchanged.

- [ ] **Step 7: Commit**

```bash
git add uni_vpn/daemon.py uni_vpn/pac.py tests/test_daemon.py tests/test_pac.py
git commit -m "daemon: totp only when the university needs it, refuse saml, profile domains"
```

---

### Task 5: Gateway detection

**Files:**
- Create: `uni_vpn/detect.py`
- Create: `tests/fixtures/detect/{heidelberg,bremen,ethz,muenster,marburg,kassel,stanford,stanford-group-stanford,fu-berlin}.xml`
- Test: `tests/test_detect.py`

**Interfaces:**
- Consumes: `universities.check_field`, `FieldError`, `DEFAULT_USERAGENT`.
- Produces: dataclass `Detection(host, usergroup, reachable, cisco, saml, groups: list[str], group: str, fields: list[dict], second_password: bool, message, error)` with `suggestion() -> {"host", "usergroup", "authgroup", "mfa"}` and `as_dict()`; `split_address(text) -> (host, usergroup)`; `init_request(host, usergroup="", group="") -> bytes`; `parse_reply(data: bytes, host: str, usergroup: str = "") -> Detection`; `probe(host, usergroup="", group="", *, useragent=..., opener=default_opener, timeout=10) -> Detection` (raises `FieldError` for invalid input, network trouble goes into `.error`); `_RepostRedirects` (redirect handler); `MAX_REPLY`, `NOT_CISCO`. Tests export `tests.test_detect.fixture(name) -> bytes` for Tasks 6 and 8.

Notes for the implementer:
- The fixtures are real replies recorded on 2026-10-05 (spec section 2). Every server names `<auth-method>single-sign-on-v2</auth-method>` in `<opaque>`, Heidelberg included, so SAML is detected only by `<sso-v2-login>` or an `sso` input. The test for Heidelberg guards that.
- Heidelberg and Bremen answer the POST with a 302 to a cluster member. urllib would turn that into a GET without body, hence `_RepostRedirects`. It copies `req.headers` (only the headers we set), not `header_items()`, because the latter includes the old `Host`.
- Replies with a DOCTYPE or over 256 KB are refused before parsing.

- [ ] **Step 1: Add the fixtures**

Create the nine files under `tests/fixtures/detect/` with exactly this content (recorded with the init request from `detect.init_request`, user agent `AnyConnect Linux_64 5.1.18.314`, headers `X-Transcend-Version: 1`, `X-Aggregate-Auth: 1`, redirects followed with the same POST; `stanford-group-stanford.xml` with `<group-select>Stanford</group-select>`).

`tests/fixtures/detect/heidelberg.xml`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<config-auth client="vpn" type="auth-request" aggregate-auth-version="2">
<opaque is-for="sg">
<tunnel-group>DefaultWEBVPNGroup</tunnel-group>
<aggauth-handle>4984830146295384610</aggauth-handle>
<auth-method>single-sign-on-v2</auth-method>
<config-hash>1790282985956</config-hash>
</opaque>
<auth id="main">
<title>Login</title>
<message>Bitte geben Sie ihren Benutzernamen und Passwort ein.</message>
<banner></banner>
<form>
<input type="text" name="username" label="Username:"></input>
<input type="password" name="password" label="Password:"></input>
</form>
</auth>
</config-auth>
```

`tests/fixtures/detect/bremen.xml`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<config-auth client="vpn" type="auth-request" aggregate-auth-version="2">
<opaque is-for="sg">
<tunnel-group>unitunnelall</tunnel-group>
<aggauth-handle>2379893026361719118</aggauth-handle>
<auth-method>single-sign-on-v2</auth-method>
<group-alias>Tunnel-All-Traffic</group-alias>
<config-hash>1788194782005</config-hash>
</opaque>
<auth id="main">
<title>Cisco Secure Client Installation</title>
<message></message>
<banner></banner>
<form>
<input type="text" name="username" label="Username:"></input>
<input type="password" name="password" label="Password:"></input>
<input type="password" name="secondary_password" label="Password:"></input>
<select name="group_list" label="GROUP:">
<option selected="true">Tunnel-All-Traffic</option>
<option>Tunnel-Uni-Bremen</option>
</select>
</form>
</auth>
</config-auth>
```

`tests/fixtures/detect/ethz.xml`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<config-auth client="vpn" type="auth-request" aggregate-auth-version="2">
<opaque is-for="sg">
<tunnel-group>staff-net</tunnel-group>
<aggauth-handle>7533735957464142058</aggauth-handle>
<auth-method>single-sign-on-v2</auth-method>
<group-alias>staff-net</group-alias>
<config-hash>1791175769768</config-hash>
</opaque>
<auth id="main">
<title>Login</title>
<message>Bitte geben Sie Benutzernamen und Passwort ein.</message>
<banner>Server: =&#x3E; sslvpn.ethz.ch/VPZ_NAME&#x0A;Username: ETH user name@REALM.ethz.ch&#x0A;Password: ETH network password&#x0A;Second Password: One Time Password (OTP)&#x0A;Example:&#x0A;=&#x3E; Aktuell ist staff-net selektiert:&#x0A;sslvpn.ethz.ch/staff-net &#x0A;ETH user name@staff-net.ethz.ch</banner>
<form>
<input type="text" name="username" label="Username:"></input>
<input type="password" name="password" label="Password:"></input>
<input type="password" name="secondary_password" label="Password:"></input>
<select name="group_list" label="GROUP:">
<option selected="true">staff-net</option>
<option>student-net</option>
</select>
</form>
</auth>
</config-auth>
```

`tests/fixtures/detect/muenster.xml`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<config-auth client="vpn" type="auth-request" aggregate-auth-version="2">
<opaque is-for="sg">
<tunnel-group>vpnstandard</tunnel-group>
<aggauth-handle>1650746544276735126</aggauth-handle>
<auth-method>single-sign-on-v2</auth-method>
<config-hash>1790699181320</config-hash>
</opaque>
<auth id="main">
<title>Login</title>
<message>Bitte geben Sie Ihre zentrale Nutzerkennung und Ihr Netzzugangspasswort ein</message>
<banner></banner>
<form>
<input type="text" name="username" label="Username:"></input>
<input type="password" name="password" label="Password:"></input>
<input type="password" name="secondary_password" label="Password:"></input>
</form>
</auth>
</config-auth>
```

`tests/fixtures/detect/marburg.xml`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<config-auth client="vpn" type="auth-request" aggregate-auth-version="2">
<opaque is-for="sg">
<tunnel-group>vpn-staff</tunnel-group>
<aggauth-handle>8192463503180710810</aggauth-handle>
<auth-method>single-sign-on-v2</auth-method>
<group-alias>unimr-vpn-staff-Passwort+2FA</group-alias>
<config-hash>1786538397690</config-hash>
</opaque>
<auth id="main">
<title>Login</title>
<message>Please enter your username and password.</message>
<banner></banner>
<form>
<input type="text" name="username" label="Username:"></input>
<input type="password" name="password" label="Password:"></input>
<select name="group_list" label="GROUP:">
<option selected="true">unimr-vpn-staff-Passwort+2FA</option>
<option>unimr-vpn-staff-ft-Passwort+2FA</option>
<option>unimr-vpn-students-Passwort+2FA</option>
<option>unimr-vpn-uv-staff-TS-PW+2FA</option>
</select>
</form>
</auth>
</config-auth>
```

`tests/fixtures/detect/kassel.xml`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<config-auth client="vpn" type="auth-request" aggregate-auth-version="2">
<opaque is-for="sg">
<tunnel-group>DefaultWEBVPNGroup</tunnel-group>
<aggauth-handle>11374319071584944258</aggauth-handle>
<auth-method>single-sign-on-v2</auth-method>
<config-hash>1789707932188</config-hash>
</opaque>
<auth id="main">
<form>
<input type="text" name="username" label="Username:"></input>
<input type="password" name="password" label="Password:"></input>
</form>
</auth>
</config-auth>
```

`tests/fixtures/detect/stanford.xml`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<config-auth client="vpn" type="auth-request" aggregate-auth-version="2">
<opaque is-for="sg">
<tunnel-group>CardinalKey-TG</tunnel-group>
<aggauth-handle>7636031216619106254</aggauth-handle>
<auth-method>single-sign-on-v2</auth-method>
<group-alias>CardinalKey</group-alias>
<config-hash>1789830105098</config-hash>
</opaque>
<auth id="main">
<title>Login</title>
<message>Please complete the authentication process in the AnyConnect Login window.</message>
<banner></banner>
<sso-v2-login>https://su-vpn-wech.stanford.edu/+CSCOE+/saml/sp/login?ctx=1168323638&#x26;acsamlcap=v2</sso-v2-login>
<sso-v2-login-final>https://su-vpn-wech.stanford.edu/+CSCOE+/saml_ac_login.html</sso-v2-login-final>
<sso-v2-token-cookie-name>acSamlv2Token</sso-v2-token-cookie-name>
<sso-v2-error-cookie-name>acSamlv2Error</sso-v2-error-cookie-name>
<form>
<input type="sso" name="sso-token"></input>
<select name="group_list" label="GROUP:">
<option selected="true">CardinalKey</option>
<option>CardinalKey-Full</option>
<option>SU-VPN Full Tunnel</option>
<option>Stanford</option>
<option>Stanford-Full</option>
<option>Test-CardinalKey</option>
<option>Test-CardinalKey-Full</option>
</select>
</form>
</auth>
</config-auth>
```

`tests/fixtures/detect/stanford-group-stanford.xml`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<config-auth client="vpn" type="auth-request" aggregate-auth-version="2">
<opaque is-for="sg">
<tunnel-group>Stanford-TG</tunnel-group>
<aggauth-handle>5327250724677720386</aggauth-handle>
<auth-method>single-sign-on-v2</auth-method>
<group-alias>Stanford</group-alias>
<config-hash>1789830105098</config-hash>
</opaque>
<auth id="main">
<title>Login</title>
<message>Please enter your username and password.</message>
<banner></banner>
<form>
<input type="text" name="username" label="Username:"></input>
<input type="password" name="password" label="Password:"></input>
<select name="group_list" label="GROUP:">
<option>CardinalKey</option>
<option>CardinalKey-Full</option>
<option>SU-VPN Full Tunnel</option>
<option selected="true">Stanford</option>
<option>Stanford-Full</option>
<option>Test-CardinalKey</option>
<option>Test-CardinalKey-Full</option>
</select>
</form>
</auth>
</config-auth>
```

`tests/fixtures/detect/fu-berlin.xml`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<config-auth client="vpn" type="auth-request" aggregate-auth-version="2">
<opaque is-for="sg">
<tunnel-group>DefaultWEBVPNGroup</tunnel-group>
<aggauth-handle>13570670443410999886</aggauth-handle>
<auth-method>single-sign-on-v2</auth-method>
<config-hash>1789723525702</config-hash>
</opaque>
<auth id="main">
<title>Login</title>
<message>Please complete the authentication process in the AnyConnect Login window.</message>
<banner></banner>
<sso-v2-login>https://vpn.fu-berlin.de/+CSCOE+/saml/sp/login?ctx=1343066654&#x26;acsamlcap=v2</sso-v2-login>
<sso-v2-login-final>https://vpn.fu-berlin.de/+CSCOE+/saml_ac_login.html</sso-v2-login-final>
<sso-v2-logout>https://vpn.fu-berlin.de/+CSCOE+/saml/sp/logout</sso-v2-logout>
<sso-v2-logout-final>https://vpn.fu-berlin.de/+CSCOE+/saml_ac_login.html</sso-v2-logout-final>
<sso-v2-token-cookie-name>acSamlv2Token</sso-v2-token-cookie-name>
<sso-v2-error-cookie-name>acSamlv2Error</sso-v2-error-cookie-name>
<form>
<input type="sso" name="sso-token"></input>
</form>
</auth>
</config-auth>
```

- [ ] **Step 2: Write the failing test**

Create `tests/test_detect.py`:

```python
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
        self.assertIn("Could not reach vpn.example.edu", d.error)
        self.assertIn("Connection refused", d.error)
        d = detect.probe("vpn.example.edu", opener=not_found)
        self.assertTrue(d.reachable)
        self.assertIn("404", d.error)

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
```

- [ ] **Step 3: Run it to make sure it fails**

Run: `python3 -m unittest tests.test_detect -v`
Expected: FAIL with `ImportError: cannot import name 'detect' from 'uni_vpn'`.

- [ ] **Step 4: Implement**

Create `uni_vpn/detect.py`:

```python
"""Probe a Cisco AnyConnect gateway without credentials: group list, login fields, SAML.

Sends the XML init request openconnect sends first and reads the login form the server
answers with. Nothing that identifies the user is sent. The result prefills the setup
assistant for a university that is not in universities.json.

Measured on 2026-10-05 against eleven gateways: every server names
<auth-method>single-sign-on-v2</auth-method> in <opaque> once the client advertises SSO, even
with a password form, so only <sso-v2-login> or an "sso" input means SAML. Load balancers
answer the POST with a 302 to a cluster member, which must get the same POST again.
"""

from __future__ import annotations

import ssl
import urllib.error
import urllib.request
import xml.etree.ElementTree as ET
from dataclasses import asdict, dataclass, field
from xml.sax.saxutils import escape

from . import universities as unis

INIT_REQUEST = (
    '<?xml version="1.0" encoding="UTF-8"?>\n'
    '<config-auth client="vpn" type="init" aggregate-auth-version="2">'
    '<version who="vpn">v9.12</version><device-id>linux-64</device-id>'
    '{group_select}<group-access>{url}</group-access>'
    '<capabilities><auth-method>single-sign-on-v2</auth-method></capabilities></config-auth>'
)
MAX_REPLY = 256 * 1024
MAX_REDIRECTS = 3
NOT_CISCO = "This address does not answer like a Cisco AnyConnect gateway"


@dataclass
class Detection:
    host: str
    usergroup: str = ""
    reachable: bool = False
    cisco: bool = False
    saml: bool = False
    groups: list[str] = field(default_factory=list)
    group: str = ""  # the group this form belongs to: the selected option, else the group alias
    fields: list[dict] = field(default_factory=list)  # {"name", "type", "label"} of text and password inputs
    second_password: bool = False
    message: str = ""
    error: str = ""

    def suggestion(self) -> dict:
        """Profile values for the setup assistant. The second factor is a guess: a second password
        field is usually TOTP, but without one the server may still want an appended code, Duo,
        or show the OTP field only after the password (Heidelberg)."""
        if self.saml:
            mfa = "saml"
        elif self.second_password:
            mfa = "totp_field"
        else:
            mfa = "none"
        return {"host": self.host, "usergroup": self.usergroup,
                "authgroup": self.group if len(self.groups) > 1 else "", "mfa": mfa}

    def as_dict(self) -> dict:
        data = asdict(self)
        data["suggestion"] = self.suggestion()
        return data


def split_address(text: str) -> tuple[str, str]:
    """"https://vpn.example.edu/staff" -> ("vpn.example.edu", "staff"). Raises unis.FieldError."""
    value = str(text or "").strip()
    for prefix in ("https://", "http://"):
        if value.lower().startswith(prefix):
            value = value[len(prefix):]
    host, _, path = value.partition("/")
    return unis.check_field("host", host), unis.check_field("usergroup", path)


def init_request(host: str, usergroup: str = "", group: str = "") -> bytes:
    url = f"https://{host}/{usergroup}"
    group_select = f"<group-select>{escape(group)}</group-select>" if group else ""
    return INIT_REQUEST.format(group_select=group_select, url=escape(url)).encode("utf-8")


def parse_reply(data: bytes, host: str, usergroup: str = "") -> Detection:
    result = Detection(host=host, usergroup=usergroup, reachable=True)
    # No DTDs: a gateway never sends one, and it is the way to entity expansion attacks.
    if len(data) > MAX_REPLY or b"<!DOCTYPE" in data[:4096].upper():
        result.error = NOT_CISCO
        return result
    try:
        root = ET.fromstring(data)
    except ET.ParseError:
        result.error = NOT_CISCO
        return result
    if root.tag != "config-auth":
        result.error = NOT_CISCO
        return result
    result.cisco = True
    auth = root.find("auth")
    if auth is None:
        result.error = "The gateway sent no login form"
        return result
    result.message = (auth.findtext("message") or "").strip()
    result.saml = auth.find("sso-v2-login") is not None or any(i.get("type") == "sso" for i in auth.iter("input"))
    form = auth.find("form")
    if form is not None:
        for item in form.findall("input"):
            if item.get("type") in ("text", "password"):
                result.fields.append({"name": item.get("name", ""), "type": item.get("type"),
                                      "label": item.get("label", "")})
        for select in form.findall("select"):
            if select.get("name") != "group_list":
                continue
            for option in select.findall("option"):
                name = (option.text or "").strip()
                if not name:
                    continue
                result.groups.append(name)
                if option.get("selected") == "true":
                    result.group = name
    if not result.group:
        result.group = (root.findtext("opaque/group-alias") or "").strip()
    result.second_password = any(f["name"] == "secondary_password" for f in result.fields)
    return result


class _RepostRedirects(urllib.request.HTTPRedirectHandler):
    """urllib turns a redirected POST into a GET without body; the gateway needs the POST again."""

    max_redirections = MAX_REDIRECTS

    def redirect_request(self, req, fp, code, msg, headers, newurl):
        if code not in (301, 302, 303, 307, 308) or not newurl.lower().startswith("https://"):
            return None
        # req.headers holds only the headers set by us; Host and Content-Length are recomputed.
        return urllib.request.Request(newurl, data=req.data, headers=dict(req.headers), method="POST")


def default_opener(request: urllib.request.Request, timeout: float) -> bytes:
    opener = urllib.request.build_opener(urllib.request.HTTPSHandler(context=ssl.create_default_context()),
                                         _RepostRedirects)
    with opener.open(request, timeout=timeout) as response:
        return response.read(MAX_REPLY + 1)


def probe(host: str, usergroup: str = "", group: str = "", *, useragent: str = unis.DEFAULT_USERAGENT,
          opener=default_opener, timeout: float = 10) -> Detection:
    """Ask the gateway for its login form. Raises unis.FieldError for an invalid address or group;
    network trouble ends up in Detection.error."""
    host = unis.check_field("host", host)
    usergroup = unis.check_field("usergroup", usergroup)
    group = unis.check_field("authgroup", group)
    request = urllib.request.Request(
        f"https://{host}/{usergroup}", data=init_request(host, usergroup, group), method="POST",
        headers={"User-Agent": useragent, "X-Transcend-Version": "1", "X-Aggregate-Auth": "1",
                 "X-Support-HTTP-Auth": "true", "Accept": "*/*", "Content-Type": "application/x-www-form-urlencoded"})
    try:
        data = opener(request, timeout)
    except urllib.error.HTTPError as exc:
        return Detection(host=host, usergroup=usergroup, reachable=True, error=f"{NOT_CISCO} (HTTP {exc.code})")
    except (urllib.error.URLError, OSError) as exc:
        reason = getattr(exc, "reason", None) or exc
        return Detection(host=host, usergroup=usergroup, error=f"Could not reach {host}: {reason}")
    return parse_reply(data, host, usergroup)
```

- [ ] **Step 5: Run the tests**

Run: `python3 -m unittest tests.test_detect -v`
Expected: PASS (16 tests).

- [ ] **Step 6: Run the whole suite**

Run: `python3 -m unittest discover -s tests -t .`
Expected: `OK (skipped=17)`.

- [ ] **Step 7: Try it against a real gateway (optional, needs network)**

```bash
python3 -c "from uni_vpn import detect; d = detect.probe('vpn.uni-bremen.de'); print(d.groups, d.suggestion())"
```

Expected (2026-10-05): `['Tunnel-All-Traffic', 'Tunnel-Uni-Bremen'] {'host': 'vpn.uni-bremen.de', 'usergroup': '', 'authgroup': 'Tunnel-All-Traffic', 'mfa': 'totp_field'}`.

- [ ] **Step 8: Commit**

```bash
git add uni_vpn/detect.py tests/test_detect.py tests/fixtures/detect
git commit -m "detect groups, login fields and saml on a cisco gateway"
```

---

### Task 6: HTTP API for universities, detection and setup with a profile

**Files:**
- Modify: `uni_vpn/httpapi.py`
- Test: `tests/test_httpapi.py`

**Interfaces:**
- Consumes: `universities.public_list`, `DEFAULT_ID`, `FieldError`; `detect.split_address`, `detect.probe`, `Detection.as_dict`; `Daemon.complete_setup(user, password, token, university, overrides)`.
- Produces: `GET /universities.json` -> `{"default": "heidelberg", "universities": [...]}`; `POST /api/detect` with `{"host", "group"}` -> `{"ok": true, ...Detection.as_dict()}` or 400 `{"ok": false, "field", "error"}`; `POST /api/setup` takes `university` (default `heidelberg`), optional `profile` object, optional `secret`, answers `code: null` without TOTP; `HttpApi.detector` (defaults to `detect.probe`, replaced in tests).

Notes for the implementer:
- The probe blocks (urllib), so it runs in the default executor.
- `/api/detect` is a POST and therefore behind the existing `X-Uni-VPN` header and origin checks, like every other POST.
- The error for `/api/setup` names the field so the page can go back to the right step; `FieldError` must be caught before `ValueError` (it is a subclass).

- [ ] **Step 1: Write the failing tests**

Apply to `tests/test_httpapi.py`:

````diff
diff --git a/tests/test_httpapi.py b/tests/test_httpapi.py
--- a/tests/test_httpapi.py
+++ b/tests/test_httpapi.py
@@ -3,10 +3,11 @@ import json
 import time
 
 from uni_vpn import daemon as dm
-from uni_vpn import credentials, pac, totp
+from uni_vpn import config, credentials, detect, pac, totp
 from uni_vpn.httpapi import allowed_origin
 
 from tests.test_daemon import DaemonHarness, wait_state
+from tests.test_detect import fixture
 
 
 async def http(port, method, path, headers=None, body=b""):
@@ -332,6 +333,104 @@ class SetupTests(DaemonHarness):
         self.assertIsNone(d.status()["error_kind"])
 
 
+class UniversitySetupTests(SetupTests):
+    async def post_setup(self, body):
+        status, _, payload = await http(self.cfg.http_port, "POST", "/api/setup", self.HEADERS, json.dumps(body).encode())
+        return status, json.loads(payload)
+
+    async def test_university_without_totp_needs_no_secret(self):
+        self.totp = credentials.TotpMissing("x")
+        d = await self.start_setup_daemon()
+        status, data = await self.post_setup({"user": "ab123", "password": "pw", "university": "bonn"})
+        self.assertEqual(status, 200, data)
+        self.assertIsNone(data["code"])
+        self.assertIn('university = "bonn"', self.cfg_path.read_text())
+        self.assertEqual(self.stored, ["pw"])
+        self.assertEqual(self.stored_totp, [])
+        self.assertEqual((d.cfg.host, d.cfg.mfa, d.cfg.university_name), ("unibn-vpn.uni-bonn.de", "none", "University of Bonn"))
+        await wait_state(d, dm.State.connected)
+
+    async def test_unlisted_university_writes_its_profile(self):
+        self.totp = credentials.TotpMissing("x")
+        d = await self.start_setup_daemon()
+        profile = {"host": "VPN.Example.edu", "usergroup": "staff", "authgroup": "Staff (Split)", "mfa": "none"}
+        status, data = await self.post_setup({"user": "ab123", "password": "pw", "secret": "", "university": "other",
+                                              "profile": profile})
+        self.assertEqual(status, 200, data)
+        cfg = config.load(self.cfg_path)
+        self.assertEqual((cfg.university, cfg.host, cfg.usergroup, cfg.authgroup, cfg.mfa),
+                         ("other", "vpn.example.edu", "staff", "Staff (Split)", "none"))
+        self.assertEqual(d.cfg.host, "vpn.example.edu")
+        await wait_state(d, dm.State.connected)
+
+    async def test_errors_name_the_university_step(self):
+        await self.start_setup_daemon()
+        for body, field in (({"university": "fu-berlin"}, "university"),
+                            ({"university": "nowhere"}, "university"),
+                            ({"university": "other", "profile": {}}, "host"),
+                            ({"university": "other", "profile": {"host": "vpn.example.edu", "mfa": "sms"}}, "mfa"),
+                            ({"university": "heidelberg"}, "totp")):
+            status, data = await self.post_setup({"user": "ab123", "password": "pw", **body})
+            self.assertEqual((status, data["field"]), (400, field), body)
+        self.assertIn("browser", (await self.post_setup({"user": "ab1", "password": "pw", "university": "fu-berlin"}))[1]["error"])
+        self.assertFalse(self.cfg_path.exists())
+        self.assertEqual(self.stored, [])
+
+    async def test_user_with_a_realm_is_accepted(self):
+        self.totp = credentials.TotpMissing("x")
+        await self.start_setup_daemon()
+        status, data = await self.post_setup({"user": "st1@stud.uni-stuttgart.de", "password": "pw", "university": "stuttgart"})
+        self.assertEqual(status, 200, data)
+        self.assertEqual(config.load(self.cfg_path).login_name, "st1@stud.uni-stuttgart.de")
+
+
+class DetectTests(DaemonHarness):
+    HEADERS = {"X-Uni-VPN": "1", "Content-Type": "application/json"}
+
+    async def test_universities_json_lists_the_registry_without_notes(self):
+        await self.start_daemon()
+        status, _, payload = await http(self.cfg.http_port, "GET", "/universities.json")
+        self.assertEqual(status, 200)
+        data = json.loads(payload)
+        self.assertEqual(data["default"], "heidelberg")
+        heidelberg = next(u for u in data["universities"] if u["id"] == "heidelberg")
+        self.assertEqual(heidelberg["mfa_portal_url"], "https://mfa.uni-heidelberg.de/")
+        self.assertFalse([u for u in data["universities"] if "notes" in u])
+        self.assertIn("fu-berlin", [u["id"] for u in data["universities"]])
+
+    async def test_detect_runs_the_probe_and_returns_the_suggestion(self):
+        d = await self.start_daemon()
+        calls = []
+
+        def fake(host, usergroup="", group=""):
+            calls.append((host, usergroup, group))
+            return detect.parse_reply(fixture("bremen.xml"), host, usergroup)
+
+        d.http.detector = fake
+        body = json.dumps({"host": "https://VPN.uni-bremen.de/", "group": "Tunnel-Uni-Bremen"}).encode()
+        status, _, payload = await http(self.cfg.http_port, "POST", "/api/detect", self.HEADERS, body)
+        self.assertEqual(status, 200, payload)
+        data = json.loads(payload)
+        self.assertEqual(calls, [("vpn.uni-bremen.de", "", "Tunnel-Uni-Bremen")])
+        self.assertTrue(data["ok"])
+        self.assertEqual(data["groups"], ["Tunnel-All-Traffic", "Tunnel-Uni-Bremen"])
+        self.assertEqual(data["suggestion"]["mfa"], "totp_field")
+
+    async def test_detect_refuses_bad_input_without_probing(self):
+        d = await self.start_daemon()
+        d.http.detector = lambda *a: self.fail("no probe for invalid input")
+        for body, status_expected in ((b'{"host": "not a host"}', 400), (b'{"host": 5}', 400), (b"[]", 400),
+                                      (b'{"host": "vpn.example.edu", "group": "a\nb"}', 400)):
+            status, _, _ = await http(self.cfg.http_port, "POST", "/api/detect", self.HEADERS, body)
+            self.assertEqual(status, status_expected, body)
+
+    async def test_detect_needs_csrf_header(self):
+        await self.start_daemon()
+        status, _, _ = await http(self.cfg.http_port, "POST", "/api/detect", {"Content-Type": "application/json"},
+                                  b'{"host": "vpn.example.edu"}')
+        self.assertEqual(status, 403)
+
+
 class PacTests(DaemonHarness):
     HEADERS = {"X-Uni-VPN": "1", "Content-Type": "application/json"}
 
````

- [ ] **Step 2: Run them to make sure they fail**

Run: `python3 -m unittest tests.test_httpapi -v`
Expected: FAIL, among others `400 != 200 : {'ok': False, 'field': None, 'error': "expected JSON with 'user', 'password' and 'secret'"}` and `404 != 400` for `/api/detect`.

- [ ] **Step 3: Implement**

````diff
diff --git a/uni_vpn/httpapi.py b/uni_vpn/httpapi.py
--- a/uni_vpn/httpapi.py
+++ b/uni_vpn/httpapi.py
@@ -10,7 +10,8 @@ import re
 from pathlib import Path
 
 from . import config as config_mod
-from . import totp
+from . import detect, totp
+from . import universities as unis
 
 MAX_HEADER = 16 * 1024
 MAX_BODY = 64 * 1024
@@ -43,6 +44,7 @@ class HttpApi:
         self.host = host
         self.port = port
         self.log = log
+        self.detector = detect.probe  # tests replace it, there is no gateway in CI
         self._server: asyncio.AbstractServer | None = None
 
     async def start(self) -> None:
@@ -131,6 +133,9 @@ class HttpApi:
                 return 200, "application/json", json.dumps(self.daemon.status()).encode()
             if path == "/proxy.pac":
                 return 200, "application/x-ns-proxy-autoconfig", self.daemon.pac().encode()
+            if path == "/universities.json":
+                payload = {"default": unis.DEFAULT_ID, "universities": unis.public_list()}
+                return 200, "application/json", json.dumps(payload).encode()
             return 404, "text/plain", b"not found"
         if method != "POST":
             return 405, "text/plain", b"method not allowed"
@@ -201,6 +206,23 @@ class HttpApi:
             self.daemon.note_code_shown()
             return 200, "application/json", json.dumps({"ok": True, "code": totp.code(token),
                                                          "remaining": remaining}).encode()
+        elif path == "/api/detect":
+            # "Not listed" in the setup assistant: what the gateway's login form looks like.
+            try:
+                data = json.loads(body.decode("utf-8"))
+                address, group = data["host"], data.get("group", "")
+            except (ValueError, KeyError, TypeError, AttributeError, UnicodeDecodeError):
+                return 400, "text/plain", b"expected JSON with 'host'"
+            if not isinstance(address, str) or not isinstance(group, str):
+                return 400, "text/plain", b"host and group must be text"
+            try:
+                host, usergroup = detect.split_address(address)
+                result = await asyncio.get_running_loop().run_in_executor(
+                    None, lambda: self.detector(host, usergroup, group))
+            except unis.FieldError as exc:
+                payload = {"ok": False, "field": exc.field, "error": str(exc)}
+                return 400, "application/json", json.dumps(payload).encode()
+            return 200, "application/json", json.dumps({"ok": True, **result.as_dict()}).encode()
         elif path == "/api/setup":
             # Errors name the step that has to change, so the assistant can go back to it.
             def fail(status: int, field: str | None, message: str):
@@ -208,26 +230,33 @@ class HttpApi:
 
             try:
                 data = json.loads(body.decode("utf-8"))
-                user, password, secret = data["user"], data["password"], data["secret"]
-            except (ValueError, KeyError, TypeError, UnicodeDecodeError):
-                return fail(400, None, "expected JSON with 'user', 'password' and 'secret'")
-            if not all(isinstance(v, str) for v in (user, password, secret)):
-                return fail(400, None, "user, password and secret must be text")
+                user, password = data["user"], data["password"]
+                secret = data.get("secret", "")
+                university = data.get("university", unis.DEFAULT_ID)
+                overrides = data.get("profile") or {}
+            except (ValueError, KeyError, TypeError, AttributeError, UnicodeDecodeError):
+                return fail(400, None, "expected JSON with 'user' and 'password'")
+            if not all(isinstance(v, str) for v in (user, password, secret, university)) or not isinstance(overrides, dict):
+                return fail(400, None, "user, password, secret and university must be text, profile an object")
             if not config_mod.valid_user(user.strip()):
                 return fail(400, "user", "Invalid university ID")
             if not password or "\n" in password or "\r" in password:
                 return fail(400, "password", "Enter your password")
+            token = None
+            if secret.strip():
+                try:
+                    token = totp.normalize(secret)
+                except ValueError as exc:
+                    return fail(400, "totp", str(exc))
             try:
-                token = totp.normalize(secret)
-            except ValueError as exc:
-                return fail(400, "totp", str(exc))
-            try:
-                await self.daemon.complete_setup(user, password, token)
+                await self.daemon.complete_setup(user, password, token, university, overrides)
+            except unis.FieldError as exc:
+                return fail(400, exc.field, str(exc))
             except ValueError as exc:
                 return fail(400, "user", str(exc))
             except Exception as exc:  # noqa: BLE001 - the error text goes to the page
                 return fail(500, None, str(exc))
-            payload = {"ok": True, "state": self.daemon.state.value, "code": totp.code(token)}
+            payload = {"ok": True, "state": self.daemon.state.value, "code": totp.code(token) if token else None}
             return 200, "application/json", json.dumps(payload).encode()
         else:
             return 404, "text/plain", b"not found"
````

- [ ] **Step 4: Run the tests**

Run: `python3 -m unittest tests.test_httpapi -v`
Expected: PASS.

- [ ] **Step 5: Run the whole suite**

Run: `python3 -m unittest discover -s tests -t .`
Expected: `OK (skipped=17)`.

- [ ] **Step 6: Commit**

```bash
git add uni_vpn/httpapi.py tests/test_httpapi.py
git commit -m "api: university list, gateway detection, setup with a profile"
```

---

### Task 7: Setup assistant with university picker

**Files:**
- Modify: `uni_vpn/ui/index.html`
- Test: `tests/test_httpapi.py`

**Interfaces:**
- Consumes: `GET /universities.json`, `POST /api/detect`, `POST /api/setup` (Task 6), `/status.json` fields `university_name`, `mfa`, `domains` (Task 4); query parameters `user` and `university` (Task 8 passes `university`).
- Produces: new step `step0` (university), ids `s-uni` (combobox), `s-uni-list` (listbox), `s-other`, `s-host`, `s-detect`, `s-group`, `s-mfa`, `s0-err`, `s-steps`, `s3-back`, `s4-msg`, `v-uni`.

Design (spec section 9, role models eduroam CAT and Thunderbird account setup):
- Step 1 "University": one search field, matches by name, alias, host and id with accents and umlaut spellings folded (same rule as `universities.fold`), at most 7 matches plus "Not listed". Arrow keys and Enter pick; Enter with no selection picks the first match.
- "Not listed" shows the VPN address and a Detect button (Enter in the address field detects too). After detection a group select (only with several groups) and a second-factor select appear, prefilled from `suggestion`. Changing the group detects again (Stanford: the default group is SAML, `Stanford` is not). SAML shows one error line and blocks Next.
- The only help text is behind the info icon. The label "Uni-ID" becomes "Username" (some universities use a realm, `name@realm`).
- Step numbers adapt: 4 steps with TOTP, 3 without. The second-factor step renders `mfa_steps` with `{portal}` replaced by a link to `mfa_portal_url`, else three generic steps. The check code hint says "Check code" instead of Heidelberg's "Code for Testen" (Heidelberg's own step still names the "Testen" field).
- Errors from `/api/setup` with a profile field go back to step 1, `password`/`user` to Sign in, `totp` to Second factor.
- Done card: with an empty domain list (every university except Heidelberg for now) it points to Settings, Websites.
- Settings show the university and hide "Second factor" when the mode has none.

- [ ] **Step 1: Write the failing tests**

Apply to `tests/test_httpapi.py` (the old test asserted the Heidelberg portal URL inside the page; it now comes from the profile):

````diff
diff --git a/tests/test_httpapi.py b/tests/test_httpapi.py
--- a/tests/test_httpapi.py
+++ b/tests/test_httpapi.py
@@ -192,7 +192,18 @@ class TotpEndpointTests(DaemonHarness):
         await self.start_daemon()
         _, _, payload = await http(self.cfg.http_port, "GET", "/")
         self.assertIn(b"/api/totp", payload)
-        self.assertIn(b"mfa.uni-heidelberg.de", payload)
+        # The portal link comes from the profile now, not from the page.
+        self.assertNotIn(b"mfa.uni-heidelberg.de", payload)
+        _, _, payload = await http(self.cfg.http_port, "GET", "/status.json")
+        self.assertEqual(json.loads(payload)["mfa_portal_url"], "https://mfa.uni-heidelberg.de/")
+
+    async def test_status_page_has_the_university_picker(self):
+        await self.start_daemon()
+        _, _, payload = await http(self.cfg.http_port, "GET", "/")
+        for needle in (b'id="s-uni"', b'role="combobox"', b"/universities.json", b"/api/detect", b'id="s-mfa"',
+                       b"{portal}", b"university: uni.id"):
+            self.assertIn(needle, payload)
+        self.assertNotIn("\u2014".encode(), payload, "no em-dashes")
 
     async def test_totp_endpoint_normalizes_stores_and_answers_with_check_code(self):
         d = await self.start_daemon()
````

- [ ] **Step 2: Run them to make sure they fail**

Run: `python3 -m unittest tests.test_httpapi -v`
Expected: FAIL with `b'id="s-uni"' not found` and `b'mfa.uni-heidelberg.de' unexpectedly found`.

- [ ] **Step 3: Implement**

Apply to `uni_vpn/ui/index.html`:

````diff
diff --git a/uni_vpn/ui/index.html b/uni_vpn/ui/index.html
--- a/uni_vpn/ui/index.html
+++ b/uni_vpn/ui/index.html
@@ -38,9 +38,9 @@ h2{font-size:24px;font-weight:500;margin:0 0 6px;outline:none}
 .field>.row-title{display:flex;margin-bottom:6px}
 .field>.row-title label{margin:0}
 .input{position:relative}
-input,textarea{width:100%;font:inherit;color:var(--text);background:var(--field);border:1px solid var(--line);border-radius:10px;padding:11px 12px;outline:none}
+input,textarea,select{width:100%;font:inherit;color:var(--text);background:var(--field);border:1px solid var(--line);border-radius:10px;padding:11px 12px;outline:none}
 input::placeholder,textarea::placeholder{color:var(--muted);opacity:.7}
-input:focus,textarea:focus{border-color:var(--accent);box-shadow:0 0 0 1px var(--accent)}
+input:focus,textarea:focus,select:focus{border-color:var(--accent);box-shadow:0 0 0 1px var(--accent)}
 .input input{padding-right:44px}
 .input .icon-btn{position:absolute;right:2px;top:2px}
 .error{color:var(--bad);font-size:13px;margin:-8px 0 14px}
@@ -91,6 +91,11 @@ pre{font-size:12px;background:var(--bg);border-radius:10px;padding:10px;max-heig
 .steps{margin:0 0 20px;padding-left:20px;display:grid;gap:6px}
 .steps a{color:var(--accent);font-weight:500}
 .code .hint{font-size:13px;color:var(--muted);margin-left:auto;text-align:right}
+.options{list-style:none;margin:6px 0 0;padding:4px;border:1px solid var(--line);border-radius:10px;max-height:264px;overflow:auto}
+.options li{display:flex;justify-content:space-between;gap:10px;padding:9px 10px;border-radius:8px;cursor:pointer}
+.options li[aria-selected=true]{background:var(--bg)}
+.options li:last-child{color:var(--accent)}
+#s-detect{margin:-8px 0 12px -8px}
 .offline{background:var(--bad);color:var(--on-color);border-radius:10px;padding:10px 14px;margin-bottom:12px;font-size:14px}
 </style>
 </head>
@@ -102,29 +107,45 @@ pre{font-size:12px;background:var(--bg);border-radius:10px;padding:10px;max-heig
   <section id="setup" hidden>
     <div class="bar"><h1>Uni VPN</h1></div>
     <div class="card">
-      <form id="step1" autocomplete="on">
-        <p class="step">Step 1 of 3</p>
+      <form id="step0" autocomplete="off">
+        <p class="step"></p>
+        <h2>University</h2>
+        <div class="field"><span class="row-title"><label for="s-uni">Find your university</label>
+          <span class="info"><button type="button" aria-label="More help"><svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="10"/><path d="M12 16v-4M12 8h.01"/></svg></button>
+            <span class="pop" hidden>Not in the list? Choose "Not listed" and enter the address your university gives for Cisco AnyConnect or Cisco Secure Client.</span></span></span>
+          <input id="s-uni" role="combobox" aria-expanded="false" aria-controls="s-uni-list" aria-autocomplete="list" autocomplete="off" spellcheck="false" placeholder="Name or city">
+          <ul class="options" id="s-uni-list" role="listbox" aria-label="Universities" hidden></ul></div>
+        <div id="s-other" hidden>
+          <label class="field"><span>VPN address</span>
+            <input id="s-host" autocapitalize="none" spellcheck="false" placeholder="vpn.example.edu"></label>
+          <button class="btn text" type="button" id="s-detect">Detect</button>
+          <label class="field" id="s-group-row" hidden><span>Group</span><select id="s-group"></select></label>
+          <label class="field" id="s-mfa-row" hidden><span>Second factor</span><select id="s-mfa">
+            <option value="none">None</option><option value="totp_field">One-time code, own field</option>
+            <option value="totp_append">One-time code after the password</option><option value="duo_push">Duo push</option></select></label>
+        </div>
+        <p class="error" id="s0-err" role="alert" hidden></p>
+        <div class="actions"><span></span><button class="btn primary" type="submit">Next</button></div>
+      </form>
+
+      <form id="step1" autocomplete="on" hidden>
+        <p class="step"></p>
         <h2>Sign in</h2>
         <p class="sub">Your university account, the same as for email.</p>
-        <label class="field"><span>Uni-ID</span>
+        <label class="field"><span>Username</span>
           <input id="s-user" name="username" autocomplete="username" autocapitalize="none" spellcheck="false" placeholder="ab123"></label>
         <label class="field"><span>Password</span>
           <div class="input"><input id="s-pw" type="password" name="password" autocomplete="current-password">
             <button type="button" class="icon-btn reveal" data-for="s-pw" aria-label="Show password"><svg viewBox="0 0 24 24"><path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7S2 12 2 12z"/><circle cx="12" cy="12" r="3"/></svg></button></div></label>
         <p class="error" id="s1-err" role="alert" hidden></p>
-        <div class="actions"><span></span><button class="btn primary" type="submit">Next</button></div>
+        <div class="actions"><button class="btn text" type="button" data-back="step0">Back</button><button class="btn primary" type="submit">Next</button></div>
       </form>
 
       <form id="step2" hidden>
-        <p class="step">Step 2 of 3</p>
+        <p class="step"></p>
         <h2>Second factor</h2>
         <p class="sub">Add this computer as a token, like a second phone.</p>
-        <ol class="steps">
-          <li>Open the <a href="https://mfa.uni-heidelberg.de/" target="_blank" rel="noopener">MFA portal</a>. It only opens on campus or with Cisco VPN.</li>
-          <li>Choose "Soft-Token (zeitbasiert)", then "Einrichten".</li>
-          <li>Click "Tokendetails einblenden" and copy the line starting with otpauth://.</li>
-          <li>Paste it below, then type the code shown here into the portal's "Testen" field.</li>
-        </ol>
+        <ol class="steps" id="s-steps"></ol>
         <div class="field"><span class="row-title"><label for="s-totp">Token secret</label>
           <span class="info"><button type="button" aria-label="More help"><svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="10"/><path d="M12 16v-4M12 8h.01"/></svg></button>
             <span class="pop" hidden>No otpauth:// line? Copy the letters after "secret=" instead. Already have a token on your phone? Add a second one for this computer; both keep working.</span></span></span>
@@ -133,23 +154,23 @@ pre{font-size:12px;background:var(--bg);border-radius:10px;padding:10px;max-heig
         <p class="error" id="s2-err" role="alert" hidden></p>
         <div class="code" id="s-code" hidden>
           <svg class="ring" viewBox="0 0 24 24"><circle class="bg" cx="12" cy="12" r="10"/><circle class="fg" cx="12" cy="12" r="10"/></svg>
-          <b></b><span class="hint">Code for "Testen"</span>
+          <b></b><span class="hint">Check code</span>
         </div>
         <div class="actions"><button class="btn text" type="button" data-back="step1">Back</button><button class="btn primary" type="submit" disabled>Next</button></div>
       </form>
 
       <div id="step3" hidden class="center">
         <div class="spinner"></div>
-        <p class="step">Step 3 of 3</p>
+        <p class="step"></p>
         <h2 tabindex="-1">Connecting</h2>
         <p class="sub" id="s3-msg" aria-live="polite">&nbsp;</p>
-        <div class="actions"><button class="btn text" type="button" data-back="step2">Back</button><button class="btn text" type="button" id="s3-skip" hidden>Finish</button></div>
+        <div class="actions"><button class="btn text" type="button" id="s3-back">Back</button><button class="btn text" type="button" id="s3-skip" hidden>Finish</button></div>
       </div>
 
       <div id="step4" hidden class="center">
         <div class="badge ok"><svg viewBox="0 0 24 24"><path d="M5 12.5l4.5 4.5L19 7.5"/></svg></div>
         <h2 tabindex="-1">All set</h2>
-        <p class="sub">University websites now open through the VPN by themselves. Close and reopen your browser once.</p>
+        <p class="sub" id="s4-msg">University websites now open through the VPN by themselves. Close and reopen your browser once.</p>
         <div class="actions" style="justify-content:center"><button class="btn primary" type="button" id="s4-done">Done</button></div>
       </div>
     </div>
@@ -173,7 +194,8 @@ pre{font-size:12px;background:var(--bg);border-radius:10px;padding:10px;max-heig
     <div class="bar"><button class="icon-btn" id="close-settings" aria-label="Back"><svg viewBox="0 0 24 24"><path d="M15 18l-6-6 6-6"/></svg></button><h1>Settings</h1><span style="width:40px"></span></div>
     <p class="group">Account</p>
     <div class="list">
-      <div class="item"><span>Uni-ID</span><span class="small" id="v-user"></span></div>
+      <div class="item"><span>University</span><span class="small" id="v-uni"></span></div>
+      <div class="item"><span>Username</span><span class="small" id="v-user"></span></div>
       <details id="d-pw"><summary><span>Password</span></summary><div class="body">
         <form id="pwform"><div class="input"><input type="password" id="pw" autocomplete="current-password" aria-label="Password">
           <button type="button" class="icon-btn reveal" data-for="pw" aria-label="Show password"><svg viewBox="0 0 24 24"><path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7S2 12 2 12z"/><circle cx="12" cy="12" r="3"/></svg></button></div>
@@ -219,7 +241,8 @@ function show(id) {
 }
 
 function step(id, focusId) {
-  for (const s of ["step1", "step2", "step3", "step4"]) $(s).hidden = s !== id;
+  for (const s of ["step0", "step1", "step2", "step3", "step4"]) $(s).hidden = s !== id;
+  if (id === "step2") renderMfaSteps();
   const focus = focusId ? $(focusId) : $(id).querySelector("input") || $(id).querySelector("h2");
   if (focus) focus.focus();
 }
@@ -256,7 +279,9 @@ function render() {
   $("toggle").className = fix ? "btn text" : "btn primary big";
   $("toggle").textContent = isOn() ? "Disconnect" : fix ? "Try again" : "Connect";
   $("toggle").disabled = S.state === "disconnecting";
+  $("v-uni").textContent = S.university_name;
   $("v-user").textContent = S.user;
+  $("d-totp").hidden = !TOTP_MODES.includes(S.mfa);
   $("v-ver").textContent = S.version;
   $("v-dom").textContent = (S.domains || []).length + " domains";
   // Only on change, so a selection in the log survives the next poll.
@@ -281,7 +306,7 @@ async function refresh() {
     refreshing = false;
   }
   if (S.setup_needed && !setupDone) {
-    if (view !== "setup") { show("setup"); step("step1", $("s-user").value ? "s-pw" : null); }
+    if (view !== "setup") { show("setup"); await loadUniversities(); step(uni ? "step1" : "step0", uni && $("s-user").value ? "s-pw" : null); }
     return;
   }
   if (view === "setup" && !setupSent) {
@@ -293,16 +318,158 @@ async function refresh() {
 }
 
 // --- setup assistant ---------------------------------------------------------
-// The installer passes the ID it was given (setup --user) in the address.
-const givenUser = new URLSearchParams(location.search).get("user");
-if (givenUser && /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/.test(givenUser)) $("s-user").value = givenUser;
+// Role models: eduroam CAT (find your institution), Thunderbird account setup (list, detect, manual).
+const USER_RE = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}(@[A-Za-z0-9][A-Za-z0-9.-]{0,252})?$/;
+const TOTP_MODES = ["totp_field", "totp_append"];
+const OTHER = {id: "other", name: "Not listed", country: "", mfa: "none", mfa_portal_url: "", mfa_steps: []};
+const SAML = "This university signs in through a browser, uni-vpn does not support that yet.";
+const GENERIC_STEPS = ["Open your university's {portal} and add a time-based token (TOTP) for this computer.",
+  "Copy the line starting with otpauth://, or the letters after secret=.", "Paste it below."];
+let UNIS = null, uni = null, active = -1, detectSeq = 0;
+
+// The installer passes what it was given (setup --user, --university) in the address.
+const params = new URLSearchParams(location.search);
+const givenUser = params.get("user");
+if (givenUser && USER_RE.test(givenUser)) $("s-user").value = givenUser;
+
+async function loadUniversities() {
+  if (UNIS) return;
+  try { UNIS = (await (await fetch("/universities.json", {cache: "no-store"})).json()).universities; } catch (e) { UNIS = []; }
+  const given = UNIS.find((u) => u.id === params.get("university"));
+  if (given && given.mfa !== "saml") pick(given);
+  numberSteps();
+}
+
+// Lower case without accents and umlaut spellings, like the registry's own search.
+const fold = (s) => String(s || "").normalize("NFKD").replace(/[\u0300-\u036f]/g, "").toLowerCase()
+  .replace(/ue/g, "u").replace(/oe/g, "o").replace(/ae/g, "a");
+
+function matches(query) {
+  const q = fold(query.trim());
+  return (UNIS || []).filter((u) => !q || [u.id, u.name, u.host, ...(u.aliases || [])].some((t) => fold(t).includes(q)));
+}
+
+function mfa() { return uni && uni.id === "other" ? $("s-mfa").value : uni ? uni.mfa : "totp_field"; }
+function needsTotp() { return TOTP_MODES.includes(mfa()); }
+
+function numberSteps() {
+  const ids = ["step0", "step1", ...(needsTotp() ? ["step2"] : []), "step3"];
+  ids.forEach((id, i) => { $(id).querySelector(".step").textContent = `Step ${i + 1} of ${ids.length}`; });
+}
+
+function closeList() { $("s-uni-list").hidden = true; $("s-uni").setAttribute("aria-expanded", "false"); active = -1; }
+
+function renderList() {
+  const list = $("s-uni-list"), found = [...matches($("s-uni").value).slice(0, 7), OTHER];
+  active = Math.min(active, found.length - 1);
+  list.replaceChildren(...found.map((u, i) => {
+    const li = document.createElement("li");
+    li.id = "s-uni-" + u.id;
+    li.setAttribute("role", "option");
+    li.setAttribute("aria-selected", String(i === active));
+    li.textContent = u.name;
+    if (u.country) { const c = document.createElement("span"); c.className = "small"; c.textContent = u.country; li.append(c); }
+    li.onmousedown = (e) => { e.preventDefault(); pick(u); };
+    return li;
+  }));
+  list.hidden = false;
+  $("s-uni").setAttribute("aria-expanded", "true");
+  $("s-uni").setAttribute("aria-activedescendant", active >= 0 ? "s-uni-" + found[active].id : "");
+  return found;
+}
+
+function pick(u) {
+  uni = u;
+  $("s-uni").value = u.name;
+  closeList();
+  $("s-other").hidden = u.id !== "other";
+  err("s0-err", u.mfa === "saml" ? SAML : "");
+  numberSteps();
+  if (u.id === "other") $("s-host").focus();
+}
+
+$("s-uni").oninput = () => { uni = null; $("s-other").hidden = true; err("s0-err", ""); active = -1; renderList(); };
+$("s-uni").onfocus = () => { if (!uni) renderList(); };
+$("s-uni").onblur = closeList;
+$("s-uni").onkeydown = (e) => {
+  if (e.key === "Escape") { closeList(); return; }
+  if (!["ArrowDown", "ArrowUp", "Enter"].includes(e.key) || (e.key === "Enter" && uni)) return;
+  e.preventDefault();
+  if (e.key === "Enter") { const found = renderList(); pick(found[Math.max(active, 0)]); return; }
+  active += e.key === "ArrowDown" ? 1 : -1;
+  if (active < 0) active = 0;
+  renderList();
+};
+
+async function detect(group) {
+  const host = $("s-host").value.trim(), seq = ++detectSeq;
+  if (!host) { err("s0-err", "Enter the VPN address"); return; }
+  err("s0-err", "");
+  $("s-detect").disabled = true;
+  const r = await send("/api/detect", {host, group});
+  if (seq !== detectSeq) return;
+  $("s-detect").disabled = false;
+  const d = r.data;
+  $("s-mfa-row").hidden = false;
+  if (!d || !d.ok) { err("s0-err", (d && d.error) || r.text); return; }
+  if (d.error) { err("s0-err", d.error); return; }
+  $("s-group").replaceChildren(...d.groups.map((g) => new Option(g, g, false, g === d.group)));
+  $("s-group-row").hidden = d.groups.length < 2;
+  if (d.saml) { err("s0-err", SAML); return; }
+  $("s-mfa").value = d.suggestion.mfa;
+  numberSteps();
+}
+$("s-detect").onclick = () => detect("");
+$("s-group").onchange = () => detect($("s-group").value);
+$("s-mfa").onchange = numberSteps;
+
+// For "Not listed": what goes into config.toml besides the university.
+function profileOverrides() {
+  if (!uni || uni.id !== "other") return {};
+  const [host, ...path] = $("s-host").value.trim().replace(/^https?:\/\//i, "").split("/");
+  const p = {host, mfa: $("s-mfa").value};
+  if (path.join("/")) p.usergroup = path.join("/");
+  if (!$("s-group-row").hidden && $("s-group").value) p.authgroup = $("s-group").value;
+  return p;
+}
+
+$("step0").onsubmit = (e) => {
+  e.preventDefault();
+  if (!uni) { err("s0-err", "Choose your university, or Not listed"); return; }
+  if (uni.mfa === "saml") { err("s0-err", SAML); return; }
+  if (uni.id === "other" && !$("s-host").value.trim()) { err("s0-err", "Enter the VPN address"); return; }
+  if (uni.id === "other" && $("s-mfa-row").hidden) { detect(""); return; }
+  err("s0-err", "");
+  step("step1");
+};
+
+function renderMfaSteps() {
+  const steps = uni && uni.mfa_steps && uni.mfa_steps.length ? uni.mfa_steps : GENERIC_STEPS;
+  $("s-steps").replaceChildren(...steps.map((text) => {
+    const li = document.createElement("li"), [before, after] = text.split("{portal}");
+    li.append(before);
+    if (after !== undefined) {
+      const url = uni && uni.mfa_portal_url;
+      if (url) {
+        const a = document.createElement("a");
+        Object.assign(a, {href: url, target: "_blank", rel: "noopener", textContent: "MFA portal"});
+        li.append(a);
+      } else {
+        li.append("MFA portal");
+      }
+      li.append(after);
+    }
+    return li;
+  }));
+}
+
 $("step1").onsubmit = (e) => {
   e.preventDefault();
   const user = $("s-user").value.trim();
-  if (!/^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/.test(user)) { err("s1-err", "Enter your Uni-ID, for example ab123"); return; }
+  if (!USER_RE.test(user)) { err("s1-err", "Enter your username, for example ab123"); return; }
   if (!$("s-pw").value) { err("s1-err", "Enter your password"); return; }
   err("s1-err", "");
-  step("step2");
+  if (needsTotp()) step("step2"); else submitSetup();
 };
 
 let checkSeq = 0;
@@ -334,8 +501,10 @@ function showCode(code, remaining, secret) {
   codeTimer = setInterval(tick, 1000);
 }
 
-$("step2").onsubmit = async (e) => {
-  e.preventDefault();
+$("step2").onsubmit = (e) => { e.preventDefault(); submitSetup(); };
+
+const PROFILE_FIELDS = ["university", "host", "usergroup", "authgroup", "mfa"];
+async function submitSetup() {
   if (submitting) return;
   submitting = setupSent = true;
   clearInterval(codeTimer);
@@ -344,7 +513,9 @@ $("step2").onsubmit = async (e) => {
   $("s3-skip").hidden = true;
   let r;
   try {
-    r = await post("/api/setup", {user: $("s-user").value.trim(), password: $("s-pw").value, secret: $("s-totp").value.trim()});
+    r = await post("/api/setup", {user: $("s-user").value.trim(), password: $("s-pw").value,
+                                  secret: needsTotp() ? $("s-totp").value.trim() : "", university: uni.id,
+                                  profile: profileOverrides()});
   } catch (e) {
     r = {ok: false, data: null, text: "uni-vpn is not running. Run: uni-vpn service start"};
   } finally {
@@ -352,22 +523,25 @@ $("step2").onsubmit = async (e) => {
   }
   if (!r.ok) {
     const field = r.data && r.data.field, text = (r.data && r.data.error) || r.text;
-    if (field === "user" || field === "password") {
-      step("step1", field === "user" ? "s-user" : "s-pw");
-      err("s1-err", text);
-    } else {
+    if (PROFILE_FIELDS.includes(field)) {
+      step("step0");
+      err("s0-err", text);
+    } else if (field === "totp" && needsTotp()) {
       step("step2");
       err("s2-err", text);
+    } else {
+      step("step1", field === "password" ? "s-pw" : "s-user");
+      err("s1-err", text);
     }
     return;
   }
   setupDone = true;
   connectRequested = Date.now();
-};
+}
 
 function setupProgress() {
   if (!setupDone || $("step3").hidden) return;
-  if (S.state === "connected") { step("step4"); return; }
+  if (S.state === "connected") { finish(); return; }
   const settled = Date.now() - connectRequested > 1500;
   if (["auth_failed", "keyring"].includes(S.state) && settled) {
     const totp = S.error_kind === "totp";
@@ -388,7 +562,15 @@ function setupProgress() {
 }
 
 for (const b of document.querySelectorAll("[data-back]")) b.onclick = () => { if (!submitting) { setupDone = false; step(b.dataset.back); } };
-$("s3-skip").onclick = () => { if (setupDone) step("step4"); };
+$("s3-back").onclick = () => { if (!submitting) { setupDone = false; step(needsTotp() ? "step2" : "step1"); } };
+// Without default domains nothing goes through the university yet: say where to add them.
+function finish() {
+  $("s4-msg").textContent = (S.domains || []).length
+    ? "University websites now open through the VPN by themselves. Close and reopen your browser once."
+    : "Add the websites that should go through the university under Settings, Websites.";
+  step("step4");
+}
+$("s3-skip").onclick = () => { if (setupDone) finish(); };
 $("s4-done").onclick = () => { show("main"); render(); refresh(); };
 
 // --- main and settings -----------------------------------------------------
````

- [ ] **Step 4: Run the tests and a syntax check of the script**

Run: `python3 -m unittest tests.test_httpapi -v`
Expected: PASS.

Run (if Node is installed): `python3 -c "import re;print(re.search(r'<script>(.*)</script>',open('uni_vpn/ui/index.html').read(),re.S).group(1))" > /tmp/ui.js && node --check /tmp/ui.js`
Expected: no output.

- [ ] **Step 5: Check it in a browser**

Stop the service so the ports are free (`uni-vpn service stop`), then start a daemon in setup mode with an empty config directory:

```bash
XDG_CONFIG_HOME=$(mktemp -d) python3 bin/uni-vpn daemon
```

Open `http://127.0.0.1:1081/` and check:
1. "Step 1 of 4", search "zurich" finds ETH Zurich, "münster" finds Muenster; picking Bonn changes it to "Step 1 of 3".
2. "fu berlin" shows the SAML line and Next does nothing.
3. "Not listed", address `vpn.uni-bremen.de`, Detect: Group shows Tunnel-All-Traffic and Tunnel-Uni-Bremen, Second factor shows "One-time code, own field". Address `su-vpn.stanford.edu`: SAML line; choosing group Stanford clears it.
4. With `?university=heidelberg&user=ab1` the assistant opens at Sign in with the ID filled in; the Second factor step shows today's four Heidelberg steps with the portal link.
5. Narrow window (360 px): the list and selects fit, no horizontal scroll.
Do not finish the setup (it would store secrets in the real keyring). Stop the daemon with Ctrl+C and start the service again (`uni-vpn service start`).

- [ ] **Step 6: Run the whole suite**

Run: `python3 -m unittest discover -s tests -t .`
Expected: `OK (skipped=17)`.

- [ ] **Step 7: Commit**

```bash
git add uni_vpn/ui/index.html tests/test_httpapi.py
git commit -m "app: pick the university or detect it, steps follow its second factor"
```

---

### Task 8: Terminal setup, CLI and installers

**Files:**
- Modify: `uni_vpn/setup.py`
- Modify: `uni_vpn/cli.py`
- Modify: `install.sh`, `install.ps1`
- Test: `tests/test_setup.py`, `tests/test_cli.py`

**Interfaces:**
- Consumes: `universities.get`, `search`, `OTHER_ID`, `DEFAULT_ID`, `FieldError`; `detect.split_address`, `detect.probe`; `config.write_initial(..., university, overrides)`, `config.profile_config`, `Config.needs_totp`, `Config.university_name`, `Config.mfa_portal_url`.
- Produces: `setup.UNIVERSITY_PROMPT`, `setup.MFA_CHOICES`, `setup.totp_hint(cfg) -> str` (replaces `TOTP_HINT`), `setup.ask_other(input_fn, probe) -> tuple[str, dict] | None`, `setup.choose_university(input_fn, probe) -> tuple[str, dict] | None`, `setup.setup(..., probe=detect.probe)`; CLI `setup --university ID`; `uni-vpn totp` exits 2 when the university has no TOTP; `install.sh --university ID`, `install.ps1 -University ID`, both passed to `setup` and, for the browser, as `?university=`.

Notes for the implementer:
- The question only comes in the terminal path for a new config (no desktop, or `--no-gui`). Enter means Heidelberg, so a returning Heidelberg user who just presses Enter gets today's result. Three answers that match nothing stop setup with exit code 1, so a test or script feeding the same answer cannot loop forever.
- A dry run never asks (CI runs `./install.sh --dry-run --user citest` without a terminal).
- An existing `config.toml` is never switched to another university by setup; it prints how to do it by hand.
- The test harness's `input_fn` now answers the university question with Enter (`SetupHarness.ask`); tests that passed their own `lambda p: "x"` would otherwise hit the three-miss limit.

- [ ] **Step 1: Write the failing tests**

Apply to `tests/test_setup.py`:

````diff
diff --git a/tests/test_setup.py b/tests/test_setup.py
--- a/tests/test_setup.py
+++ b/tests/test_setup.py
@@ -11,10 +11,12 @@ from contextlib import redirect_stdout
 from pathlib import Path
 from unittest import mock
 
-from uni_vpn import doctor, service, sysproxy
+from uni_vpn import config, detect, doctor, service, sysproxy
 from uni_vpn import platform as pf
 from uni_vpn import setup
 
+from tests.test_detect import fixture
+
 
 class SetupHarness(unittest.TestCase):
     def setUp(self):
@@ -59,13 +61,17 @@ class SetupHarness(unittest.TestCase):
         return [target]
 
     def args(self, **kwargs):
-        base = {"dry_run": False, "user": None, "yes": False}
+        base = {"dry_run": False, "user": None, "university": None, "yes": False}
         base.update(kwargs)
         return argparse.Namespace(**base)
 
     def answer(self, prompt):
         return "pw" if "password" in prompt else "gezd gnbv gy3t qojq gezd gnbv gy3t qojq"
 
+    def ask(self, prompt):
+        # Enter at the university question keeps Heidelberg, as before profiles existed.
+        return "" if prompt == setup.UNIVERSITY_PROMPT else "ab123"
+
     def fake_proxy_install(self, http_port):
         self.proxy_calls.append(http_port)
         return self.proxy_result
@@ -73,7 +79,7 @@ class SetupHarness(unittest.TestCase):
     def run_setup(self, service_install=None, run_doctor=False, port_open=lambda port: True, getpass_fn=None, **kwargs):
         out = StringIO()
         with redirect_stdout(out):
-            rc = setup.setup(self.args(**kwargs), input_fn=lambda prompt: "ab123", getpass_fn=getpass_fn or self.answer,
+            rc = setup.setup(self.args(**kwargs), input_fn=self.ask, getpass_fn=getpass_fn or self.answer,
                              service_install=service_install or self.fake_service_install,
                              store=lambda user, pw: self.stored.append((user, pw)),
                              store_totp=lambda user, token: self.stored_totp.append((user, token)),
@@ -309,7 +315,7 @@ class SetupTests(SetupHarness):
     def test_existing_secrets_not_asked_again(self):
         out = StringIO()
         with redirect_stdout(out):
-            rc = setup.setup(self.args(user="ab123"), input_fn=lambda p: "x", getpass_fn=lambda p: (_ for _ in ()).throw(AssertionError("must not ask")),
+            rc = setup.setup(self.args(user="ab123"), input_fn=self.ask, getpass_fn=lambda p: (_ for _ in ()).throw(AssertionError("must not ask")),
                              service_install=self.fake_service_install, store=self.stored.append,
                              store_totp=self.stored_totp.append, proxy_install=self.fake_proxy_install,
                              keyring_probe=lambda user, kind="password": "present", run_doctor=False)
@@ -354,6 +360,11 @@ class GuiSetupTests(SetupHarness):
         self.assertEqual(self.urls, ["http://127.0.0.1:1081/?user=ab123"])
         self.assertFalse((self.home / ".config" / "uni-vpn" / "config.toml").exists())
 
+    def test_given_university_is_passed_to_the_assistant(self):
+        rc, out = self.run_gui(user="ab123", university="ethz")
+        self.assertEqual(rc, 0, out)
+        self.assertEqual(self.urls, ["http://127.0.0.1:1081/?user=ab123&university=ethz"])
+
     def test_without_a_browser_it_says_where_to_go(self):
         rc, out = self.run_gui(opened=False)
         self.assertEqual(rc, 0)
@@ -362,7 +373,7 @@ class GuiSetupTests(SetupHarness):
     def test_no_gui_flag_asks_in_the_terminal(self):
         out = StringIO()
         with redirect_stdout(out):
-            rc = setup.setup(self.args(no_gui=True), input_fn=lambda p: "ab123", getpass_fn=self.answer,
+            rc = setup.setup(self.args(no_gui=True), input_fn=self.ask, getpass_fn=self.answer,
                              service_install=self.fake_service_install,
                              store=lambda user, pw: self.stored.append((user, pw)),
                              store_totp=lambda user, token: self.stored_totp.append((user, token)),
@@ -388,6 +399,131 @@ class GuiSetupTests(SetupHarness):
         self.assertIn("Not a university ID", out)
 
 
+class UniversityChoiceTests(SetupHarness):
+    def run_terminal(self, answers, probe=None, **kwargs):
+        """answers: prompt start -> list of answers, used in order."""
+        self.prompts = []
+        queue = {start: list(values) for start, values in answers.items()}
+
+        def ask(prompt):
+            self.prompts.append(prompt)
+            for start, values in queue.items():
+                if prompt.startswith(start) and values:
+                    return values.pop(0)
+            return "ab123" if prompt.startswith("University ID") else ""
+
+        def no_probe(*args):
+            raise AssertionError("no probe expected")
+
+        out = StringIO()
+        with redirect_stdout(out):
+            rc = setup.setup(self.args(**kwargs), input_fn=ask, getpass_fn=self.answer,
+                             service_install=self.fake_service_install,
+                             store=lambda user, pw: self.stored.append((user, pw)),
+                             store_totp=lambda user, token: self.stored_totp.append((user, token)),
+                             keyring_probe=lambda user, kind="password": self.keyring[kind],
+                             proxy_install=self.fake_proxy_install, run_doctor=False, port_open=lambda port: True,
+                             probe=probe or no_probe)
+        return rc, out.getvalue()
+
+    def config_path(self):
+        return self.home / ".config" / "uni-vpn" / "config.toml"
+
+    def test_enter_keeps_heidelberg(self):
+        rc, out = self.run_terminal({})
+        self.assertEqual(rc, 0, out)
+        self.assertIn('university = "heidelberg"', self.config_path().read_text())
+        self.assertEqual(len(self.stored_totp), 1)
+        self.assertIn("mfa.uni-heidelberg.de", out)
+
+    def test_a_name_picks_the_profile_and_skips_the_totp_question(self):
+        rc, out = self.run_terminal({"Your university": ["bonn"]})
+        self.assertEqual(rc, 0, out)
+        self.assertEqual(config.load(self.config_path()).university, "bonn")
+        self.assertIn("University of Bonn", out)
+        self.assertEqual(self.stored, [("ab123", "pw")])
+        self.assertEqual(self.stored_totp, [])
+        self.assertNotIn("TOTP", out)
+
+    def test_an_ambiguous_name_asks_again(self):
+        rc, out = self.run_terminal({"Your university": ["universit", "mannheim"]})
+        self.assertEqual(rc, 0, out)
+        self.assertIn("Which one?", out)
+        self.assertEqual(config.load(self.config_path()).university, "mannheim")
+
+    def test_three_misses_stop_without_a_config(self):
+        rc, out = self.run_terminal({"Your university": ["atlantis", "atlantis", "atlantis"]})
+        self.assertEqual(rc, 1)
+        self.assertIn("Type other", out)
+        self.assertFalse(self.config_path().exists())
+
+    def test_saml_university_is_refused(self):
+        rc, out = self.run_terminal({"Your university": ["oxford"]})
+        self.assertEqual(rc, 1)
+        self.assertIn("browser", out)
+        self.assertFalse(self.config_path().exists())
+
+    def test_other_probes_the_gateway_and_writes_the_profile(self):
+        calls = []
+
+        def probe(host, usergroup="", group=""):
+            calls.append((host, usergroup, group))
+            return detect.parse_reply(fixture("bremen.xml"), host, usergroup)
+
+        rc, out = self.run_terminal({"Your university": ["other"], "VPN address": ["https://vpn.example.edu/staff"]},
+                                    probe=probe)
+        self.assertEqual(rc, 0, out)
+        self.assertEqual(calls, [("vpn.example.edu", "staff", "")])
+        cfg = config.load(self.config_path())
+        self.assertEqual((cfg.university, cfg.host, cfg.usergroup, cfg.authgroup, cfg.mfa),
+                         ("other", "vpn.example.edu", "staff", "Tunnel-All-Traffic", "totp_field"))
+        self.assertEqual(len(self.stored_totp), 1)
+
+    def test_other_with_another_group_probes_again(self):
+        def probe(host, usergroup="", group=""):
+            return detect.parse_reply(fixture("stanford-group-stanford.xml" if group else "stanford.xml"), host)
+
+        rc, out = self.run_terminal({"Your university": ["other"], "VPN address": ["su-vpn.stanford.edu"],
+                                     "Group": ["Stanford"], "Second factor": ["duo_push"]}, probe=probe)
+        self.assertEqual(rc, 0, out)
+        cfg = config.load(self.config_path())
+        self.assertEqual((cfg.authgroup, cfg.mfa), ("Stanford", "duo_push"))
+        self.assertEqual(self.stored_totp, [])
+
+    def test_other_with_saml_stops(self):
+        rc, out = self.run_terminal({"Your university": ["other"], "VPN address": ["vpn.fu-berlin.de"]},
+                                    probe=lambda host, usergroup="", group="": detect.parse_reply(fixture("fu-berlin.xml"), host))
+        self.assertEqual(rc, 1)
+        self.assertIn("browser", out)
+        self.assertFalse(self.config_path().exists())
+
+    def test_university_option_skips_the_question(self):
+        rc, out = self.run_terminal({}, university="stanford")
+        self.assertEqual(rc, 0, out)
+        self.assertNotIn(setup.UNIVERSITY_PROMPT, self.prompts)
+        self.assertEqual(config.load(self.config_path()).university, "stanford")
+        self.assertEqual(self.stored_totp, [])
+
+    def test_unknown_or_saml_university_option_is_refused(self):
+        for university in ("atlantis", "other", "fu-berlin"):
+            rc, out = self.run_terminal({}, university=university)
+            self.assertEqual(rc, 1, university)
+        self.assertFalse(self.config_path().exists())
+
+    def test_existing_config_keeps_its_university(self):
+        rc, _ = self.run_terminal({})
+        rc, out = self.run_terminal({}, university="bonn")
+        self.assertEqual(rc, 0, out)
+        self.assertIn('set university = "bonn"', out)
+        self.assertEqual(config.load(self.config_path()).university, "heidelberg")
+
+    def test_dry_run_asks_no_university(self):
+        rc, out = self.run_terminal({}, dry_run=True, user="ab123")
+        self.assertEqual(rc, 0, out)
+        self.assertNotIn(setup.UNIVERSITY_PROMPT, self.prompts)
+        self.assertIn("Heidelberg University", out)
+
+
 class UninstallTests(SetupHarness):
     def test_uninstall_removes_recorded_files(self):
         self.run_setup()
````

Apply to `tests/test_cli.py`:

````diff
diff --git a/tests/test_cli.py b/tests/test_cli.py
--- a/tests/test_cli.py
+++ b/tests/test_cli.py
@@ -97,6 +97,20 @@ class ApiTests(DaemonHarness):
             store.assert_not_called()
             self.assertTrue(out.getvalue().strip(), repr(text))
 
+    async def test_totp_command_refuses_when_the_university_uses_none(self):
+        path = Path(tempfile.mkdtemp()) / "config.toml"
+        path.write_text('university = "bonn"\nuser = "u"\n')
+        out = io.StringIO()
+        with mock.patch.object(cli.getpass, "getpass", side_effect=AssertionError("must not ask")), \
+             mock.patch.object(credentials, "store_totp") as store, redirect_stdout(out):
+            self.assertEqual(cli.main(["--config", str(path), "totp"]), 2)
+        store.assert_not_called()
+        self.assertIn("University of Bonn uses no TOTP secret", out.getvalue())
+
+    def test_setup_takes_a_university(self):
+        args = cli.build_parser().parse_args(["setup", "--university", "ethz", "--user", "jdoe"])
+        self.assertEqual((args.university, args.user), ("ethz", "jdoe"))
+
     async def test_password_empty_rejected(self):
         with mock.patch.object(cli.getpass, "getpass", return_value=""), redirect_stdout(io.StringIO()):
             self.assertEqual(cli.main(["--config", self.config_path(), "password"]), 2)
````

- [ ] **Step 2: Run them to make sure they fail**

Run: `python3 -m unittest tests.test_setup tests.test_cli -v`
Expected: FAIL, among others `AttributeError: module 'uni_vpn.setup' has no attribute 'UNIVERSITY_PROMPT'` and `TypeError: setup() got an unexpected keyword argument 'probe'`.

- [ ] **Step 3: Implement setup**

````diff
diff --git a/uni_vpn/setup.py b/uni_vpn/setup.py
--- a/uni_vpn/setup.py
+++ b/uni_vpn/setup.py
@@ -8,17 +8,19 @@ import shutil
 import subprocess
 import sys
 import time
+import urllib.parse
 import xml.etree.ElementTree as ET
 from pathlib import Path
 
-from . import config, credentials, doctor, service, sysproxy, totp
+from . import config, credentials, detect, doctor, service, sysproxy, totp
 from . import platform as pf
+from . import universities as unis
 from .tunnel import port_open as _port_open
 
 INSTALLED_FILES = "installed-files.txt"
-TOTP_HINT = """   Second factor: in the MFA portal https://mfa.uni-heidelberg.de (reachable only on the university network
-   or via VPN), set up another token under "Soft-Token (zeitbasiert)", click "Tokendetails einblenden" and copy
-   the text between secret= and &issuer=. The app on your phone stays as a second token."""
+UNIVERSITY_PROMPT = "Your university (name or part of it, Enter for Heidelberg, other if not listed): "
+MFA_CHOICES = {"none": "none", "totp_field": "one-time code in its own field",
+               "totp_append": "one-time code after the password", "duo_push": "Duo push"}
 FINAL_HINT = """
 Status page (state, connect/disconnect, domain list): http://127.0.0.1:{port}/
 Restart any open browser once so that it reads the proxy rule.
@@ -198,10 +200,70 @@ def install_launcher(port: int, dry: bool, created, run=subprocess.run) -> None:
     _say(f"App entry created: {path}")
 
 
+def totp_hint(cfg: config.Config) -> str:
+    portal = f" ({cfg.mfa_portal_url})" if cfg.mfa_portal_url else ""
+    return (f"   Second factor: in your university's MFA portal{portal}, add another time-based token (TOTP) for\n"
+            "   this computer and copy its secret, the otpauth:// line or the letters after secret=.\n"
+            "   The app on your phone stays as a second token.")
+
+
+def ask_other(input_fn, probe=detect.probe) -> tuple[str, dict] | None:
+    """"Not listed": the VPN address, then what the gateway's login form shows."""
+    try:
+        host, usergroup = detect.split_address(input_fn("VPN address (for example vpn.example.edu): "))
+    except unis.FieldError as exc:
+        print(f"   {exc}")
+        return None
+    result = probe(host, usergroup)
+    overrides = {"host": host, **({"usergroup": usergroup} if usergroup else {})}
+    if result.error:
+        print(f"   {result.error}")
+    if len(result.groups) > 1:
+        print("   Groups: " + ", ".join(result.groups))
+        group = input_fn(f"Group [{result.group}]: ").strip() or result.group
+        if group != result.group:
+            result = probe(host, usergroup, group)
+        overrides["authgroup"] = group
+    if result.saml:
+        print("   This university signs in through a browser (SAML), uni-vpn does not support that yet")
+        return None
+    guess = result.suggestion()["mfa"]
+    print("   Second factor: " + ", ".join(f"{key} ({text})" for key, text in MFA_CHOICES.items()))
+    mfa = input_fn(f"Second factor [{guess}]: ").strip() or guess
+    if mfa not in MFA_CHOICES:
+        print(f"   Unknown second factor {mfa!r}")
+        return None
+    overrides["mfa"] = mfa
+    return unis.OTHER_ID, overrides
+
+
+def choose_university(input_fn, probe=detect.probe) -> tuple[str, dict] | None:
+    """The assistant's university step for the terminal. (id, overrides), None if nothing fits."""
+    for _attempt in range(3):
+        text = input_fn(UNIVERSITY_PROMPT).strip()
+        if not text:
+            return unis.DEFAULT_ID, {}
+        if text.lower() in (unis.OTHER_ID, "not listed"):
+            return ask_other(input_fn, probe)
+        found = unis.search(text)
+        if len(found) == 1:
+            if found[0].mfa == "saml":
+                print(f"   {found[0].name} signs in through a browser (SAML), uni-vpn does not support that yet")
+                return None
+            _say(found[0].name)
+            return found[0].id, {}
+        if found:
+            print("   Which one? " + ", ".join(f"{p.name} ({p.id})" for p in found[:8]))
+        else:
+            print("   Not in the list. Type other to enter the VPN address")
+    print("No university chosen")
+    return None
+
+
 def setup(args, *, input_fn=input, getpass_fn=getpass.getpass, service_install=service.install,
           store=credentials.store_password, store_totp=credentials.store_totp,
           keyring_probe=doctor.keyring_state, proxy_install=sysproxy.install, run_doctor=True,
-          port_open=_port_open, open_url=None, has_desktop=None) -> int:
+          port_open=_port_open, open_url=None, has_desktop=None, probe=detect.probe) -> int:
     dry = bool(getattr(args, "dry_run", False))
     open_url = open_url or pf.open_url
     has_desktop = has_desktop or pf.has_desktop
@@ -228,6 +290,15 @@ def setup(args, *, input_fn=input, getpass_fn=getpass.getpass, service_install=s
 
     cfg_path = config.default_path()
     user = (getattr(args, "user", None) or "").strip()
+    university = (getattr(args, "university", None) or "").strip()
+    if university:
+        profile = unis.get(university)
+        if profile is None or university == unis.OTHER_ID:
+            print(f"Unknown university {university!r}, see uni_vpn/universities.json")
+            return 1
+        if profile.mfa == "saml":
+            print(f"{profile.name} signs in through a browser (SAML), uni-vpn does not support that yet")
+            return 1
     broken = None
     configured = cfg_path.exists()
     if configured:
@@ -239,7 +310,9 @@ def setup(args, *, input_fn=input, getpass_fn=getpass.getpass, service_install=s
             cfg = config.Config(**config.ports_from_broken(cfg_path))  # the ports the daemon uses
             print(f"Config {cfg_path} is invalid ({exc}), please fix it; continuing with defaults")
         else:
-            _say(f"Config found: {cfg_path} (university ID {cfg.user})")
+            _say(f"Config found: {cfg_path} ({cfg.university_name}, university ID {cfg.user})")
+            if university and university != cfg.university:
+                print(f"   Keeping {cfg.university_name}. To switch, set university = \"{university}\" in {cfg_path}")
         if user and broken is None and user != cfg.user:
             if not config.valid_user(user):
                 print(f"Not a university ID: {user!r}")
@@ -257,6 +330,14 @@ def setup(args, *, input_fn=input, getpass_fn=getpass.getpass, service_install=s
             return 1
         cfg = config.Config()
     else:
+        overrides: dict = {}
+        if not university and dry:
+            university = unis.DEFAULT_ID
+        elif not university:
+            chosen = choose_university(input_fn, probe)
+            if chosen is None:
+                return 1
+            university, overrides = chosen
         if not user:
             if dry and not sys.stdin.isatty():
                 user = "example"
@@ -269,10 +350,11 @@ def setup(args, *, input_fn=input, getpass_fn=getpass.getpass, service_install=s
             print(f"Not a university ID: {user!r}")
             return 1
         if dry:
-            _say(f"would create {cfg_path} with university ID {user}")
-            cfg = config.Config(user=user)
+            _say(f"would create {cfg_path} for {unis.get(university).name} with university ID {user}")
+            cfg = config.profile_config(university)
+            cfg.user = user
         else:
-            config.write_initial(cfg_path, user=user)
+            config.write_initial(cfg_path, user=user, university=university, overrides=overrides)
             created(cfg_path)
             cfg = config.load(cfg_path)
             _say(f"Config created: {cfg_path}")
@@ -328,8 +410,10 @@ def setup(args, *, input_fn=input, getpass_fn=getpass.getpass, service_install=s
 
     url = f"http://127.0.0.1:{cfg.http_port}/"
     if gui:
-        if user and not configured:
-            url += f"?user={user}"
+        given = {"user": user, "university": university} if not configured else {}
+        query = urllib.parse.urlencode({key: value for key, value in given.items() if value})
+        if query:
+            url += f"?{query}"
         # The service has started, but the daemon needs a moment until bind().
         if not wait_for_port(cfg.http_port, port_open=port_open, timeout=15):
             print(f"\nThe service did not open {url} within 15 seconds. See: uni-vpn log, uni-vpn doctor")
@@ -351,7 +435,8 @@ def setup(args, *, input_fn=input, getpass_fn=getpass.getpass, service_install=s
             return 0
 
     if dry:
-        _say("would ask for the university password and the TOTP secret and store both in the keyring")
+        _say("would ask for the university password" + (" and the TOTP secret" if cfg.needs_totp else "")
+             + " and store it in the keyring")
     else:
         state = keyring_probe(cfg.user)
         if state == "present":
@@ -363,11 +448,13 @@ def setup(args, *, input_fn=input, getpass_fn=getpass.getpass, service_install=s
                 _say("Password stored in the keyring")
             else:
                 print("   No password entered, set it later with: uni-vpn password")
-        state = keyring_probe(cfg.user, kind="totp")
-        if state == "present":
+        state = keyring_probe(cfg.user, kind="totp") if cfg.needs_totp else "not used"
+        if state == "not used":
+            pass
+        elif state == "present":
             _say("TOTP secret is already in the keyring")
         else:
-            print(TOTP_HINT)
+            print(totp_hint(cfg))
             text = getpass_fn(f"TOTP secret for {cfg.user} (otpauth URL or Base32, input stays hidden): ")
             if not text.strip():
                 print("   No secret entered, set it later with: uni-vpn totp")
````

- [ ] **Step 4: CLI**

````diff
diff --git a/uni_vpn/cli.py b/uni_vpn/cli.py
--- a/uni_vpn/cli.py
+++ b/uni_vpn/cli.py
@@ -127,6 +127,9 @@ def cmd_password(args) -> int:
 
 def cmd_totp(args) -> int:
     cfg = _load(args)
+    if not cfg.needs_totp:
+        print(f"{cfg.university_name} uses no TOTP secret (mfa = \"{cfg.mfa}\" in config.toml)")
+        return 2
     text = getpass.getpass(f"TOTP secret for {cfg.user} (otpauth URL or Base32, input stays hidden): ")
     if not text.strip():
         print("No secret entered")
@@ -283,6 +286,7 @@ def build_parser() -> argparse.ArgumentParser:
     p = sub.add_parser("setup", help="Set up (called by the installer)")
     p.add_argument("--dry-run", action="store_true")
     p.add_argument("--user", help="University ID")
+    p.add_argument("--university", help="University from uni_vpn/universities.json, for example heidelberg")
     p.add_argument("--no-gui", action="store_true", help="Ask in the terminal instead of opening the setup assistant")
     p.add_argument("--no-browser", action="store_true", help=argparse.SUPPRESS)
     p.set_defaults(func=cmd_setup)
````

- [ ] **Step 5: Installers**

````diff
diff --git a/install.sh b/install.sh
--- a/install.sh
+++ b/install.sh
@@ -1,6 +1,6 @@
 #!/usr/bin/env bash
 # Install uni-vpn on Linux or macOS: fetch packages, then run "uni-vpn setup".
-# Usage: ./install.sh [--uninstall | --update] [--dry-run] [--no-gui] [--user UNIVERSITY-ID]
+# Usage: ./install.sh [--uninstall | --update] [--dry-run] [--no-gui] [--user UNIVERSITY-ID] [--university ID]
 set -euo pipefail
 cd "$(dirname "${BASH_SOURCE[0]}")"
 
@@ -16,12 +16,15 @@ while [ $# -gt 0 ]; do
     --user)
       [ $# -ge 2 ] || { echo "--user needs a university ID"; exit 2; }
       extra+=(--user "$2"); shift ;;
+    --university)
+      [ $# -ge 2 ] || { echo "--university needs an id from uni_vpn/universities.json"; exit 2; }
+      extra+=(--university "$2"); shift ;;
     -h|--help) sed -n '2,3p' "$0"; exit 0 ;;
     *) echo "Unknown option: $1"; exit 2 ;;
   esac
   shift
 done
-# --no-gui and --user only mean something to "setup".
+# --no-gui, --user and --university only mean something to "setup".
 if [ "$mode" != setup ]; then
   filtered=()
   for a in "${extra[@]:-}"; do [ "$a" = "--dry-run" ] && filtered+=("$a"); done
````

````diff
diff --git a/install.ps1 b/install.ps1
--- a/install.ps1
+++ b/install.ps1
@@ -1,5 +1,5 @@
 # Install uni-vpn on Windows: Python and OpenConnect (with Wintun) if missing, then "uni-vpn setup".
-# Usage: powershell -ExecutionPolicy Bypass -File install.ps1 [-Uninstall | -Update] [-DryRun] [-NoGui] [-User UNIVERSITY-ID]
+# Usage: powershell -ExecutionPolicy Bypass -File install.ps1 [-Uninstall | -Update] [-DryRun] [-NoGui] [-User UNIVERSITY-ID] [-University ID]
 # Needs an administrator account: Wintun, the virtual network adapter openconnect uses on
 # Windows, can only be created with administrator rights. The program goes to
 # %ProgramFiles%\uni-vpn: the service runs it elevated, so only administrators may change it.
@@ -11,6 +11,7 @@ param(
     [switch]$NoGui,
     [switch]$NoBrowser,
     [string]$User = "",
+    [string]$University = "",
     [string]$ForUser = ""
 )
 $ErrorActionPreference = "Stop"
@@ -126,6 +127,7 @@ if (-not $DryRun -and -not (Test-Admin)) {
     # The browser must not run elevated: this window opens it once the elevated part is done.
     $arguments += "-NoBrowser"
     if ($User) { $arguments += @("-User", "`"$User`"") }
+    if ($University) { $arguments += @("-University", "`"$University`"") }
     Say "asking for administrator rights"
     try {
         $process = Start-Process -FilePath "powershell.exe" -ArgumentList $arguments -Verb RunAs -Wait -PassThru
@@ -141,8 +143,11 @@ if (-not $DryRun -and -not (Test-Admin)) {
             if ($match) { $port = [int]$match.Matches[0].Groups[1].Value }
         }
         $url = "http://127.0.0.1:$port/"
-        # A given ID is prefilled in the assistant, which writes the config.
-        if ($User -and -not (Test-Path $config)) { $url += "?user=$([uri]::EscapeDataString($User))" }
+        # A given ID and university are prefilled in the assistant, which writes the config.
+        $query = @()
+        if ($User) { $query += "user=$([uri]::EscapeDataString($User))" }
+        if ($University) { $query += "university=$([uri]::EscapeDataString($University))" }
+        if ($query.Count -gt 0 -and -not (Test-Path $config)) { $url += "?" + ($query -join "&") }
         Start-Process $url
     }
     exit $process.ExitCode
@@ -197,6 +202,7 @@ try {
         if ($NoGui) { $cliArgs += "--no-gui" }
         if ($NoBrowser) { $cliArgs += "--no-browser" }
         if ($User) { $cliArgs += @("--user", $User) }
+        if ($University) { $cliArgs += @("--university", $University) }
     }
     & $python @cliArgs
     $code = $LASTEXITCODE
````

- [ ] **Step 6: Run the tests and the installer checks**

Run: `python3 -m unittest tests.test_setup tests.test_cli -v`
Expected: PASS.

Run: `bash -n install.sh get.sh && ./install.sh --dry-run --user citest && ./install.sh --dry-run --user citest --university bonn`
Expected: both dry runs end with the status page hint; the second says `would create ... for University of Bonn` and `would ask for the university password and store it in the keyring` (no TOTP).

On Windows (or in CI): `powershell -NoProfile -ExecutionPolicy Bypass -File install.ps1 -DryRun -User citest -University bonn`.

- [ ] **Step 7: Run the whole suite**

Run: `python3 -m unittest discover -s tests -t .`
Expected: `OK (skipped=17)`.

- [ ] **Step 8: Commit**

```bash
git add uni_vpn/setup.py uni_vpn/cli.py install.sh install.ps1 tests/test_setup.py tests/test_cli.py
git commit -m "setup: ask for the university in the terminal, --university for installers"
```

---

### Task 9: Doctor follows the profile

**Files:**
- Modify: `uni_vpn/doctor.py`
- Test: `tests/test_doctor.py`

**Interfaces:**
- Consumes: `Config.mfa`, `Config.needs_totp`, `Config.no_external_auth`, `Config.university_name`.
- Produces: `doctor.NO_EXTERNAL_AUTH_SINCE = (9, 10)`, `doctor.version_tuple(text) -> tuple[int, int] | None`; the "Second factor" check is `fail` for SAML, `ok` without a keyring probe for `none` and `duo_push`; the "openconnect" check is `warn` when the profile wants `--no-external-auth` and openconnect is older than 9.10 (Debian 12 ships 9.01); the "Config" detail names the university.

- [ ] **Step 1: Write the failing tests**

Apply to `tests/test_doctor.py`:

````diff
diff --git a/tests/test_doctor.py b/tests/test_doctor.py
--- a/tests/test_doctor.py
+++ b/tests/test_doctor.py
@@ -177,6 +177,43 @@ class DoctorTests(unittest.TestCase):
         self.assertEqual(self.by_name(checks, "Second factor").status, "fail")
         self.assertIn("uni-vpn totp", self.by_name(checks, "Second factor").detail)
 
+    def test_second_factor_follows_the_university(self):
+        kinds = []
+
+        def probe(user, kind="password"):
+            kinds.append(kind)
+            return "missing" if kind == "totp" else "present"
+
+        checks = doctor.run_checks(write_config('university = "bonn"\nuser = "ab1"\n'), **self.probes(keyring_probe=probe))
+        self.assertEqual(self.by_name(checks, "Second factor").status, "ok")
+        self.assertNotIn("totp", kinds)
+        checks = doctor.run_checks(write_config('university = "stanford"\nuser = "ab1"\n'), **self.probes(keyring_probe=probe))
+        self.assertIn("Duo", self.by_name(checks, "Second factor").detail)
+        checks = doctor.run_checks(write_config('university = "oxford"\nuser = "ab1"\n'), **self.probes())
+        self.assertEqual(self.by_name(checks, "Second factor").status, "fail")
+        self.assertIn("SAML", self.by_name(checks, "Second factor").detail)
+
+    def test_config_names_the_university(self):
+        checks = doctor.run_checks(write_config('university = "ethz"\nuser = "ab1"\n'), **self.probes())
+        self.assertIn("ETH Zurich", self.by_name(checks, "Config").detail)
+
+    def test_old_openconnect_warns_only_when_the_profile_needs_no_external_auth(self):
+        def run(cmd, **kwargs):
+            if cmd == ["/usr/bin/openconnect", "--version"]:
+                return subprocess.CompletedProcess(cmd, 0, "OpenConnect version v9.01-3\n", "")
+            return subprocess.CompletedProcess(cmd, 1, "", "")
+
+        checks = doctor.run_checks(write_config('university = "bonn"\nuser = "ab1"\n'), **self.probes(run=run))
+        self.assertEqual(self.by_name(checks, "openconnect").status, "warn")
+        self.assertIn("9.10", self.by_name(checks, "openconnect").detail)
+        checks = doctor.run_checks(write_config(), **self.probes(run=run))
+        self.assertEqual(self.by_name(checks, "openconnect").status, "ok", "Heidelberg runs without the flag")
+
+    def test_version_tuple(self):
+        self.assertEqual(doctor.version_tuple("OpenConnect version v9.12-1"), (9, 12))
+        self.assertEqual(doctor.version_tuple("OpenConnect version v10.0"), (10, 0))
+        self.assertIsNone(doctor.version_tuple(""))
+
     def test_keyring_state_probes_requested_kind(self):
         seen = []
 
````

- [ ] **Step 2: Run them to make sure they fail**

Run: `python3 -m unittest tests.test_doctor -v`
Expected: FAIL, among others `AttributeError: module 'uni_vpn.doctor' has no attribute 'version_tuple'`.

- [ ] **Step 3: Implement**

````diff
diff --git a/uni_vpn/doctor.py b/uni_vpn/doctor.py
--- a/uni_vpn/doctor.py
+++ b/uni_vpn/doctor.py
@@ -4,6 +4,7 @@ from __future__ import annotations
 
 import asyncio
 import os
+import re
 import subprocess
 import sys
 from dataclasses import dataclass
@@ -52,6 +53,15 @@ def openconnect_version(path: str, run=subprocess.run) -> str:
     return _first_line(result.stdout or "") or _first_line(result.stderr or "")
 
 
+NO_EXTERNAL_AUTH_SINCE = (9, 10)  # openconnect release that added --no-external-auth
+
+
+def version_tuple(text: str) -> tuple[int, int] | None:
+    """(9, 12) from "OpenConnect version v9.12-1"; None if there is no version in it."""
+    match = re.search(r"v?(\d+)\.(\d+)", text)
+    return (int(match.group(1)), int(match.group(2))) if match else None
+
+
 def port_owner(port: int, run=subprocess.run) -> str:
     """Process line from ss (Linux) or lsof (macOS) for a port in use, shortened. Empty if unknown."""
     if pf.IS_WINDOWS:
@@ -94,7 +104,7 @@ def run_checks(cfg_path: Path | None = None, *,
     cfg = config.Config()
     try:
         cfg = config.load(cfg_path)
-        checks.append(Check("Config", "ok", f"{cfg.path}, university ID {cfg.user}, host {cfg.host}"))
+        checks.append(Check("Config", "ok", f"{cfg.path}, {cfg.university_name}, university ID {cfg.user}, host {cfg.host}"))
     except config.ConfigError as exc:
         checks.append(Check("Config", "fail", str(exc)))
 
@@ -102,10 +112,16 @@ def run_checks(cfg_path: Path | None = None, *,
     for name, override in programs:
         found = find_binary(name, override)
         detail = found or "not found, run install.sh"
+        status = "ok" if found else "fail"
         if found and name == "openconnect":
             version = openconnect_version(found, run)
             detail = f"{found}, {version}" if version else found
-        checks.append(Check(name, "ok" if found else "fail", detail))
+            parsed = version_tuple(version)
+            if cfg.no_external_auth and parsed and parsed < NO_EXTERNAL_AUTH_SINCE:
+                status = "warn"
+                detail += (", too old for --no-external-auth (needs 9.10): update openconnect,"
+                           " or set no_external_auth = false in config.toml")
+        checks.append(Check(name, status, detail))
     if pf.IS_WINDOWS:
         found = find_binary("openconnect", cfg.openconnect)
         wintun = bool(found) and os.path.isfile(os.path.join(os.path.dirname(found), "wintun.dll"))
@@ -155,12 +171,18 @@ def run_checks(cfg_path: Path | None = None, *,
                    "locked": ("warn", "keyring locked or not responding")}
         status, detail = mapping.get(state, ("fail", state.replace("error:", "error: ")))
         checks.append(Check("Keyring", status, detail))
-        state = keyring_probe(cfg.user, kind="totp")
-        mapping = {"present": ("ok", "TOTP secret stored"),
-                   "missing": ("fail", "no TOTP secret stored: uni-vpn totp"),
-                   "locked": ("warn", "keyring locked or not responding")}
-        status, detail = mapping.get(state, ("fail", state.replace("error:", "error: ")))
-        checks.append(Check("Second factor", status, detail))
+        if cfg.mfa == "saml":
+            checks.append(Check("Second factor", "fail", "browser sign-in (SAML) is not supported yet"))
+        elif cfg.needs_totp:
+            state = keyring_probe(cfg.user, kind="totp")
+            mapping = {"present": ("ok", "TOTP secret stored"),
+                       "missing": ("fail", "no TOTP secret stored: uni-vpn totp"),
+                       "locked": ("warn", "keyring locked or not responding")}
+            status, detail = mapping.get(state, ("fail", state.replace("error:", "error: ")))
+            checks.append(Check("Second factor", status, detail))
+        else:
+            detail = {"none": "none", "duo_push": "Duo push, confirm it on the phone"}[cfg.mfa]
+            checks.append(Check("Second factor", "ok", detail))
 
     url = sysproxy.pac_url(cfg.http_port)
     proxy = proxy_state(cfg.http_port)
````

- [ ] **Step 4: Run the tests**

Run: `python3 -m unittest tests.test_doctor -v`
Expected: PASS (24 tests).

- [ ] **Step 5: Run the whole suite**

Run: `python3 -m unittest discover -s tests -t .`
Expected: `OK (skipped=17)`.

- [ ] **Step 6: Commit**

```bash
git add uni_vpn/doctor.py tests/test_doctor.py
git commit -m "doctor: second factor and openconnect version by university"
```

---

### Task 10: README and final verification

**Files:**
- Modify: `README.md`
- Test: `tests/test_universities.py`

**Interfaces:**
- Consumes: the registry; every profile must appear in the README table as `| <name> | <host> |`.

- [ ] **Step 1: Write the failing test**

Apply to `tests/test_universities.py`:

````diff
diff --git a/tests/test_universities.py b/tests/test_universities.py
--- a/tests/test_universities.py
+++ b/tests/test_universities.py
@@ -1,6 +1,7 @@
 import copy
 import json
 import unittest
+from pathlib import Path
 
 from uni_vpn import universities as unis
 
@@ -70,6 +71,13 @@ class RegistryTests(unittest.TestCase):
         self.assertEqual(unis.search("   "), [])
 
 
+class ReadmeTests(unittest.TestCase):
+    def test_readme_lists_every_university(self):
+        readme = (Path(__file__).parent.parent / "README.md").read_text(encoding="utf-8")
+        for profile in unis.registry().values():
+            self.assertIn(f"| {profile.name} | {profile.host} |", readme, profile.id)
+
+
 class ParseTests(unittest.TestCase):
     def assert_refused(self, data, text):
         with self.assertRaises(ValueError) as cm:
````

- [ ] **Step 2: Run it to make sure it fails**

Run: `python3 -m unittest tests.test_universities -v`
Expected: FAIL with `'| Heidelberg University | vpn-ac.uni-heidelberg.de |' not found`.

- [ ] **Step 3: Generalize the README**

````diff
diff --git a/README.md b/README.md
--- a/README.md
+++ b/README.md
@@ -1,9 +1,43 @@
 # uni-vpn
 
-Heidelberg University's VPN (Cisco AnyConnect) only for selected websites in the browser.
-The tunnel comes up on the first request to a listed domain, password and TOTP secret live
-in the operating system's keyring, and after 15 minutes without traffic the tunnel is torn
-down again. Everything else on the machine keeps its normal connection.
+Your university's Cisco AnyConnect VPN only for selected websites in the browser. Built at
+Heidelberg University, with profiles for other universities. The tunnel comes up on the first
+request to a listed domain, password and second factor live in the operating system's
+keyring, and after 15 minutes without traffic the tunnel is torn down again. Everything else
+on the machine keeps its normal connection.
+
+## Universities
+
+| University | Gateway | Second factor | State |
+|---|---|---|---|
+| Heidelberg University | vpn-ac.uni-heidelberg.de | TOTP | tested end to end |
+| ETH Zurich | sslvpn.ethz.ch | TOTP | profile only |
+| University of Bremen | vpn.uni-bremen.de | TOTP | profile only |
+| University of Muenster | vpn.uni-muenster.de | TOTP | profile only |
+| Harvard FAS Research Computing | vpn.rc.fas.harvard.edu | TOTP | profile only |
+| University of Marburg | vpn.uni-marburg.de | TOTP after the password | profile only |
+| Stanford University | su-vpn.stanford.edu | Duo push | profile only |
+| University of Stuttgart | vpn.tik.uni-stuttgart.de | none | profile only |
+| University of Bonn | unibn-vpn.uni-bonn.de | none | profile only |
+| University of Mannheim | vpn.uni-mannheim.de | none | profile only |
+| University of Kassel | univpn.uni-kassel.de | none | profile only |
+| TU Dresden | vpn2.zih.tu-dresden.de | none | profile only |
+| Freie Universitaet Berlin | vpn.fu-berlin.de | browser sign-in (SAML) | not supported yet |
+| University of Oxford | vpn.ox.ac.uk | browser sign-in (SAML) | not supported yet |
+
+"Profile only" means the values come from the university's documentation and a look at its
+login form, but nobody has logged in with uni-vpn yet. If it works for you, or if your
+university is missing, open an issue or a pull request against `uni_vpn/universities.json`.
+Not listed: choose "Not listed" in the setup assistant and enter the VPN address; uni-vpn reads
+the gateway's login form and fills in what it can. Every value can be changed in
+`config.toml`, for example:
+
+```toml
+university = "ethz"
+user = "jdoe"
+authgroup = "student-net"
+username_suffix = "@student-net.ethz.ch"
+```
 
 ## Platforms
 
@@ -56,20 +90,28 @@ What the installer adds when missing:
 - Windows: Python 3.12 (winget, or the signed python.org installer) and OpenConnect with
   Wintun (the OpenConnect-GUI 1.6.2 installer, checked against its SHA-256).
 
-The assistant then asks for three things, one per step: university ID and password, the TOTP
-secret (see below, with a live check code), done. Restart open browsers once afterwards.
+The assistant then asks one thing per step: your university, user name and password, the TOTP
+secret if your university uses one (see below, with a live check code), done. Restart open browsers once afterwards.
 Without a desktop (SSH), or with `--no-gui` (`-NoGui` on Windows), the same questions come
 in the terminal.
 
-`https://sogo.uni-heidelberg.de` and `https://elearning-med.uni-heidelberg.de` now go
-through the university, along with `cip.dmed.uni-heidelberg.de`, which elearning-med embeds
-for its statistics. Everything else does not. Further domains go under Settings, Websites.
+At Heidelberg, `https://sogo.uni-heidelberg.de` and `https://elearning-med.uni-heidelberg.de`
+now go through the university, along with `cip.dmed.uni-heidelberg.de`, which elearning-med
+embeds for its statistics. Everything else does not. For other universities the list starts
+empty. Domains go under Settings, Websites.
+
+Without a desktop, `--university ID` (`-University ID` on Windows) picks the university, for
+example `./install.sh --university ethz`.
 
 ### Second factor
 
-The URZ requires a time-based one-time password (TOTP) for VPN login. So that uni-vpn can
-connect without asking, the machine gets a token of its own, exactly as the URZ describes
-for KeePassXC. The app on your phone stays as it is.
+Universities with a time-based one-time password (TOTP): so that uni-vpn can connect without
+asking, the machine gets a token of its own, like a second phone. Add one in your
+university's MFA portal and paste its secret (the `otpauth://` line) into the assistant. With
+Duo push, confirm the request on the phone; without a second factor there is nothing to do.
+
+At Heidelberg the URZ requires TOTP for VPN login and describes the same for KeePassXC. The
+app on your phone stays as it is.
 
 1. On the university network, or with the Cisco client connected, open
    https://mfa.uni-heidelberg.de
@@ -111,6 +153,8 @@ The app is `http://127.0.0.1:1081/`, also in the start menu, Launchpad or app gr
 | App: login rejected | password wrong or expired | `uni-vpn password`; if it persists, check `uni-vpn log` and open an issue |
 | App: one-time code rejected | the machine's clock is off, or the TOTP secret is wrong | turn on automatic time; otherwise create a new token in the MFA portal and run `uni-vpn totp` |
 | App: no TOTP secret stored | second factor not set up yet | `uni-vpn totp`, see "Second factor" |
+| App: this university signs in through a browser (SAML) | the university uses single sign-on in a browser | not supported yet, use the Cisco client |
+| App: login rejected with Duo | the push was denied or not answered in time | connect again and confirm the push |
 | App: waiting for the next one-time code | within 30 s of the last login the same code cannot be reused | nothing to do, it continues by itself |
 | App: Cisco Secure Client is connected | the Cisco client is active | disconnect Cisco, uni-vpn then connects on its own |
 | App: keyring locked | the keyring was not unlocked after autologin | log out and log in with your password |
@@ -139,7 +183,8 @@ The app is `http://127.0.0.1:1081/`, also in the start menu, Launchpad or app gr
   `%ProgramFiles%\uni-vpn` and runs only Python and openconnect from Program Files, so nothing
   the user account can change runs elevated. `uni-vpn update` asks for administrator rights.
 - Password and TOTP secret live in the GNOME keyring, the macOS keychain or the Windows
-  Credential Manager, nowhere else. While connecting, openconnect reads the secret from a
+  Credential Manager, nowhere else. Setting up a university that is not listed asks its
+  gateway for the login form once, without any user data. While connecting, openconnect reads the secret from a
   file readable only by the user, which is deleted immediately afterwards. The machine is
   therefore the second factor, the same way the KeePassXC token documented by the URZ is.
 - macOS lists the service under System Settings, General, Login Items.
@@ -156,7 +201,8 @@ The app is `http://127.0.0.1:1081/`, also in the start menu, Launchpad or app gr
 ## Development
 
 `python3 -m unittest discover -s tests -t . -v` runs without a real VPN, against a fake
-openconnect, on Linux, macOS and Windows. Design: `docs/superpowers/specs/2026-09-07-uni-vpn-design.md`. End-to-end tests:
+openconnect, on Linux, macOS and Windows. Design: `docs/superpowers/specs/2026-09-07-uni-vpn-design.md`,
+other universities: `docs/superpowers/specs/2026-10-05-multi-university-design.md`. End-to-end tests:
 `docs/e2e.md` (Linux), `docs/macos-test.md`, `docs/windows-test.md`.
 
 ## License
````

- [ ] **Step 4: Run the tests**

Run: `python3 -m unittest tests.test_universities -v`
Expected: PASS (14 tests).

- [ ] **Step 5: Full CI equivalent**

```bash
python3 -m compileall -q uni_vpn bin/uni-vpn tests
python3 -m unittest discover -s tests -t . -v
bash -n install.sh get.sh
./install.sh --dry-run --user citest
grep -rnP '\x{2014}' uni_vpn tests README.md docs/superpowers/specs/2026-10-05-multi-university-design.md install.sh install.ps1 || echo "no em-dashes"
```

Expected: `Ran 442 tests ... OK (skipped=17)` on Linux, the dry run ends with the status page hint, and `no em-dashes`.

- [ ] **Step 6: Heidelberg regression on a real install (manual)**

On a machine with a working Heidelberg install (config.toml without `university`): update to this branch, `uni-vpn service restart`, open a listed site (`https://sogo.uni-heidelberg.de`). Expected: connects as before; `uni-vpn log` shows the same openconnect arguments as before (no `--no-external-auth`, no `--authgroup`); `uni-vpn doctor` shows `Config: ..., Heidelberg University, ...` and `Second factor: TOTP secret stored`.

- [ ] **Step 7: Commit**

```bash
git add README.md tests/test_universities.py
git commit -m "readme: other universities and how to add one"
```

---

## Self-review against the spec

| Spec section | Task |
|---|---|
| 3 Profile schema, registry, initial entries, only Heidelberg verified | 1 |
| 4 `university` key, overrides, Heidelberg default, `write_initial`, `set_values`, realm in user names | 2 |
| 5 command per profile, stdin per mode, `--non-inter` for Duo, OTP wait only with TOTP | 3, 4 |
| 6 generic, mode-aware error texts | 3 |
| 7 detection with fixtures, redirect re-post, SAML markers | 5 |
| 8 daemon (TOTP optional, SAML refusal, domains, status), API, doctor, CLI, terminal setup | 4, 6, 8, 9 |
| 9 setup assistant picker, detection, adaptive steps, portal link and steps from the profile | 7 |
| README table of supported universities | 10 |

Open points carried over from the spec, not implemented on purpose: real tests of `duo_push` and `totp_append` against Stanford and Marburg, Bremen users without 2FA (empty second field), Kassel's Windows user agent, SAML.
