"""Removing the app removes uni-vpn. Dragging "Uni VPN" to the Trash (macOS) or removing the
package in the App Center (Linux) runs nothing of ours, and each user's service would keep
running from the user's own copy. So the service watches the installer's folder and, once it
is gone, restores the proxy setting, deletes its files and ends itself. Role model: macOS apps
that clean up after being dragged to the Trash (for example Logi Options+ and Zoom's helper);
the keyring entries stay, like the data of any app moved to the Trash."""

from __future__ import annotations

import argparse
import os
import subprocess
from pathlib import Path

from . import platform as pf

# In the user's copy of the program: the installer folder it was copied from (packaging/uni-vpn-open).
MARKER = ".package"


def package_dir(root: Path) -> Path | None:
    try:
        text = (root / MARKER).read_text(encoding="utf-8").strip()
    except (OSError, UnicodeDecodeError):
        return None
    return Path(text) if text else None


def package_gone(root: Path) -> bool:
    """True when this copy came from an installer whose files are no longer there: neither the
    Python program nor the Go core (a package of the Go core alone replaces this one)."""
    package = package_dir(root)
    return (package is not None and not (package / "app" / "uni_vpn" / "__init__.py").is_file()
            and not (package / "app" / "bin" / "uni-vpn-core").is_file())


def _forget_service(run=subprocess.run) -> bool:
    """Unregisters the service without stopping it: stopping ends this process."""
    from . import service

    if not pf.IS_MACOS:
        run(["systemctl", "--user", "disable", service.UNIT], capture_output=True, text=True)
    service.unit_target_path().unlink(missing_ok=True)
    if not pf.IS_MACOS:
        run(["systemctl", "--user", "daemon-reload"], capture_output=True, text=True)
    return True


def _end_service(run=subprocess.run) -> None:
    from . import service

    if pf.IS_MACOS:
        run(["launchctl", "bootout", f"gui/{os.getuid()}/{service.LABEL}"], capture_output=True, text=True)
    else:
        run(["systemctl", "--user", "stop", service.UNIT], capture_output=True, text=True)


def remove(run=subprocess.run, uninstall=None) -> int:
    """Everything "uni-vpn uninstall" does, keeping the keyring entries; the service ends last."""
    from . import setup

    uninstall = uninstall or setup.uninstall
    code = uninstall(argparse.Namespace(dry_run=False, yes=False), input_fn=lambda _prompt: "n",
                     service_uninstall=lambda: _forget_service(run))
    _end_service(run)
    return code
