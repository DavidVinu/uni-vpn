"""Self-diagnosis. Prints no secrets; the output is meant for GitHub issues."""

from __future__ import annotations

import asyncio
import os
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path

from . import __version__, cli, config, credentials, service, sysproxy
from . import platform as pf
from .tunnel import port_open


@dataclass
class Check:
    name: str
    status: str  # ok | warn | fail
    detail: str


def keyring_state(user: str, timeout: float = 5, kind: str = "password") -> str:
    async def probe() -> str:
        try:
            await credentials.get_secret(user, kind, timeout)
            return "present"
        except credentials.SecretMissing:
            return "missing"
        except credentials.KeyringLocked:
            return "locked"
        except credentials.KeyringError as exc:
            return f"error:{exc}"

    try:
        return asyncio.run(probe())
    except Exception as exc:  # noqa: BLE001
        return f"error:{exc}"


def _first_line(text: str) -> str:
    return " ".join(text.strip().splitlines()[0].split()) if text.strip() else ""


def openconnect_version(path: str, run=subprocess.run) -> str:
    """First line of 'openconnect --version', empty if it cannot be queried."""
    try:
        result = run([path, "--version"], capture_output=True, text=True, timeout=5)
    except (OSError, subprocess.SubprocessError):
        return ""
    return _first_line(result.stdout or "") or _first_line(result.stderr or "")


def port_owner(port: int, run=subprocess.run) -> str:
    """Process line from ss (Linux) or lsof (macOS) for a port in use, shortened. Empty if unknown."""
    if pf.IS_WINDOWS:
        try:
            result = run(["netstat", "-ano", "-p", "TCP"], capture_output=True, text=True, errors="replace", timeout=10)
        except (OSError, subprocess.SubprocessError):
            return ""
        # The state word is localized ("ABHÖREN" in German): a listening socket has no foreign address.
        for line in (result.stdout or "").splitlines():
            parts = line.split()
            if (len(parts) >= 5 and parts[0].upper() == "TCP" and parts[1].endswith(f":{port}")
                    and parts[2] in ("0.0.0.0:0", "[::]:0")):
                return f"PID {parts[-1]}"
        return ""
    if pf.IS_MACOS:
        cmd = ["lsof", "-nP", f"-iTCP:{port}", "-sTCP:LISTEN"]
    else:
        cmd = ["ss", "-ltnp", f"sport = :{port}"]
    try:
        result = run(cmd, capture_output=True, text=True, timeout=5)
    except (OSError, subprocess.SubprocessError):
        return ""
    lines = [" ".join(line.split()) for line in (result.stdout or "").splitlines()[1:] if line.strip()]
    if not lines:
        return ""
    line = lines[0]
    return line if len(line) <= 120 else line[:117] + "..."


