"""University profiles (universities.json): gateway, groups, client quirks, second factor."""

from __future__ import annotations

import functools
import json
import re
import unicodedata
from dataclasses import dataclass, fields
from pathlib import Path

from .i18n import t

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
            raise FieldError(name, t("address.invalid"))
    elif name == "usergroup":
        value = value.strip().strip("/")
        if not USERGROUP_RE.fullmatch(value):
            raise FieldError(name, t("address.path"))
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
