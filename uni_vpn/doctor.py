"""Selbstdiagnose. Gibt keine Geheimnisse aus, Ausgabe ist fuer GitHub-Issues gedacht."""

from __future__ import annotations

import asyncio
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
    """Erste Zeile von 'openconnect --version', leer wenn nicht abfragbar."""
    try:
        result = run([path, "--version"], capture_output=True, text=True, timeout=5)
    except (OSError, subprocess.SubprocessError):
        return ""
    return _first_line(result.stdout or "") or _first_line(result.stderr or "")


def port_owner(port: int, run=subprocess.run) -> str:
    """Prozesszeile aus ss (Linux) bzw. lsof (macOS) fuer einen belegten Port, gekuerzt. Leer, wenn unbekannt."""
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
                        f"{major}.{minor} ({sys.executable})" + ("" if (major, minor) >= (3, 11) else ", mindestens 3.11 noetig")))

    cfg = config.Config()
    try:
        cfg = config.load(cfg_path)
        checks.append(Check("Konfiguration", "ok", f"{cfg.path}, Uni-ID {cfg.user}, Host {cfg.host}"))
    except config.ConfigError as exc:
        checks.append(Check("Konfiguration", "fail", str(exc)))

    for name, override in (("openconnect", cfg.openconnect), ("ocproxy", cfg.ocproxy)):
        found = find_binary(name, override)
        detail = found or "nicht gefunden, install.sh ausfuehren"
        if found and name == "openconnect":
            version = openconnect_version(found, run)
            detail = f"{found}, {version}" if version else found
        checks.append(Check(name, "ok" if found else "fail", detail))
    if not pf.IS_MACOS:
        found = find_binary("secret-tool")
        checks.append(Check("secret-tool", "ok" if found else "fail", found or "nicht gefunden, install.sh ausfuehren (libsecret-tools)"))

    # Erst den Daemon fragen: antwortet er, gehoeren die Ports uns, egal ob systemd/launchd ihn gestartet hat.
    daemon_status: dict | None = None
    try:
        daemon_status = api_get(cfg, "/status.json")
    except cli.DaemonUnreachable:
        pass
    daemon_up = bool(daemon_status) and daemon_status.get("protocol") == 1

    active = is_active()
    if active:
        checks.append(Check("Dienst", "ok", "laeuft"))
    elif daemon_up:
        checks.append(Check("Dienst", "warn", "Daemon laeuft, aber nicht als Dienst (von Hand gestartet?)"))
    else:
        checks.append(Check("Dienst", "fail", "laeuft nicht: uni-vpn service start, Log: uni-vpn log"))

    for label, port in (("Port %d" % cfg.socks_port, cfg.socks_port), ("Port %d" % cfg.http_port, cfg.http_port)):
        in_use = port_in_use(port)
        if daemon_up and in_use:
            checks.append(Check(label, "ok", "gebunden (uni-vpn)"))
        elif daemon_up:
            checks.append(Check(label, "fail", "Daemon antwortet, Port aber nicht gebunden, siehe uni-vpn log"))
        elif active and in_use:
            checks.append(Check(label, "ok", "gebunden"))
        elif active:
            checks.append(Check(label, "fail", "Dienst laeuft, Port aber nicht gebunden, siehe uni-vpn log"))
        elif in_use:
            owner = port_owner(port, run)
            checks.append(Check(label, "fail", "belegt von anderem Prozess" + (f": {owner}" if owner else "")
                                + ", Port in config.toml aendern"))
        else:
            checks.append(Check(label, "warn", "frei, Dienst laeuft nicht"))

    if cfg.user:
        state = keyring_probe(cfg.user)
        mapping = {"present": ("ok", "Passwort hinterlegt"),
                   "missing": ("fail", "kein Passwort hinterlegt: uni-vpn password"),
                   "locked": ("warn", "Schluesselbund gesperrt oder keine Antwort")}
        status, detail = mapping.get(state, ("fail", state.replace("error:", "Fehler: ")))
        checks.append(Check("Keyring", status, detail))
        state = keyring_probe(cfg.user, kind="totp")
        mapping = {"present": ("ok", "TOTP-Schluessel hinterlegt"),
                   "missing": ("fail", "kein TOTP-Schluessel hinterlegt: uni-vpn totp"),
                   "locked": ("warn", "Schluesselbund gesperrt oder keine Antwort")}
        status, detail = mapping.get(state, ("fail", state.replace("error:", "Fehler: ")))
        checks.append(Check("Zweiter Faktor", status, detail))

    url = sysproxy.pac_url(cfg.http_port)
    proxy = proxy_state(cfg.http_port)
    proxy_map = {"ok": ("ok", f"System liest {url}"),
                 "unset": ("fail", "nicht im System eingetragen: install.sh erneut ausfuehren"),
                 "foreign": ("warn", "eine andere Proxy-Einstellung ist aktiv, install.sh erneut ausfuehren ersetzt sie"),
                 "unavailable": ("warn", f"keine GNOME- oder macOS-Proxyverwaltung; im Browser von Hand eintragen: {url}")}
    checks.append(Check("Proxy-Regel", *proxy_map.get(proxy, ("warn", proxy))))

    if cisco_installed():
        connected = cisco_connected()
        checks.append(Check("Cisco Secure Client", "warn" if connected else "ok",
                            "verbunden, uni-vpn pausiert solange" if connected else "installiert, getrennt"))
    else:
        checks.append(Check("Cisco Secure Client", "ok", "nicht installiert"))

    if daemon_status:
        bad = daemon_status["state"] in ("auth_failed", "keyring", "error", "blocked")
        checks.append(Check("Daemon", "warn" if bad else "ok",
                            f"{daemon_status['state']}: {daemon_status['message']} (Version {daemon_status['version']})"))
    else:
        checks.append(Check("Daemon", "fail", "Statusseite nicht erreichbar"))

    browsers = [name for name in ("google-chrome", "google-chrome-stable", "chromium", "firefox", "brave-browser")
                if find_binary(name)]
    checks.append(Check("Browser", "ok", ", ".join(browsers) if browsers else "keiner im PATH gefunden (macOS: normal)"))
    checks.append(Check("uni-vpn", "ok", f"Version {__version__}, Repo {pf.repo_root()}"))
    return checks


def format_checks(checks: list[Check]) -> str:
    marks = {"ok": "[OK]", "warn": "[..]", "fail": "[!!]"}
    return "\n".join(f"{marks[c.status]} {c.name}: {c.detail}" for c in checks)
