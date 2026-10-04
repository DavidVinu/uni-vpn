"""Render, load and control the service: systemd --user (Linux), launchd (macOS), Task Scheduler (Windows)."""

from __future__ import annotations

import json
import os
import re
import subprocess
import time
import urllib.request
from pathlib import Path
from xml.sax.saxutils import escape

from . import platform as pf

LABEL = "de.davidvinu.uni-vpn"
UNIT = "uni-vpn"
# These variables determine config_dir()/state_dir(); otherwise the service does not get them.
PASSTHROUGH_ENV = ("XDG_CONFIG_HOME", "XDG_STATE_HOME")


class ServiceError(Exception):
    """Service file written, but systemctl/launchctl failed. files: files already created."""

    def __init__(self, message: str, files: list[Path] | None = None):
        super().__init__(message)
        self.files = files or []


def template_path() -> Path:
    if pf.IS_MACOS:
        return pf.repo_root() / "launchd" / f"{LABEL}.plist.in"
    return pf.repo_root() / "systemd" / f"{UNIT}.service.in"


def unit_target_path() -> Path:
    if pf.IS_WINDOWS:
        # Task Scheduler keeps its own copy; this one records what was registered.
        return pf.config_dir() / "uni-vpn-task.xml"
    if pf.IS_MACOS:
        return Path.home() / "Library" / "LaunchAgents" / f"{LABEL}.plist"
    base = os.environ.get("XDG_CONFIG_HOME") or str(Path.home() / ".config")
    return Path(base) / "systemd" / "user" / f"{UNIT}.service"


def _schtasks(*args: str) -> list[str]:
    return ["schtasks", *args]


def _no_window() -> dict:
    return {"creationflags": 0x08000000} if pf.IS_WINDOWS else {}  # CREATE_NO_WINDOW


def _disconnect_daemon(timeout: float = 15, sleep=time.sleep) -> None:
    """Ask a running daemon to log out of the VPN. Ending the task kills it outright, and the
    session would stay open on the server until it times out."""
    from . import config

    try:
        port = config.load(config.default_path()).http_port
    except (config.ConfigError, OSError):
        port = 1081
    base = f"http://127.0.0.1:{port}"
    try:
        request = urllib.request.Request(f"{base}/api/disconnect", data=b"{}", method="POST",
                                         headers={"X-Uni-VPN": "1", "Content-Type": "application/json"})
        urllib.request.urlopen(request, timeout=3).close()
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            with urllib.request.urlopen(f"{base}/status.json", timeout=3) as response:
                if json.load(response).get("state") not in ("connected", "connecting", "disconnecting"):
                    return
            sleep(0.3)
    except (OSError, ValueError):
        pass  # not running, or an older daemon without the API


def _end_task(run, sleep=time.sleep) -> bool:
    """Log out, end the task and wait until Task Scheduler no longer counts it as running
    (with IgnoreNew a "/Run" right after "/End" is otherwise silently dropped)."""
    from .windows import TASK_NAME

    _disconnect_daemon(sleep=sleep)
    run(_schtasks("/End", "/TN", TASK_NAME), capture_output=True, text=True, **_no_window())
    for _ in range(20):
        if not is_active(run):
            return True
        sleep(0.5)
    return False


def _install_windows(target: Path, dry_run: bool, run) -> list[Path]:
    from . import windows

    python = windows.pythonw(pf.python_executable())
    for path in (python, str(pf.bin_dir() / "uni-vpn")):
        if not pf.admin_only(path):
            # Runs elevated: anything the user can change could gain administrator rights.
            print(f"   Warning: {path} is not in Program Files, use install.ps1 to install")
    text = windows.render_task(python, str(pf.bin_dir() / "uni-vpn"), windows.current_user(), str(pf.repo_root()))
    if dry_run:
        print(f"-> would register the scheduled task {windows.TASK_NAME} ({target}) and start it")
        return [target]
    target.parent.mkdir(parents=True, exist_ok=True)
    pf.state_dir().mkdir(parents=True, exist_ok=True)
    target.write_text(text, encoding="utf-16")
    files = [target]
    _run_checked(run, _schtasks("/Create", "/TN", windows.TASK_NAME, "/XML", str(target), "/F"), files)
    # Like "systemctl restart": a running old daemon makes way for the new code.
    _end_task(run)
    _run_checked(run, _schtasks("/Run", "/TN", windows.TASK_NAME), files)
    return files


def render(template: str, mapping: dict[str, str]) -> str:
    text = template
    for key, value in mapping.items():
        text = text.replace(f"@{key}@", value)
    leftover = re.findall(r"@[A-Z_]+@", text)
    if leftover:
        raise ValueError(f"Placeholders not replaced: {', '.join(leftover)}")
    return text


