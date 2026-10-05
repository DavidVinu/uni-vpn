"""The app's Repair button: run the installer again, without a terminal.

It installs what is missing (openconnect, ocproxy, Wintun), registers the service again and
restarts it. Where that needs administrator rights, the operating system asks in its own
window: the UAC prompt on Windows, the password dialog of pkexec on Linux. On macOS Homebrew
installs without asking.

The installer outlives this daemon, because the service restart at its end stops the daemon:
Linux starts it as a unit of its own (systemd would stop everything in the service's cgroup),
macOS in a session of its own, Windows outside the daemon's kill-on-close job.
"""

from __future__ import annotations

import shutil
import subprocess

from . import platform as pf

CREATE_NEW_CONSOLE = 0x00000010
CREATE_BREAKAWAY_FROM_JOB = 0x01000000


def command() -> list[str]:
    root = pf.repo_root()
    if pf.IS_WINDOWS:
        return ["powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File",
                str(root / "install.ps1"), "-Repair"]
    installer = ["/bin/bash", str(root / "install.sh"), "--repair"]
    if not pf.IS_MACOS:
        systemd_run = shutil.which("systemd-run")
        if systemd_run:
            return [systemd_run, "--user", "--collect", "--quiet", "--wait", "--", *installer]
    return installer


def start(popen=subprocess.Popen) -> subprocess.Popen:
    kwargs: dict = {"stdin": subprocess.DEVNULL}
    if not pf.IS_WINDOWS:
        kwargs["start_new_session"] = True
        kwargs["stdout"] = kwargs["stderr"] = subprocess.DEVNULL
        return popen(command(), cwd=str(pf.repo_root()), **kwargs)
    # A visible window, so the progress and the administrator prompt are not hidden.
    try:
        return popen(command(), cwd=str(pf.repo_root()),
                     creationflags=CREATE_NEW_CONSOLE | CREATE_BREAKAWAY_FROM_JOB, **kwargs)
    except PermissionError:
        # An outer job (Task Scheduler) without breakaway: the elevated part that install.ps1
        # starts through UAC is not ours anyway.
        return popen(command(), cwd=str(pf.repo_root()), creationflags=CREATE_NEW_CONSOLE, **kwargs)