def run_checks(cfg_path: Path | None = None, *,
               find_binary=pf.find_binary, is_active=service.is_active, port_in_use=port_open,
               keyring_probe=keyring_state, proxy_state=sysproxy.state, cisco_installed=pf.cisco_installed,
               cisco_connected=pf.cisco_connected, api_get=cli.api_get,
               python_version=sys.version_info, run=subprocess.run) -> list[Check]:
    checks: list[Check] = []
    major, minor = python_version[0], python_version[1]
    checks.append(Check("Python", "ok" if (major, minor) >= (3, 11) else "fail",
                        f"{major}.{minor} ({sys.executable})" + ("" if (major, minor) >= (3, 11) else ", at least 3.11 required")))

    cfg = config.Config()
    try:
        cfg = config.load(cfg_path)
        checks.append(Check("Config", "ok", f"{cfg.path}, university ID {cfg.user}, host {cfg.host}"))
    except config.ConfigError as exc:
        checks.append(Check("Config", "fail", str(exc)))

    programs = [("openconnect", cfg.openconnect)] + [(name, cfg.ocproxy) for name in pf.tunnel_helpers()]
    for name, override in programs:
        found = find_binary(name, override)
        detail = found or "not found, run install.sh"
        if found and name == "openconnect":
            version = openconnect_version(found, run)
            detail = f"{found}, {version}" if version else found
        checks.append(Check(name, "ok" if found else "fail", detail))
    if pf.IS_WINDOWS:
        found = find_binary("openconnect", cfg.openconnect)
        wintun = bool(found) and os.path.isfile(os.path.join(os.path.dirname(found), "wintun.dll"))
        checks.append(Check("Wintun", "ok" if wintun else "fail",
                            "wintun.dll next to openconnect" if wintun else "wintun.dll missing, run install.ps1 again"))
    elif not pf.IS_MACOS:
        found = find_binary("secret-tool")
        checks.append(Check("secret-tool", "ok" if found else "fail", found or "not found, run install.sh (libsecret-tools)"))

    # Ask the daemon first: if it answers, the ports are ours, whether or not systemd/launchd started it.
    daemon_status: dict | None = None
    try:
        daemon_status = api_get(cfg, "/status.json")
    except cli.DaemonUnreachable:
        pass
    daemon_up = bool(daemon_status) and daemon_status.get("protocol") == 1

    active = is_active()
    if active:
        checks.append(Check("Service", "ok", "running"))
    elif daemon_up:
        checks.append(Check("Service", "warn", "Daemon running, but not as a service (started by hand?)"))
    else:
        checks.append(Check("Service", "fail", "not running: uni-vpn service start, log: uni-vpn log"))

    for label, port in (("Port %d" % cfg.socks_port, cfg.socks_port), ("Port %d" % cfg.http_port, cfg.http_port)):
        in_use = port_in_use(port)
        if daemon_up and in_use:
            checks.append(Check(label, "ok", "bound (uni-vpn)"))
        elif daemon_up:
            checks.append(Check(label, "fail", "daemon answers, but the port is not bound, see uni-vpn log"))
        elif active and in_use:
            checks.append(Check(label, "ok", "bound"))
        elif active:
            checks.append(Check(label, "fail", "service running, but the port is not bound, see uni-vpn log"))
        elif in_use:
            owner = port_owner(port, run)
            checks.append(Check(label, "fail", "in use by another process" + (f": {owner}" if owner else "")
                                + ", change the port in config.toml"))
        else:
            checks.append(Check(label, "warn", "free, service not running"))

    if cfg.user:
        state = keyring_probe(cfg.user)
        mapping = {"present": ("ok", "password stored"),
                   "missing": ("fail", "no password stored: uni-vpn password"),
                   "locked": ("warn", "keyring locked or not responding")}
        status, detail = mapping.get(state, ("fail", state.replace("error:", "error: ")))
        checks.append(Check("Keyring", status, detail))
        state = keyring_probe(cfg.user, kind="totp")
        mapping = {"present": ("ok", "TOTP secret stored"),
                   "missing": ("fail", "no TOTP secret stored: uni-vpn totp"),
                   "locked": ("warn", "keyring locked or not responding")}
        status, detail = mapping.get(state, ("fail", state.replace("error:", "error: ")))
        checks.append(Check("Second factor", status, detail))

    url = sysproxy.pac_url(cfg.http_port)
    proxy = proxy_state(cfg.http_port)
    proxy_map = {"ok": ("ok", f"system reads {url}"),
                 "unset": ("fail", "not registered with the system: run install.sh again"),
                 "foreign": ("warn", "another proxy setting is active, running install.sh again replaces it"),
                 "unavailable": ("warn", f"no GNOME, KDE, macOS or Windows proxy settings; enter it by hand in the browser: {url}")}
    checks.append(Check("Proxy rule", *proxy_map.get(proxy, ("warn", proxy))))

    if cisco_installed():
        connected = cisco_connected()
        checks.append(Check("Cisco Secure Client", "warn" if connected else "ok",
                            "connected, uni-vpn pauses while it is" if connected else "installed, disconnected"))
    else:
        checks.append(Check("Cisco Secure Client", "ok", "not installed"))

    if daemon_status and pf.IS_WINDOWS and daemon_status.get("elevated") is not None:
        elevated = daemon_status["elevated"]
        checks.append(Check("Administrator rights", "ok" if elevated else "fail",
                            "service runs elevated" if elevated
                            else "service is not elevated, Wintun needs it: run install.ps1 from an administrator account"))

    if daemon_status:
        bad = daemon_status["state"] in ("auth_failed", "keyring", "error", "blocked")
        checks.append(Check("Daemon", "warn" if bad else "ok",
                            f"{daemon_status['state']}: {daemon_status['message']} (version {daemon_status['version']})"))
    else:
        checks.append(Check("Daemon", "fail", "status page not reachable"))

    browsers = [name for name in ("google-chrome", "google-chrome-stable", "chromium", "firefox", "brave-browser")
                if find_binary(name)]
    checks.append(Check("Browser", "ok", ", ".join(browsers) if browsers else "none found in PATH (normal on macOS and Windows)"))
    checks.append(Check("uni-vpn", "ok", f"version {__version__}, repo {pf.repo_root()}"))
    return checks


def format_checks(checks: list[Check]) -> str:
    marks = {"ok": "[OK]", "warn": "[..]", "fail": "[!!]"}
    return "\n".join(f"{marks[c.status]} {c.name}: {c.detail}" for c in checks)
