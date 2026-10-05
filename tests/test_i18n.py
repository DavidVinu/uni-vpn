import copy
import json
import re
import tempfile
import unittest
from dataclasses import asdict
from pathlib import Path

from uni_vpn import config, detect, i18n, tunnel
from uni_vpn import universities as unis

from tests.test_daemon import DaemonHarness
from tests.test_httpapi import http

UI = (Path(i18n.__file__).parent / "ui" / "index.html").read_text(encoding="utf-8")
PLACEHOLDER = re.compile(r"\{(\w+)\}")
HEADERS = {"X-Uni-VPN": "1", "Content-Type": "application/json"}


def placeholders(text: str) -> set[str]:
    return set(PLACEHOLDER.findall(text))


class CatalogTests(unittest.TestCase):
    def test_every_language_has_every_key_with_the_same_placeholders(self):
        source = i18n.catalog("en")
        for code in i18n.CODES:
            with self.subTest(code=code):
                cat = i18n.catalog(code)
                own = {k for k in cat if not k.startswith("uni.")}
                self.assertEqual(own, set(source))
                for key, text in source.items():
                    self.assertEqual(placeholders(cat[key]), placeholders(text), key)
                    self.assertTrue(cat[key].strip(), key)

    def test_the_app_never_asks_for_a_command(self):
        # Anyone must manage with clicks alone; commands are for the terminal (en-cli.json).
        command = re.compile(r"uni-vpn (service|doctor|password|totp|log|setup|update|status)\b|\b(run|Run):")
        for code in i18n.CODES:
            for key, text in i18n.catalog(code).items():
                self.assertIsNone(command.search(text), (code, key))
        for key, text in i18n.terminal().items():
            self.assertIn(key, i18n.catalog("en"))
            self.assertEqual(placeholders(text), placeholders(i18n.catalog("en")[key]), key)

    def test_university_steps_match_the_list(self):
        for code in i18n.CODES:
            for key, text in i18n.catalog(code).items():
                if not key.startswith("uni."):
                    continue
                with self.subTest(code=code, key=key):
                    _, uid, kind, number = key.split(".")
                    self.assertEqual(kind, "mfa")
                    steps = unis.get(uid).mfa_steps
                    self.assertLessEqual(int(number), len(steps))
                    self.assertEqual(placeholders(text), placeholders(steps[int(number) - 1]))

    def test_translated_universities_have_all_their_steps(self):
        for code in i18n.CODES[1:]:
            cat = i18n.catalog(code)
            for profile in unis.registry().values():
                keys = [f"uni.{profile.id}.mfa.{n}" for n in range(1, len(profile.mfa_steps) + 1)]
                present = [key in cat for key in keys]
                self.assertIn(set(present) or {True}, ({True}, {False}), (code, profile.id))

    def test_every_key_the_app_uses_exists(self):
        source = i18n.catalog("en")
        used = set(re.findall(r'data-t(?:-placeholder|-label)?="([^"]+)"', UI))
        used |= set(re.findall(r'\bt\("([a-z_.]+[a-z])"', UI))
        used |= {"look." + state for state in re.findall(r"(\w+):", re.search(r"const LOOK = \{([^}]*)\}", UI).group(1))}
        used |= {f"setup.generic_step.{n}" for n in (1, 2, 3)}
        used |= {"settings.domain_count.one", "settings.domain_count.other"}
        self.assertEqual(used - set(source), set())
        self.assertGreater(len(used), 60)

    def test_languages_are_named_in_their_own_language(self):
        self.assertEqual(dict(i18n.LANGUAGES)["de"], "Deutsch")
        self.assertEqual(i18n.CODES[0], "en")
        self.assertEqual(set(i18n.catalogs()["catalogs"]), set(i18n.CODES))


