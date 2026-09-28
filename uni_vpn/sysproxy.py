"""Proxy-Regel im System eintragen: GNOME (gsettings) auf Linux, networksetup auf macOS.

Chrome und Firefox lesen die Systemeinstellung "automatische Proxy-Konfiguration" und holen
sich die PAC-Datei vom Daemon. Der vorherige Zustand wird gesichert und beim Entfernen
wiederhergestellt.
"""

from __future__ import annotations

import json
import re
import subprocess
import time
from pathlib import Path

from . import platform as pf

SCHEMA = "org.gnome.system.proxy"
TIMEOUT = 20


def pac_url(http_port: int, version: int | None = None) -> str:
    url = f"http://127.0.0.1:{int(http_port)}/proxy.pac"
    return f"{url}?v={int(version)}" if version is not None else url


def is_ours(url: str, http_port: int) -> bool:
    return bool(url) and url.split("?", 1)[0] == pac_url(http_port)


def backup_path() -> Path:
    return pf.config_dir() / "proxy-backup.json"


def _run(cmd: list[str], run) -> subprocess.CompletedProcess:
    return run(cmd, capture_output=True, text=True, timeout=TIMEOUT)


def _gsettings_get(key: str, run) -> str | None:
    try:
        result = _run(["gsettings", "get", SCHEMA, key], run)
    except (OSError, subprocess.SubprocessError):
        return None
    if result.returncode != 0:
        return None
    return (result.stdout or "").strip().strip("'")


def _services(run) -> list[str]:
    result = _run(["networksetup", "-listallnetworkservices"], run)
    names = []
    for line in (result.stdout or "").splitlines()[1:]:
        line = line.strip()
        if line and not line.startswith("*"):  # * = deaktivierter Dienst
            names.append(line)
    return names


def current(run=subprocess.run) -> dict | None:
    """Aktuelle Einstellung oder None, wenn das System keine bekannte Proxy-Verwaltung hat."""
    try:
        if pf.IS_MACOS:
            services = {}
            for name in _services(run):
                out = _run(["networksetup", "-getautoproxyurl", name], run).stdout or ""
                url = re.search(r"^URL:\s*(.*)$", out, re.M)
                enabled = re.search(r"^Enabled:\s*(\w+)", out, re.M)
                url_value = (url.group(1).strip() if url else "")
                services[name] = {"url": "" if url_value == "(null)" else url_value,
                                  "enabled": bool(enabled and enabled.group(1).lower() == "yes")}
            return {"services": services}
    except (OSError, subprocess.SubprocessError):
        return None
    mode = _gsettings_get("mode", run)
    if mode is None:
        return None
    return {"mode": mode, "url": _gsettings_get("autoconfig-url", run) or ""}


def apply(url: str, run=subprocess.run) -> None:
    if pf.IS_MACOS:
        for name in _services(run):
            _run(["networksetup", "-setautoproxyurl", name, url], run)
        return
    _run(["gsettings", "set", SCHEMA, "autoconfig-url", url], run)
    _run(["gsettings", "set", SCHEMA, "mode", "auto"], run)


def restore(saved: dict, run=subprocess.run) -> None:
    if pf.IS_MACOS:
        for name, entry in (saved.get("services") or {}).items():
            if entry.get("enabled") and entry.get("url"):
                _run(["networksetup", "-setautoproxyurl", name, entry["url"]], run)
            else:
                _run(["networksetup", "-setautoproxystate", name, "off"], run)
        return
    _run(["gsettings", "set", SCHEMA, "mode", saved.get("mode") or "none"], run)
    _run(["gsettings", "set", SCHEMA, "autoconfig-url", saved.get("url") or ""], run)


def refresh(http_port: int, run=subprocess.run) -> None:
    """Neue URL-Version setzen: GNOME meldet die Aenderung, Chrome und Firefox laden die PAC neu."""
    if pf.IS_MACOS:
        return  # networksetup verlangt Admin-Rechte; Nutzer startet den Browser neu
    try:
        _run(["gsettings", "set", SCHEMA, "autoconfig-url", pac_url(http_port, int(time.time()))], run)
    except (OSError, subprocess.SubprocessError):
        pass


def install(http_port: int, backup: Path | None = None, run=subprocess.run) -> str:
    """Liefert 'ok', 'replaced' (fremde Einstellung ersetzt) oder 'unavailable'."""
    backup = backup or backup_path()
    before = current(run=run)
    if before is None:
        return "unavailable"
    if not backup.exists():
        backup.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        backup.write_text(json.dumps(before), encoding="utf-8")
    apply(pac_url(http_port), run=run)
    if pf.IS_MACOS:
        foreign = any(e.get("enabled") and not is_ours(e.get("url", ""), http_port) for e in before["services"].values())
    else:
        foreign = before["mode"] not in ("none",) and not is_ours(before["url"], http_port)
    return "replaced" if foreign else "ok"


def uninstall(backup: Path | None = None, run=subprocess.run) -> bool:
    backup = backup or backup_path()
    try:
        saved = json.loads(backup.read_text(encoding="utf-8"))
    except (OSError, ValueError):
        return False
    try:
        restore(saved, run=run)
    except (OSError, subprocess.SubprocessError):
        return False
    backup.unlink(missing_ok=True)
    return True


def state(http_port: int, run=subprocess.run) -> str:
    """'ok' (unsere PAC aktiv), 'unset', 'foreign' (andere Proxy-Einstellung) oder 'unavailable'."""
    now = current(run=run)
    if now is None:
        return "unavailable"
    if pf.IS_MACOS:
        entries = [e for e in now["services"].values() if e.get("enabled")]
        if any(is_ours(e.get("url", ""), http_port) for e in entries):
            return "ok"
        return "foreign" if entries else "unset"
    if now["mode"] == "auto" and is_ours(now["url"], http_port):
        return "ok"
    return "unset" if now["mode"] == "none" else "foreign"
