"""Read and write config.toml."""

from __future__ import annotations

import json
import re
import tomllib
from dataclasses import dataclass, field
from pathlib import Path

from . import universities as unis

DEFAULT_HOST = "vpn-ac.uni-heidelberg.de"
DEFAULT_USERAGENT = unis.DEFAULT_USERAGENT
# Without "university" in config.toml everything is as before: Heidelberg.
_DEFAULT = unis.get(unis.DEFAULT_ID)


class ConfigError(Exception):
    def __init__(self, message: str, line: int | None = None):
        super().__init__(message)
        self.line = line

    def __str__(self) -> str:
        if self.line:
            return f"config.toml line {self.line}: {self.args[0]}"
        return f"config.toml: {self.args[0]}"


@dataclass
class Config:
    host: str = _DEFAULT.host
    user: str = ""
    idle_minutes: float = 15.0
    socks_port: int = 1080
    http_port: int = 1081
    useragent: str = _DEFAULT.useragent
    # Profile (universities.json), each field can be overridden in config.toml
    university: str = unis.DEFAULT_ID
    university_name: str = _DEFAULT.name
    usergroup: str = _DEFAULT.usergroup
    authgroup: str = _DEFAULT.authgroup
    username_suffix: str = _DEFAULT.username_suffix
    os: str = _DEFAULT.os
    no_external_auth: bool = _DEFAULT.no_external_auth
    mfa: str = _DEFAULT.mfa
    totp_separator: str = _DEFAULT.totp_separator
    mfa_portal_url: str = _DEFAULT.mfa_portal_url
    mfa_steps: list[str] = field(default_factory=lambda: list(_DEFAULT.mfa_steps))
    default_domains: list[str] = field(default_factory=lambda: list(_DEFAULT.default_domains))
    openconnect: str | None = None
    ocproxy: str | None = None
    auto_update: bool = True
    # [timing], all in seconds
    ready_timeout: float = 45.0
    client_wait: float = 25.0
    stop_grace: float = 15.0
    probe_timeout: float = 5.0
    keyring_timeout: float = 20.0
    halfclose_grace: float = 60.0
    demand_window: float = 60.0
    retry_interval: float = 30.0
    tick: float = 5.0
    backoff: list[float] = field(default_factory=lambda: [5, 10, 20, 40, 80, 300])
    path: Path | None = None

    @property
    def needs_totp(self) -> bool:
        return self.mfa in unis.TOTP_MODES

    @property
    def login_name(self) -> str:
        """The user name openconnect sends: with the profile's realm unless the user typed one."""
        if "@" in self.user or not self.username_suffix:
            return self.user
        return self.user + self.username_suffix


PROFILE_ATTRS = ("university", "university_name", *unis.PROFILE_FIELDS, "mfa_steps", "default_domains")


def apply_profile(cfg: Config, profile: unis.Profile) -> None:
    cfg.university = profile.id
    cfg.university_name = profile.name
    for name in unis.PROFILE_FIELDS:
        setattr(cfg, name, getattr(profile, name))
    cfg.mfa_steps = list(profile.mfa_steps)
    cfg.default_domains = list(profile.default_domains)


def copy_profile(target: Config, source: Config) -> None:
    """Take over university and profile fields, for example after the setup assistant wrote them."""
    for name in PROFILE_ATTRS:
        value = getattr(source, name)
        setattr(target, name, list(value) if isinstance(value, list) else value)


def check_overrides(values: dict) -> dict:
    """Profile fields from the setup assistant or config.toml, normalized. Raises unis.FieldError."""
    return {key: unis.check_field(key, value) for key, value in values.items()}


def profile_config(university: str, overrides: dict | None = None) -> Config:
    """The profile part of a Config for a university id plus overrides. Raises unis.FieldError."""
    profile = unis.get(university) if isinstance(university, str) else None
    if profile is None:
        raise unis.FieldError("university", f"Unknown university {university!r}")
    cfg = Config(user="")
    apply_profile(cfg, profile)
    for key, value in check_overrides(overrides or {}).items():
        setattr(cfg, key, value)
    if not cfg.host:
        raise unis.FieldError("host", "Enter the VPN address, for example vpn.example.edu")
    return cfg


