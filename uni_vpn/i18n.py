"""Translations: one flat JSON catalog per language in locales/, English (en.json) is the source.

The service keeps speaking English (log, command line). Messages the app shows are Text values:
plain English strings that also remember their catalog key and arguments, so status.json and
the API can hand both to the app, which shows them in the user's language.
"""

from __future__ import annotations

import json
from functools import cache
from pathlib import Path

LOCALES_DIR = Path(__file__).resolve().parent / "locales"
# Code, native name. Every language spoken at a university in universities.json; Chinese in
# both scripts (Hong Kong writes Traditional, most students from the mainland read Simplified).
LANGUAGES = (
    ("en", "English"),
    ("de", "Deutsch"),
    ("fr", "Français"),
    ("es", "Español"),
    ("zh-Hans", "简体中文"),
    ("zh-Hant", "繁體中文"),
)
CODES = tuple(code for code, _name in LANGUAGES)
SOURCE = "en"


@cache
def catalog(code: str) -> dict[str, str]:
    return json.loads((LOCALES_DIR / f"{code}.json").read_text(encoding="utf-8"))


def catalogs() -> dict:
    """Everything the app needs to switch languages: the choices and all catalogs."""
    return {"languages": [list(entry) for entry in LANGUAGES], "catalogs": {code: catalog(code) for code in CODES}}


class Text(str):
    """English text that remembers its catalog key and arguments."""

    key: str
    args: dict

    def __new__(cls, key: str, **args) -> Text:
        self = super().__new__(cls, catalog(SOURCE)[key].format_map({k: _plain(v) for k, v in args.items()}))
        self.key = key
        self.args = args
        return self

    def __reduce__(self):
        # copy and dataclasses.asdict would otherwise rebuild it from the English text.
        return _rebuild, (self.key, self.args)


def _rebuild(key: str, args: dict) -> Text:
    return Text(key, **args)


def _plain(value) -> str:
    """An argument in English: lists (several errors) one per line."""
    if isinstance(value, (list, tuple)):
        return "\n".join(map(str, value))
    return str(value)


def t(key: str, **args) -> Text:
    return Text(key, **args)


def as_json(value):
    """A message for status.json and the API: {"key", "args"} for Text, None for plain text."""
    if not isinstance(value, Text):
        return None
    return {"key": value.key, "args": {k: _arg(v) for k, v in value.args.items()}}


def _arg(value):
    if isinstance(value, (list, tuple)):
        return [_arg(v) for v in value]
    if isinstance(value, (int, float)) and not isinstance(value, bool):
        return value
    return as_json(value) or str(value)


def of(exc: BaseException) -> str:
    """The message of an exception, still a Text when it was raised with one."""
    if len(exc.args) == 1 and isinstance(exc.args[0], Text):
        return exc.args[0]
    return str(exc)


def valid(code: str) -> bool:
    """A language setting: "" follows the system, otherwise one of CODES."""
    return code == "" or code in CODES