def _check_value(name: str, value: str) -> str:
    # The values end up in systemd quotes or Environment= lines, where " and \ would be escape characters.
    if any(ch in value for ch in '"\\\n'):
        raise ValueError(f"{name} must not contain quotes, backslashes or line breaks: {value!r}")
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
    if pf.IS_WINDOWS:
        return _install_windows(target, dry_run, run)
    text = render_unit(pf.python_executable(), str(pf.bin_dir() / "uni-vpn"), str(pf.state_dir()),
                       pf.brew_prefix() if pf.IS_MACOS else None, extra_env=passthrough_env())
    if dry_run:
        print(f"-> would write {target} and load the service")
        return [target]
    target.parent.mkdir(parents=True, exist_ok=True)
    pf.state_dir().mkdir(parents=True, exist_ok=True, mode=0o700)
    target.write_text(text, encoding="utf-8")
    files = [target]
    if pf.IS_MACOS:
        run(["launchctl", "bootout", _gui_domain(), str(target)], capture_output=True, text=True)
        # Right after bootout the old job may still be going away ("Bootstrap failed: 5").
        for attempt in range(5):
            try:
                _run_checked(run, ["launchctl", "bootstrap", _gui_domain(), str(target)], files)
                break
            except ServiceError:
                if attempt == 4:
                    raise
                time.sleep(1)
    else:
        _run_checked(run, ["systemctl", "--user", "daemon-reload"], files)
        _run_checked(run, ["systemctl", "--user", "enable", "--now", UNIT], files)
        # "enable --now" leaves a running service alone; after install.sh the new code should run.
        _run_checked(run, ["systemctl", "--user", "restart", UNIT], files)
    return files


def uninstall(run=subprocess.run) -> bool:
    """False if the service could not be removed."""
    target = unit_target_path()
    if pf.IS_WINDOWS:
        from .windows import TASK_NAME

        _end_task(run)
        result = run(_schtasks("/Delete", "/TN", TASK_NAME, "/F"), capture_output=True, text=True, **_no_window())
        missing = run(_schtasks("/Query", "/TN", TASK_NAME), capture_output=True, text=True,
                      **_no_window()).returncode != 0
        target.unlink(missing_ok=True)
        return result.returncode == 0 or missing
    if pf.IS_MACOS:
        run(["launchctl", "bootout", _gui_domain(), str(target)], capture_output=True, text=True)
    else:
        run(["systemctl", "--user", "disable", "--now", UNIT], capture_output=True, text=True)
    if target.exists():
        target.unlink()
    if not pf.IS_MACOS:
        run(["systemctl", "--user", "daemon-reload"], capture_output=True, text=True)
    return True


def is_active(run=subprocess.run) -> bool:
    try:
        if pf.IS_WINDOWS:
            # The state name from PowerShell is not localized, unlike the schtasks output.
            from .windows import TASK_NAME

            result = run(["powershell", "-NoProfile", "-NonInteractive", "-Command",
                          f"(Get-ScheduledTask -TaskName '{TASK_NAME}').State"],
                         capture_output=True, text=True, timeout=15, **_no_window())
            return (result.stdout or "").strip() == "Running"
        if pf.IS_MACOS:
            result = run(["launchctl", "print", f"{_gui_domain()}/{LABEL}"], capture_output=True, text=True, timeout=5)
            return result.returncode == 0 and "state = running" in (result.stdout or "")
        result = run(["systemctl", "--user", "is-active", UNIT], capture_output=True, text=True, timeout=5)
        return (result.stdout or "").strip() == "active"
    except (OSError, subprocess.SubprocessError):
        return False


def control(action: str, run=subprocess.run) -> int:
    target = unit_target_path()
    if pf.IS_WINDOWS:
        from .windows import TASK_NAME

        if not target.exists():
            print(f"Service file is missing ({target}), please run install.ps1")
            return 1
        start = _schtasks("/Run", "/TN", TASK_NAME)
        if action in ("stop", "restart", "disable") and not _end_task(run) and action != "disable":
            print("The service did not stop (administrator rights needed?)")
            return 1
        steps = {
            "start": [start], "stop": [], "restart": [start],
            "enable": [_schtasks("/Change", "/TN", TASK_NAME, "/ENABLE"), start],
            "disable": [_schtasks("/Change", "/TN", TASK_NAME, "/DISABLE")],
            "status": [_schtasks("/Query", "/TN", TASK_NAME, "/V", "/FO", "LIST")],
        }[action]
        rc = 0
        for cmd in steps:
            rc = rc or run(cmd, text=True).returncode
        return rc
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
        print(f"Service file is missing ({target}), please run install.sh")
        return 1
    result = run(commands[action], text=True)
    return result.returncode