_NUMBER = (int, float)
TOP_KEYS: dict[str, tuple[type, ...]] = {
    "host": (str,),
    "user": (str,),
    "idle_minutes": _NUMBER,
    "socks_port": (int,),
    "http_port": (int,),
    "useragent": (str,),
    "openconnect": (str,),
    "ocproxy": (str,),
    "auto_update": (bool,),
    "university": (str,),
    **{key: (kind,) for key, kind in unis.PROFILE_FIELDS.items()},
}
TIMING_KEYS: dict[str, tuple[type, ...]] = {
    "ready_timeout": _NUMBER,
    "client_wait": _NUMBER,
    "stop_grace": _NUMBER,
    "probe_timeout": _NUMBER,
    "keyring_timeout": _NUMBER,
    "halfclose_grace": _NUMBER,
    "demand_window": _NUMBER,
    "retry_interval": _NUMBER,
    "tick": _NUMBER,
    "backoff": (list,),
}


def default_path() -> Path:
    from .platform import config_dir

    return config_dir() / "config.toml"


def _line_of_key(text: str, key: str, table: str | None = None) -> int | None:
    in_table = table is None
    for number, line in enumerate(text.splitlines(), start=1):
        stripped = line.strip()
        if stripped.startswith("["):
            in_table = stripped == f"[{table}]"
            continue
        if in_table and re.match(rf"^\s*{re.escape(key)}\s*=", line):
            return number
    return None


def _check(key: str, value, types: tuple[type, ...], text: str, table: str | None) -> None:
    ok = isinstance(value, types) and not (isinstance(value, bool) and bool not in types)
    if not ok:
        names = "/".join(t.__name__ for t in types)
        raise ConfigError(f"'{key}' must be {names}", line=_line_of_key(text, key, table))


def load(path: Path | None = None) -> Config:
    path = path or default_path()
    try:
        text = path.read_text(encoding="utf-8")
    except FileNotFoundError:
        raise ConfigError(f"{path} is missing, please run install.sh") from None
    try:
        data = tomllib.loads(text)
    except tomllib.TOMLDecodeError as exc:
        match = re.search(r"at line (\d+)", str(exc))
        raise ConfigError(str(exc), line=int(match.group(1)) if match else None) from None

    cfg = Config(path=path)
    university = data.get("university", unis.DEFAULT_ID)
    _check("university", university, (str,), text, None)
    profile = unis.get(university)
    if profile is None:
        raise ConfigError(f"unknown university '{university}', see uni_vpn/universities.json",
                          line=_line_of_key(text, "university"))
    apply_profile(cfg, profile)
    for key, value in data.items():
        if key == "timing":
            if not isinstance(value, dict):
                raise ConfigError("'timing' must be a table", line=_line_of_key(text, key))
            for tkey, tvalue in value.items():
                if tkey not in TIMING_KEYS:
                    raise ConfigError(f"unknown key '{tkey}'", line=_line_of_key(text, tkey, "timing"))
                _check(tkey, tvalue, TIMING_KEYS[tkey], text, "timing")
                if tkey == "backoff" and not all(isinstance(v, _NUMBER) for v in tvalue):
                    raise ConfigError("'backoff' must be a list of numbers", line=_line_of_key(text, tkey, "timing"))
                setattr(cfg, tkey, tvalue)
            continue
        if key not in TOP_KEYS:
            raise ConfigError(f"unknown key '{key}'", line=_line_of_key(text, key))
        _check(key, value, TOP_KEYS[key], text, None)
        if key == "university":
            continue
        if key in unis.PROFILE_FIELDS:
            try:
                value = unis.check_field(key, value, strict=False)
            except unis.FieldError as exc:
                raise ConfigError(str(exc), line=_line_of_key(text, key)) from None
        setattr(cfg, key, value)

    if not cfg.user:
        raise ConfigError("'user' (university ID) is missing")
    if not cfg.host:
        raise ConfigError("'host' is missing, it is needed with university = \"other\"")
    for name in ("socks_port", "http_port"):
        port = getattr(cfg, name)
        if not 1 <= port <= 65535:
            raise ConfigError(f"'{name}' must be between 1 and 65535", line=_line_of_key(text, name))
    if cfg.socks_port == cfg.http_port:
        raise ConfigError("'socks_port' and 'http_port' must differ")
    if cfg.idle_minutes <= 0:
        raise ConfigError("'idle_minutes' must be greater than 0", line=_line_of_key(text, "idle_minutes"))
    if not cfg.backoff:
        raise ConfigError("'backoff' must not be empty", line=_line_of_key(text, "backoff", "timing"))
    return cfg


TEMPLATE = """# uni-vpn configuration
university = "{university}"  # profile from uni_vpn/universities.json, "other" if not listed
user = "{user}"          # university ID
{overrides}idle_minutes = 15        # tear the tunnel down after this many minutes without traffic
socks_port = 1080        # SOCKS5 proxy for the browser
http_port = 1081         # status page http://127.0.0.1:1081
# openconnect = "/usr/sbin/openconnect"
# ocproxy = "/usr/bin/ocproxy"
"""


