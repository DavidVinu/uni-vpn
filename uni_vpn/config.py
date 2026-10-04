"""Read and write config.toml."""

from __future__ import annotations

import re
import tomllib
from dataclasses import dataclass, field
from pathlib import Path

DEFAULT_HOST = "vpn-ac.uni-heidelberg.de"
DEFAULT_USERAGENT = "AnyConnect Linux_64 5.1.18.314"


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
    host: str = DEFAULT_HOST
    user: str = ""
    idle_minutes: float = 15.0
    socks_port: int = 1080
    http_port: int = 1081
    useragent: str = DEFAULT_USERAGENT
    openconnect: str | None = None
    ocproxy: str | None = None
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
        setattr(cfg, key, value)

    if not cfg.user:
        raise ConfigError("'user' (university ID) is missing")
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
host = "{host}"
user = "{user}"          # university ID
idle_minutes = 15        # tear the tunnel down after this many minutes without traffic
socks_port = 1080        # SOCKS5 proxy for the browser
http_port = 1081         # status page http://127.0.0.1:1081
# useragent = "{useragent}"
# openconnect = "/usr/sbin/openconnect"
# ocproxy = "/usr/bin/ocproxy"
"""


def write_initial(path: Path, user: str, host: str = DEFAULT_HOST) -> None:
    if not valid_user(user):
        raise ConfigError(f"invalid university ID: {user!r}")
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    path.write_text(TEMPLATE.format(host=host, user=user, useragent=DEFAULT_USERAGENT), encoding="utf-8")
    path.chmod(0o600)


USER_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")


def valid_user(user: str) -> bool:
    return bool(USER_RE.fullmatch(user or ""))


def set_user(path: Path, user: str) -> None:
    """Write a new university ID into an existing config.toml, keeping everything else."""
    if not valid_user(user):
        raise ConfigError(f"invalid university ID: {user!r}")
    text = path.read_text(encoding="utf-8")
    new, count = re.subn(r'(?m)^(\s*user\s*=\s*)"[^"\n]*"', lambda m: f'{m.group(1)}"{user}"', text, count=1)
    if not count:
        head, sep, rest = text.partition("\n[")
        new = head.rstrip("\n") + f'\nuser = "{user}"\n' + (sep.lstrip("\n") and "\n[" + rest)
    path.write_text(new, encoding="utf-8")
