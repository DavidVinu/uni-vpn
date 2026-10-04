"""Register the proxy rule with the system: GNOME (gsettings) on Linux, networksetup on macOS.

Chrome and Firefox read the system's "automatic proxy configuration" setting and fetch the
PAC file from the daemon. The previous state is backed up and restored on removal.
"""

from __future__ import annotations

import json
import os
import re
import subprocess
import time
from pathlib import Path

from . import platform as pf

SCHEMA = "org.gnome.system.proxy"
KDE_GROUP = "Proxy Settings"
KDE_KEYS = ("ProxyType", "Proxy Config Script")
KDE_PAC = "2"
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
        if line and not line.startswith("*"):  # * = disabled service
            names.append(line)
    return names


def _kde_tools() -> tuple[str, str] | None:
    """kreadconfig/kwriteconfig of Plasma 6 or 5, only on a KDE desktop."""
    if "KDE" not in os.environ.get("XDG_CURRENT_DESKTOP", "").upper():
        return None
    for version in ("6", "5"):
        read, write = pf.find_binary(f"kreadconfig{version}"), pf.find_binary(f"kwriteconfig{version}")
        if read and write:
            return read, write
    return None


def _kde_get(run) -> dict | None:
    tools = _kde_tools()
    if not tools:
        return None
    values = {}
    for key in KDE_KEYS:
        try:
            result = _run([tools[0], "--file", "kioslaverc", "--group", KDE_GROUP, "--key", key], run)
        except (OSError, subprocess.SubprocessError):
            return None
        values[key] = (result.stdout or "").strip() if result.returncode == 0 else ""
    return {"type": values["ProxyType"] or "0", "url": values["Proxy Config Script"]}


def _kde_set(proxy_type: str, url: str, run) -> None:
    tools = _kde_tools()
    if not tools:
        return
    _run([tools[1], "--file", "kioslaverc", "--group", KDE_GROUP, "--key", "Proxy Config Script", url], run)
    _run([tools[1], "--file", "kioslaverc", "--group", KDE_GROUP, "--key", "ProxyType", proxy_type], run)
    # Running KDE programs re-read kioslaverc on this signal; Chrome watches the file itself.
    try:
        _run(["dbus-send", "--type=signal", "/KIO/Scheduler", "org.kde.KIO.Scheduler.reparseSlaveConfiguration",
              "string:"], run)
    except (OSError, subprocess.SubprocessError):
        pass


def current(run=subprocess.run) -> dict | None:
    """Current setting, or None when the system has no known proxy management."""
    if pf.IS_WINDOWS:
        from .windows import proxy_get

        try:
            return {"windows": proxy_get()}
        except OSError:
            return None
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
    result: dict = {}
    mode = _gsettings_get("mode", run)
    if mode is not None:
        result = {"mode": mode, "url": _gsettings_get("autoconfig-url", run) or ""}
    kde = _kde_get(run)
    if kde is not None:
        result["kde"] = kde
    return result or None


def apply(url: str, run=subprocess.run) -> None:
    if pf.IS_WINDOWS:
        from .windows import proxy_set

        proxy_set(url)
        return
    if pf.IS_MACOS:
        for name in _services(run):
            _run(["networksetup", "-setautoproxyurl", name, url], run)
        return
    if _gsettings_get("mode", run) is not None:
        _run(["gsettings", "set", SCHEMA, "autoconfig-url", url], run)
        _run(["gsettings", "set", SCHEMA, "mode", "auto"], run)
    _kde_set(KDE_PAC, url, run)


def restore(saved: dict, run=subprocess.run) -> None:
    if pf.IS_WINDOWS:
        from .windows import proxy_set

        proxy_set((saved.get("windows") or {}).get("url") or "")
        return
    if pf.IS_MACOS:
        for name, entry in (saved.get("services") or {}).items():
            if entry.get("enabled") and entry.get("url"):
                _run(["networksetup", "-setautoproxyurl", name, entry["url"]], run)
            else:
                _run(["networksetup", "-setautoproxystate", name, "off"], run)
        return
    if "mode" in saved:
        _run(["gsettings", "set", SCHEMA, "mode", saved.get("mode") or "none"], run)
        _run(["gsettings", "set", SCHEMA, "autoconfig-url", saved.get("url") or ""], run)
    if "kde" in saved:
        _kde_set(str(saved["kde"].get("type") or "0"), saved["kde"].get("url") or "", run)


def refresh(http_port: int, run=subprocess.run) -> None:
    """Set a new URL version: GNOME, KDE and Windows announce the change, Chrome and Firefox reload the PAC."""
    if pf.IS_MACOS:
        return  # networksetup requires admin rights; the user restarts the browser
    url = pac_url(http_port, int(time.time()))
    try:
        if pf.IS_WINDOWS:
            from .windows import proxy_get, proxy_set

            if is_ours(proxy_get()["url"], http_port):
                proxy_set(url)
            return
        if _gsettings_get("mode", run) is not None:
            _run(["gsettings", "set", SCHEMA, "autoconfig-url", url], run)
        kde = _kde_get(run)
        if kde is not None and is_ours(kde["url"], http_port):
            _kde_set(KDE_PAC, url, run)
    except (OSError, subprocess.SubprocessError):
        pass


def _is_foreign(before: dict, http_port: int) -> bool:
    if pf.IS_WINDOWS:
        url = (before.get("windows") or {}).get("url", "")
        return bool(url) and not is_ours(url, http_port)
    if pf.IS_MACOS:
        return any(e.get("enabled") and not is_ours(e.get("url", ""), http_port) for e in before["services"].values())
    foreign = "mode" in before and before["mode"] not in ("none",) and not is_ours(before["url"], http_port)
    kde = before.get("kde")
    if kde and kde.get("type") not in ("", "0") and not is_ours(kde.get("url", ""), http_port):
        foreign = True
    return foreign


def install(http_port: int, backup: Path | None = None, run=subprocess.run) -> str:
    """Returns 'ok', 'replaced' (a foreign setting was replaced) or 'unavailable'."""
    backup = backup or backup_path()
    before = current(run=run)
    if before is None:
        return "unavailable"
    if not backup.exists():
        backup.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        backup.write_text(json.dumps(before), encoding="utf-8")
    apply(pac_url(http_port), run=run)
    return "replaced" if _is_foreign(before, http_port) else "ok"


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
    """'ok' (our PAC active), 'unset', 'foreign' (another proxy setting) or 'unavailable'."""
    now = current(run=run)
    if now is None:
        return "unavailable"
    if pf.IS_WINDOWS:
        url = now["windows"]["url"]
        return "ok" if is_ours(url, http_port) else ("foreign" if url else "unset")
    if pf.IS_MACOS:
        entries = [e for e in now["services"].values() if e.get("enabled")]
        if any(is_ours(e.get("url", ""), http_port) for e in entries):
            return "ok"
        return "foreign" if entries else "unset"
    kde = now.get("kde")
    if kde is not None:
        # On a KDE desktop Chrome and Firefox follow kioslaverc, not GNOME.
        if kde["type"] == KDE_PAC and is_ours(kde["url"], http_port):
            return "ok"
        return "unset" if kde["type"] in ("", "0") else "foreign"
    if now["mode"] == "auto" and is_ours(now["url"], http_port):
        return "ok"
    return "unset" if now["mode"] == "none" else "foreign"