def toml_value(value) -> str:
    if isinstance(value, bool):
        return "true" if value else "false"
    if isinstance(value, int):
        return str(value)
    # A JSON string is a valid TOML basic string.
    return json.dumps(str(value), ensure_ascii=False)


def write_initial(path: Path, user: str, university: str = unis.DEFAULT_ID, overrides: dict | None = None) -> None:
    if not valid_user(user):
        raise ConfigError(f"invalid university ID: {user!r}")
    try:
        overrides = check_overrides(overrides or {})
        profile_config(university, overrides)
    except unis.FieldError as exc:
        raise ConfigError(str(exc)) from None
    lines = "".join(f"{key} = {toml_value(value)}\n" for key, value in overrides.items())
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    path.write_text(TEMPLATE.format(university=university, user=user, overrides=lines), encoding="utf-8")
    path.chmod(0o600)


# "ab123", or with a realm the university asks for: "st123456@stud.uni-stuttgart.de"
USER_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,63}(?:@[A-Za-z0-9][A-Za-z0-9.-]{0,252})?$")


def valid_user(user: str) -> bool:
    return bool(USER_RE.fullmatch(user or ""))


def set_user(path: Path, user: str) -> None:
    """Write a new university ID into an existing config.toml, keeping everything else."""
    if not valid_user(user):
        raise ConfigError(f"invalid university ID: {user!r}")
    set_values(path, {"user": user})


# A basic ("...") or literal ('...') string or a boolean, as written by hand.
_VALUE = r"""(?:"(?:[^"\\\n]|\\.)*"|'[^'\n]*'|true|false|[0-9]+\b)"""


def set_values(path: Path, values: dict) -> None:
    """Set top-level keys in an existing config.toml, keeping comments, order and everything else."""
    text = new = path.read_text(encoding="utf-8")
    for key, value in values.items():
        literal = toml_value(value)
        new, count = re.subn(rf"(?m)^(\s*{re.escape(key)}\s*=\s*){_VALUE}",
                             lambda m: m.group(1) + literal, new, count=1)
        if not count:
            head, sep, rest = new.partition("\n[")
            new = head.rstrip("\n") + f"\n{key} = {literal}\n" + (sep.lstrip("\n") and "\n[" + rest)
    # Forms the regex does not know ('''...''', "user" = ...) must not end in a broken file.
    try:
        written = tomllib.loads(new)
    except tomllib.TOMLDecodeError:
        written = {}
    if any(written.get(key) != value for key, value in values.items()):
        raise ConfigError(f"could not change {', '.join(values)} in {path}, please edit it by hand")
    if new != text:
        path.write_text(new, encoding="utf-8")


def remove_keys(path: Path, keys) -> None:
    """Delete top-level keys from an existing config.toml, keeping everything else. Used when
    the university changes: overrides that belonged to the old profile must not stay behind."""
    text = path.read_text(encoding="utf-8")
    table = re.search(r"(?m)^[ \t]*\[", text)
    head, rest = (text[:table.start()], text[table.start():]) if table else (text, "")
    for key in keys:
        head = re.sub(rf"(?m)^[ \t]*{re.escape(key)}[ \t]*=[ \t]*{_VALUE}[ \t]*(?:#[^\n]*)?(?:\n|$)", "", head)
    new = head + rest
    try:
        written = tomllib.loads(new)
    except tomllib.TOMLDecodeError:
        written = {"": None}
    if any(key in written for key in keys) or "" in written:
        raise ConfigError(f"could not remove {', '.join(keys)} from {path}, please edit it by hand")
    if new != text:
        path.write_text(new, encoding="utf-8")


_PORT_LINE = re.compile(r"^\s*(socks_port|http_port)\s*=\s*([0-9]{1,5})\s*(#.*)?$")


def ports_from_broken(path: Path | None) -> dict[str, int]:
    """Read the ports from a broken config.toml as far as possible.

    Even then the status page must be reachable where browsers and the user expect
    it, otherwise nobody sees the error message with the line number.
    """
    try:
        text = (path or default_path()).read_text(encoding="utf-8")
    except OSError:
        return {}
    ports: dict[str, int] = {}
    for line in text.splitlines():
        if line.strip().startswith("["):
            break  # tables like [timing] from here on, no more top-level keys
        match = _PORT_LINE.match(line)
        if match and 1 <= int(match.group(2)) <= 65535:
            ports[match.group(1)] = int(match.group(2))
    if len(ports) == 2 and ports["socks_port"] == ports["http_port"]:
        return {}
    return ports
