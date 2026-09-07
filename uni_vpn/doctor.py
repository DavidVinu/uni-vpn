"""Selbstdiagnose. Gibt keine Geheimnisse aus, Ausgabe ist fuer GitHub-Issues gedacht."""

from __future__ import annotations

import asyncio
import sys
from dataclasses import dataclass
from pathlib import Path

from . import __version__, cli, config, credentials, service
from . import platform as pf
from .tunnel import port_open


@dataclass
class Check:
    name: str
    status: str  # ok | warn | fail
    detail: str


def keyring_state(user: str, timeout: float = 5) -> str:
    async def probe() -> str:
        try:
            await credentials.get_password(user, timeout)
            return "present"
        except credentials.PasswordMissing:
            return "missing"
        except credentials.KeyringLocked:
            return "locked"
        except credentials.KeyringError as exc:
            return f"error:{exc}"

    try:
        return asyncio.run(probe())
    except Exception as exc:  # noqa: BLE001
        return f"error:{exc}"


def run_checks(cfg_path: Path | None = None, *,
               find_binary=pf.find_binary, is_active=service.is_active, port_in_use=port_open,
               keyring_probe=keyring_state, cisco_installed=pf.cisco_installed,
               cisco_connected=pf.cisco_connected, api_get=cli.api_get,
               python_version=sys.version_info) -> list[Check]:
    checks: list[Check] = []
    major, minor = python_version[0], python_version[1]
    checks.append(Check("Python", "ok" if (major, minor) >= (3, 11) else "fail",
                        f"{major}.{minor} ({sys.executable})" + ("" if (major, minor) >= (3, 11) else ", mindestens 3.11 noetig")))

    cfg = config.Config()
    try:
        cfg = config.load(cfg_path)
        checks.append(Check("Konfiguration", "ok", f"{cfg.path}, Uni-ID {cfg.user}, Host {cfg.host}"))
    except config.ConfigError as exc:
        checks.append(Check("Konfiguration", "fail", str(exc)))

    for name, override in (("openconnect", cfg.openconnect), ("ocproxy", cfg.ocproxy)):
        found = find_binary(name, override)
        checks.append(Check(name, "ok" if found else "fail", found or "nicht gefunden, install.sh ausfuehren"))
    if not pf.IS_MACOS:
        found = find_binary("secret-tool")
        checks.append(Check("secret-tool", "ok" if found else "fail", found or "nicht gefunden, install.sh ausfuehren (libsecret-tools)"))

    active = is_active()
    checks.append(Check("Dienst", "ok" if active else "fail",
                        "laeuft" if active else "laeuft nicht: uni-vpn service start, Log: uni-vpn log"))

    for label, port in (("Port %d" % cfg.socks_port, cfg.socks_port), ("Port %d" % cfg.http_port, cfg.http_port)):
        in_use = port_in_use(port)
        if active and in_use:
            checks.append(Check(label, "ok", "gebunden"))
        elif active and not in_use:
            checks.append(Check(label, "fail", "Dienst laeuft, Port aber nicht gebunden, siehe uni-vpn log"))
        elif in_use:
            checks.append(Check(label, "fail", "belegt von einem anderen Prozess (ss -ltnp bzw. lsof -i), Port in config.toml aendern"))
        else:
            checks.append(Check(label, "warn", "frei, Dienst laeuft nicht"))

    if cfg.user:
        state = keyring_probe(cfg.user)
        mapping = {"present": ("ok", "Passwort hinterlegt"),
                   "missing": ("fail", "kein Passwort hinterlegt: uni-vpn password"),
                   "locked": ("warn", "Schluesselbund gesperrt oder keine Antwort")}
        status, detail = mapping.get(state, ("fail", state.replace("error:", "Fehler: ")))
        checks.append(Check("Keyring", status, detail))

    if cisco_installed():
        connected = cisco_connected()
        checks.append(Check("Cisco Secure Client", "warn" if connected else "ok",
                            "verbunden, uni-vpn pausiert solange" if connected else "installiert, getrennt"))
    else:
        checks.append(Check("Cisco Secure Client", "ok", "nicht installiert"))

    try:
        status = api_get(cfg, "/status.json")
        bad = status["state"] in ("auth_failed", "keyring", "error", "blocked")
        checks.append(Check("Daemon", "warn" if bad else "ok", f"{status['state']}: {status['message']} (Version {status['version']})"))
    except cli.DaemonUnreachable:
        checks.append(Check("Daemon", "fail", "Statusseite nicht erreichbar"))

    browsers = [name for name in ("google-chrome", "google-chrome-stable", "chromium", "firefox", "brave-browser")
                if find_binary(name)]
    checks.append(Check("Browser", "ok", ", ".join(browsers) if browsers else "keiner im PATH gefunden (macOS: normal)"))
    checks.append(Check("uni-vpn", "ok", f"Version {__version__}, Repo {pf.repo_root()}"))
    return checks


def format_checks(checks: list[Check]) -> str:
    marks = {"ok": "[OK]", "warn": "[..]", "fail": "[!!]"}
    return "\n".join(f"{marks[c.status]} {c.name}: {c.detail}" for c in checks)
