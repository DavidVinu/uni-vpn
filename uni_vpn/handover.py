"""Hands bin/uni-vpn over to the Go core when bin/uni-vpn-core exists next to it.

Service registrations written by the Python setup run "python bin/uni-vpn daemon"; with the
hand-over they start the Go core unchanged, so switching an installation means putting one file
in place (docs/go-switch.md). Stdlib only: bin/uni-vpn runs this before anything else."""

from __future__ import annotations

import os
import subprocess
import sys

DISABLE_ENV = "UNI_VPN_PYTHON"
IS_WINDOWS = sys.platform == "win32"
CREATE_NO_WINDOW = 0x08000000


def core_path(root: str) -> str:
    return os.path.join(root, "bin", "uni-vpn-core.exe" if IS_WINDOWS else "uni-vpn-core")


def target(root: str, environ=os.environ) -> str | None:
    if environ.get(DISABLE_ENV):
        return None
    path = core_path(root)
    return path if os.path.isfile(path) and (IS_WINDOWS or os.access(path, os.X_OK)) else None


def handover(root: str, argv: list[str], *, environ=os.environ, execv=os.execv, call=subprocess.call,
             windows: bool = IS_WINDOWS, has_console: bool | None = None) -> int | None:
    """Runs the Go core in place of this process. None: no Go core here, carry on with Python."""
    core = target(root, environ)
    if not core:
        return None
    command = [core, *argv]
    if not windows:
        execv(core, command)  # same process id: systemd and launchd keep watching the same service
        return 1  # only reached with a replaced execv
    # No exec on Windows: run it as a child and pass its exit code on (75 asks the supervisor in
    # cli.restart_daemon to start the service again). Under pythonw there is no console, so the
    # child gets a hidden one instead of a window of its own.
    if has_console is None:
        has_console = sys.stdout is not None
    try:
        return call(command, **({} if has_console else {"creationflags": CREATE_NO_WINDOW}))
    except KeyboardInterrupt:
        return 130
