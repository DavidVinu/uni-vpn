"""Betriebssystem, Pfade, Binaries, Cisco-Erkennung."""

from __future__ import annotations

import os
import shutil
import subprocess
import sys
from pathlib import Path

IS_MACOS = sys.platform == "darwin"
APP = "uni-vpn"
CISCO_VPN = "/opt/cisco/secureclient/bin/vpn"
SECURITY = "/usr/bin/security"
SEARCH_DIRS = [
    "/usr/sbin", "/usr/bin", "/opt/homebrew/bin", "/opt/homebrew/sbin",
    "/usr/local/bin", "/usr/local/sbin", "/bin", "/sbin",
]


def repo_root() -> Path:
    return Path(__file__).resolve().parent.parent


def bin_dir() -> Path:
    return repo_root() / "bin"


def config_dir() -> Path:
    base = os.environ.get("XDG_CONFIG_HOME") or str(Path.home() / ".config")
    return Path(base) / APP


def state_dir() -> Path:
    if IS_MACOS:
        return Path.home() / "Library" / "Logs" / APP
    base = os.environ.get("XDG_STATE_HOME") or str(Path.home() / ".local" / "state")
    return Path(base) / APP


def log_file() -> Path:
    return state_dir() / "daemon.log"


def lock_file() -> Path:
    return config_dir() / "daemon.lock"


def find_binary(name: str, override: str | None = None) -> str | None:
    if override:
        return override if os.access(override, os.X_OK) and os.path.isfile(override) else None
    found = shutil.which(name)
    if found:
        return found
    for directory in SEARCH_DIRS:
        candidate = os.path.join(directory, name)
        if os.access(candidate, os.X_OK) and os.path.isfile(candidate):
            return candidate
    return None


def cisco_installed() -> bool:
    return os.access(CISCO_VPN, os.X_OK)


def cisco_connected(run=subprocess.run, cscotun: Path = Path("/sys/class/net/cscotun0")) -> bool:
    if cscotun.exists():
        return True
    if not cisco_installed():
        return False
    try:
        result = run([CISCO_VPN, "state"], capture_output=True, text=True, timeout=5)
    except (OSError, subprocess.SubprocessError):
        return False
    return "state: Connected" in (result.stdout or "")


def brew_prefix() -> str | None:
    for prefix in ("/opt/homebrew", "/usr/local"):
        if os.access(os.path.join(prefix, "bin", "brew"), os.X_OK):
            return prefix
    return None


def python_executable() -> str:
    return sys.executable