class TextTests(unittest.TestCase):
    def test_is_the_english_text(self):
        text = i18n.t("state.port_in_use", port=1080)
        self.assertEqual(text, "Port 1080 is in use, run uni-vpn doctor")
        self.assertIsInstance(text, str)
        self.assertEqual(i18n.as_json(text), {"key": "state.port_in_use", "args": {"port": 1080}})
        self.assertIsNone(i18n.as_json("plain"))

    def test_nested_and_lists(self):
        text = i18n.t("state.not_connected_because", reason=tunnel.PASSWORD_REJECTED)
        self.assertEqual(text, "Not connected (Login rejected: check your password (uni-vpn password))")
        self.assertEqual(i18n.as_json(text)["args"]["reason"], {"key": "tunnel.password_rejected", "args": {}})
        lines = i18n.t("app.lines", lines=[i18n.t("domains.not_hostname", line=2, text="a b"), "plain"])
        self.assertEqual(lines, "line 2: 'a b' is not a hostname\nplain")
        self.assertEqual(i18n.as_json(lines)["args"]["lines"][1], "plain")

    def test_survives_copies_and_exceptions(self):
        text = i18n.t("totp.algorithm", algorithm="MD5")
        self.assertEqual(i18n.as_json(copy.deepcopy(text)), i18n.as_json(text))
        self.assertEqual(i18n.of(ValueError(text)).key, "totp.algorithm")
        self.assertEqual(i18n.of(ValueError("x")), "x")
        result = detect.Detection(host="h", error=detect.NOT_CISCO)
        self.assertEqual(result.as_dict()["error_t"], {"key": "detect.not_cisco", "args": {}})
        self.assertEqual(asdict(result)["error"], detect.NOT_CISCO)


class ConfigTests(unittest.TestCase):
    def test_language_key(self):
        path = Path(tempfile.mkdtemp()) / "config.toml"
        config.write_initial(path, "u")
        self.assertEqual(config.load(path).language, "")
        config.set_values(path, {"language": "zh-Hant"})
        self.assertEqual(config.load(path).language, "zh-Hant")
        config.set_values(path, {"language": "xx"})
        with self.assertRaises(config.ConfigError):
            config.load(path)


class ApiTests(DaemonHarness):
    async def test_locales_and_message_keys(self):
        await self.start_daemon()
        status, _, payload = await http(self.cfg.http_port, "GET", "/locales.json")
        self.assertEqual(status, 200)
        data = json.loads(payload)
        self.assertEqual(data["languages"][0], ["en", "English"])
        self.assertEqual(data["catalogs"]["fr"]["settings.language"], "Langue")
        state = json.loads((await http(self.cfg.http_port, "GET", "/status.json"))[2])
        self.assertEqual(state["language"], "")
        self.assertEqual(state["message_t"]["key"], "state.not_connected")

    async def test_language_setting(self):
        path = Path(tempfile.mkdtemp()) / "config.toml"
        config.write_initial(path, "u")
        d = await self.start_daemon(config_path=path)
        status, _, _ = await http(self.cfg.http_port, "POST", "/api/language", HEADERS, b'{"language": "de"}')
        self.assertEqual(status, 200)
        self.assertEqual(config.load(path).language, "de")
        self.assertEqual(d.status()["language"], "de")
        status, _, _ = await http(self.cfg.http_port, "POST", "/api/language", HEADERS, b'{"language": "xx"}')
        self.assertEqual(status, 400)
        status, _, _ = await http(self.cfg.http_port, "POST", "/api/language", HEADERS, b'{"language": ""}')
        self.assertEqual(status, 200)
        self.assertEqual(config.load(path).language, "")

    async def test_language_needs_a_config(self):
        await self.start_daemon(config_path=Path(tempfile.mkdtemp()) / "none.toml")
        status, _, payload = await http(self.cfg.http_port, "POST", "/api/language", HEADERS, b'{"language": "de"}')
        self.assertEqual(status, 409)
        self.assertEqual(json.loads(payload)["error_t"]["key"], "setup.finish_first")

    async def test_setup_errors_carry_keys(self):
        await self.start_daemon()
        body = json.dumps({"user": "u", "password": "pw", "secret": "nope"}).encode()
        status, _, payload = await http(self.cfg.http_port, "POST", "/api/setup", HEADERS, body)
        self.assertEqual(status, 400)
        answer = json.loads(payload)
        self.assertEqual(answer["field"], "totp")
        self.assertTrue(answer["error_t"]["key"].startswith("totp."))
