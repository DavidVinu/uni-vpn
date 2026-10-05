"""Operating system, paths, binaries, Cisco detection."""

from __future__ import annotations

import os
import shutil
import subprocess
import sys
from pathlib import Path

IS_MACOS = sys.platform == "darwin"
IS_WINDOWS = sys.platform == "win32"
APP = "uni-vpn"
SECURITY = "/usr/bin/security"
# The certificates macOS itself ships, in the form OpenSSL and GnuTLS read.
MACOS_CA_FILE = "/etc/ssl/cert.pem"
_PROGRAM_FILES = [os.environ.get(name) for name in ("ProgramFiles", "ProgramFiles(x86)", "ProgramW6432")]
_PROGRAM_FILES = list(dict.fromkeys(p for p in _PROGRAM_FILES if p)) or ["C:\\Program Files", "C:\\Program Files (x86)"]
if IS_WINDOWS:
    CISCO_VPN = next((os.path.join(base, "Cisco", "Cisco Secure Client", "vpncli.exe") for base in _PROGRAM_FILES
                      if os.path.isfile(os.path.join(base, "Cisco", "Cisco Secure Client", "vpncli.exe"))),
                     os.path.join(_PROGRAM_FILES[-1], "Cisco", "Cisco Secure Client", "vpncli.exe"))
    # openconnect.exe ships with OpenConnect-GUI (with Wintun), which install.ps1 installs.
    SEARCH_DIRS = [os.path.join(base, name) for base in _PROGRAM_FILES for name in ("OpenConnect-GUI", "OpenConnect")]
else:
    CISCO_VPN = "/opt/cisco/secureclient/bin/vpn"
    SEARCH_DIRS = [
        "/usr/sbin", "/usr/bin", "/opt/homebrew/bin", "/opt/homebrew/sbin",
        "/usr/local/bin", "/usr/local/sbin", "/bin", "/sbin",
    ]


# The installers (packaging/) put a read-only copy of the program here, which each user's own
# copy in app_install_dir() starts from; on macOS also Python, openconnect and ocproxy.
if IS_MACOS:
    PACKAGE_DIR = "/Applications/Uni VPN.app/Contents/Resources"
elif IS_WINDOWS:
    PACKAGE_DIR = None  # the Windows installer installs straight into app_install_dir()
else:
    PACKAGE_DIR = "/usr/share/uni-vpn"


def packaged(package_dir: str | None = None) -> bool:
    """True when uni-vpn came from the .pkg, .deb or .rpm, which bring their own app entry."""
    package_dir = package_dir or PACKAGE_DIR
    return bool(package_dir) and os.path.isfile(os.path.join(package_dir, "app", "uni_vpn", "__init__.py"))


def bundled_bin_dir(package_dir: str | None = None) -> str | None:
    """macOS: openconnect and ocproxy from the .pkg, built for this processor."""
    package_dir = package_dir or PACKAGE_DIR
    if not IS_MACOS or not package_dir:
        return None
    directory = os.path.join(package_dir, os.uname().machine, "bin")
    return directory if os.path.isdir(directory) else None


def is_bundled(path: str, package_dir: str | None = None) -> bool:
    package_dir = package_dir or PACKAGE_DIR
    if not package_dir or IS_WINDOWS:
        return False
    return os.path.realpath(path).startswith(os.path.realpath(package_dir) + os.sep)


def repo_root() -> Path:
    return Path(__file__).resolve().parent.parent


def bin_dir() -> Path:
    return repo_root() / "bin"


def _local_appdata() -> Path:
    return Path(os.environ.get("LOCALAPPDATA") or (Path.home() / "AppData" / "Local"))


def config_dir() -> Path:
    if IS_WINDOWS and not os.environ.get("XDG_CONFIG_HOME"):
        return _local_appdata() / APP
    base = os.environ.get("XDG_CONFIG_HOME") or str(Path.home() / ".config")
    return Path(base) / APP


def state_dir() -> Path:
    if IS_WINDOWS and not os.environ.get("XDG_STATE_HOME"):
        return _local_appdata() / APP / "logs"
    if IS_MACOS and not os.environ.get("XDG_STATE_HOME"):
        return Path.home() / "Library" / "Logs" / APP
    base = os.environ.get("XDG_STATE_HOME") or str(Path.home() / ".local" / "state")
    return Path(base) / APP


