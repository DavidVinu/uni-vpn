"""Dienstdateien rendern, laden und steuern: systemd --user (Linux), launchd (macOS)."""

from __future__ import annotations

import os
import re
import subprocess
from pathlib import Path
from xml.sax.saxutils import escape

from . import platform as pf

LABEL = "de.davidvinu.uni-vpn"
UNIT = "uni-vpn"
# Diese Variablen bestimmen config_dir()/state_dir(); der Dienst bekommt sie sonst nicht.
PASSTHROUGH_ENV = ("XDG_CONFIG_HOME", "XDG_STATE_HOME")


class ServiceError(Exception):
    """Dienstdatei geschrieben, aber systemctl/launchctl ist gescheitert. files: bereits angelegte Dateien."""

    def __init__(self, message: str, files: list[Path] | None = None):
        super().__init__(message)
        self.files = files or []


def template_path() -> Path:
    if pf.IS_MACOS:
        return pf.repo_root() / "launchd" / f"{LABEL}.plist.in"
    return pf.repo_root() / "systemd" / f"{UNIT}.service.in"


def unit_target_path() -> Path:
    if pf.IS_MACOS:
        return Path.home() / "Library" / "LaunchAgents" / f"{LABEL}.plist"
    return Path.home() / ".config" / "systemd" / "user" / f"{UNIT}.service"


def render(template: str, mapping: dict[str, str]) -> str:
    text = template
    for key, value in mapping.items():
        text = text.replace(f"@{key}@", value)
    leftover = re.findall(r"@[A-Z_]+@", text)
    if leftover:
        raise ValueError(f"Platzhalter nicht ersetzt: {', '.join(leftover)}")
    return text


def _check_value(name: str, value: str) -> str:
    # Die Werte landen in systemd-Anfuehrungszeichen bzw. Environment=-Zeilen; dort waeren " und \ Escape-Zeichen.
    if any(ch in value for ch in '"\\\n'):
        raise ValueError(f"{name} darf keine Anfuehrungszeichen, Backslashes oder Zeilenumbrueche enthalten: {value!r}")
    return value


def _extra_env_block(extra_env: dict[str, str]) -> str:
    if pf.IS_MACOS:
        return "\n".join(f"    <key>{escape(key)}</key>\n    <string>{escape(value)}</string>" for key, value in extra_env.items())
    return "\n".join(f'Environment="{key}={value}"' for key, value in extra_env.items())


def render_unit(python: str, uni_vpn: str, log_dir: str, brew_prefix: str | None = None,
                extra_env: dict[str, str] | None = None) -> str:
    path_env = "/usr/bin:/bin:/usr/sbin:/sbin"
    if brew_prefix:
        path_env = f"{brew_prefix}/bin:{brew_prefix}/sbin:{path_env}"
    extra_env = {key: _check_value(key, value) for key, value in (extra_env or {}).items()}
    mapping = {"PYTHON": _check_value("python", python), "UNI_VPN": _check_value("uni-vpn", uni_vpn),
               "LOG_DIR": log_dir, "PATH": path_env, "EXTRA_ENV": _extra_env_block(extra_env)}
    template = template_path().read_text(encoding="utf-8")
    if not extra_env:
        template = template.replace("@EXTRA_ENV@\n", "")
    return render(template, mapping)


def passthrough_env(environ=os.environ) -> dict[str, str]:
    return {key: environ[key] for key in PASSTHROUGH_ENV if environ.get(key)}


def _gui_domain() -> str:
    return f"gui/{os.getuid()}"


def _run_checked(run, cmd: list[str], files: list[Path]) -> None:
    try:
        run(cmd, check=True, capture_output=True, text=True)
    except subprocess.CalledProcessError as exc:
        detail = ((exc.stderr or "") + (exc.stdout or "")).strip() or f"Exit {exc.returncode}"
        raise ServiceError(f"{' '.join(cmd)}: {detail}", files) from None
    except OSError as exc:
        raise ServiceError(f"{cmd[0]}: {exc.strerror or exc}", files) from None


def install(dry_run: bool = False, run=subprocess.run) -> list[Path]:
    target = unit_target_path()
    text = render_unit(pf.python_executable(), str(pf.bin_dir() / "uni-vpn"), str(pf.state_dir()),
                       pf.brew_prefix() if pf.IS_MACOS else None, extra_env=passthrough_env())
    if dry_run:
        print(f"-> wuerde {target} schreiben und den Dienst laden")
        return [target]
    target.parent.mkdir(parents=True, exist_ok=True)
    pf.state_dir().mkdir(parents=True, exist_ok=True, mode=0o700)
    target.write_text(text, encoding="utf-8")
    files = [target]
    if pf.IS_MACOS:
        run(["launchctl", "bootout", _gui_domain(), str(target)], capture_output=True, text=True)
        _run_checked(run, ["launchctl", "bootstrap", _gui_domain(), str(target)], files)
    else:
        _run_checked(run, ["systemctl", "--user", "daemon-reload"], files)
        _run_checked(run, ["systemctl", "--user", "enable", "--now", UNIT], files)
        # "enable --now" laesst einen laufenden Dienst stehen; nach install.sh soll der neue Code laufen.
        _run_checked(run, ["systemctl", "--user", "restart", UNIT], files)
    return files


def uninstall(run=subprocess.run) -> None:
    target = unit_target_path()
    if pf.IS_MACOS:
        run(["launchctl", "bootout", _gui_domain(), str(target)], capture_output=True, text=True)
    else:
        run(["systemctl", "--user", "disable", "--now", UNIT], capture_output=True, text=True)
    if target.exists():
        target.unlink()
    if not pf.IS_MACOS:
        run(["systemctl", "--user", "daemon-reload"], capture_output=True, text=True)


def is_active(run=subprocess.run) -> bool:
    try:
        if pf.IS_MACOS:
            result = run(["launchctl", "print", f"{_gui_domain()}/{LABEL}"], capture_output=True, text=True, timeout=5)
            return result.returncode == 0 and "state = running" in (result.stdout or "")
        result = run(["systemctl", "--user", "is-active", UNIT], capture_output=True, text=True, timeout=5)
        return (result.stdout or "").strip() == "active"
    except (OSError, subprocess.SubprocessError):
        return False


def control(action: str, run=subprocess.run) -> int:
    target = unit_target_path()
    if pf.IS_MACOS:
        commands = {
            "start": ["launchctl", "bootstrap", _gui_domain(), str(target)],
            "stop": ["launchctl", "bootout", _gui_domain(), str(target)],
            "restart": ["launchctl", "kickstart", "-k", f"{_gui_domain()}/{LABEL}"],
            "enable": ["launchctl", "bootstrap", _gui_domain(), str(target)],
            "disable": ["launchctl", "bootout", _gui_domain(), str(target)],
            "status": ["launchctl", "print", f"{_gui_domain()}/{LABEL}"],
        }
    else:
        commands = {
            "start": ["systemctl", "--user", "start", UNIT],
            "stop": ["systemctl", "--user", "stop", UNIT],
            "restart": ["systemctl", "--user", "restart", UNIT],
            "enable": ["systemctl", "--user", "enable", "--now", UNIT],
            "disable": ["systemctl", "--user", "disable", "--now", UNIT],
            "status": ["systemctl", "--user", "status", "--no-pager", UNIT],
        }
    if not target.exists():
        print(f"Dienstdatei fehlt ({target}), bitte install.sh ausfuehren")
        return 1
    result = run(commands[action], text=True)
    return result.returncode