def log_file() -> Path:
    return state_dir() / "daemon.log"


def lock_file() -> Path:
    return config_dir() / "daemon.lock"


def admin_only(path: str) -> bool:
    """Windows: inside Program Files, which only administrators can change. The elevated
    service must not run programs that any process of the user could replace."""
    full = os.path.normcase(os.path.realpath(path))
    return any(full.startswith(os.path.normcase(os.path.realpath(base)) + os.sep) for base in _PROGRAM_FILES)


def find_binary(name: str, override: str | None = None) -> str | None:
    if IS_WINDOWS:
        # Neither PATH nor config.toml (both writable by the user) for the elevated service.
        if override and not admin_only(override):
            override = None
    if override:
        return override if os.access(override, os.X_OK) and os.path.isfile(override) else None
    bundled = bundled_bin_dir()
    if bundled:
        candidate = os.path.join(bundled, name)
        if os.access(candidate, os.X_OK) and os.path.isfile(candidate):
            return candidate
    found = None if IS_WINDOWS else shutil.which(name)
    if found:
        return found
    names = [name, name + ".exe"] if IS_WINDOWS and not name.lower().endswith(".exe") else [name]
    for directory in SEARCH_DIRS:
        for candidate_name in names:
            candidate = os.path.join(directory, candidate_name)
            if os.access(candidate, os.X_OK) and os.path.isfile(candidate):
                return candidate
    return None


def tunnel_helpers() -> list[str]:
    """Programs the tunnel needs besides openconnect. Windows uses the built-in SOCKS server."""
    return [] if IS_WINDOWS else ["ocproxy"]


def cisco_installed() -> bool:
    return os.access(CISCO_VPN, os.X_OK)


def cisco_connected(run=subprocess.run, cscotun: Path = Path("/sys/class/net/cscotun0")) -> bool:
    if not IS_WINDOWS and cscotun.exists():
        return True
    if not (IS_MACOS or IS_WINDOWS):
        # On Linux the Cisco client always creates cscotun0 when connected. "vpn state"
        # takes 2.2 s (measured 2026-09-08) and would delay every connect.
        return False
    if not cisco_installed():
        return False
    extra = {"creationflags": 0x08000000} if IS_WINDOWS else {}  # CREATE_NO_WINDOW
    try:
        result = run([CISCO_VPN, "state"], capture_output=True, text=True, timeout=5, **extra)
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


def has_desktop() -> bool:
    """True when a browser window can be shown to the person running setup."""
    if IS_WINDOWS:
        return True
    if os.environ.get("SSH_CONNECTION") and not os.environ.get("DISPLAY"):
        return False
    if IS_MACOS:
        return True
    return bool(os.environ.get("DISPLAY") or os.environ.get("WAYLAND_DISPLAY"))


def open_url(url: str, popen=subprocess.Popen) -> bool:
    """Open a URL in the default browser. False when there is no desktop to show it on."""
    if not has_desktop():
        return False
    try:
        if IS_WINDOWS:
            os.startfile(url)  # noqa: S606 - our own loopback URL
            return True
        # xdg-open can stay in the foreground when it starts a new browser: no pipes the
        # browser could inherit, and a launcher that is still running counts as success.
        process = popen(["open" if IS_MACOS else "xdg-open", url], stdin=subprocess.DEVNULL,
                        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, start_new_session=True)
        try:
            return process.wait(timeout=3) == 0
        except subprocess.TimeoutExpired:
            return True
    except (OSError, subprocess.SubprocessError):
        return False


def app_install_dir() -> Path:
    """Where get.sh and get.ps1 put the program."""
    if IS_WINDOWS:
        # Program Files: the elevated service runs this code, so only administrators may change it.
        return Path(_PROGRAM_FILES[0]) / APP
    if IS_MACOS:
        return Path.home() / "Library" / "Application Support" / APP / "app"
    base = os.environ.get("XDG_DATA_HOME") or str(Path.home() / ".local" / "share")
    return Path(base) / APP / "app"
