"""Passwort im OS-Keyring: GNOME-Keyring/KDE (secret-tool) oder macOS-Schluesselbund (security)."""

from __future__ import annotations

import asyncio
import subprocess

from . import platform as pf

SERVICE = "uni-vpn"
LABEL = "Uni VPN"


class PasswordMissing(Exception):
    pass


class KeyringLocked(Exception):
    pass


class KeyringError(Exception):
    pass


def _secret_tool() -> str:
    tool = pf.find_binary("secret-tool")
    if not tool:
        raise KeyringError("secret-tool fehlt (Paket libsecret-tools)")
    return tool


def lookup_command(user: str) -> list[str]:
    if pf.IS_MACOS:
        return [pf.SECURITY, "find-generic-password", "-s", SERVICE, "-a", user, "-w"]
    return [_secret_tool(), "lookup", "service", SERVICE, "user", user]


async def get_password(user: str, timeout: float, command: list[str] | None = None) -> bytes:
    cmd = command or lookup_command(user)
    proc = await asyncio.create_subprocess_exec(
        *cmd, stdin=asyncio.subprocess.DEVNULL,
        stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE,
    )
    try:
        out, _err = await asyncio.wait_for(proc.communicate(), timeout)
    except asyncio.TimeoutError:
        proc.kill()
        await proc.wait()
        raise KeyringLocked("Schluesselbund gesperrt oder Zugriff verweigert") from None
    if proc.returncode != 0 or not out:
        raise PasswordMissing("Kein Passwort hinterlegt")
    if pf.IS_MACOS and out.endswith(b"\n"):
        out = out[:-1]
    return out


def _quote_security(value: str) -> str:
    return value.replace("\\", "\\\\").replace('"', '\\"')


# Ein Keyring-Dialog, den niemand sieht (Dienst ohne GUI, CI), darf nichts ewig blockieren.
COMMAND_TIMEOUT = 60


def store_password(user: str, password: str, run=subprocess.run) -> None:
    try:
        if pf.IS_MACOS:
            script = (
                f'add-generic-password -a "{_quote_security(user)}" -s "{SERVICE}" '
                f'-T {pf.SECURITY} -U -w "{_quote_security(password)}"\n'
            )
            result = run([pf.SECURITY, "-i"], input=script.encode(), capture_output=True, timeout=COMMAND_TIMEOUT)
        else:
            cmd = [_secret_tool(), "store", "--label", LABEL, "service", SERVICE, "user", user]
            result = run(cmd, input=password.encode(), capture_output=True, timeout=COMMAND_TIMEOUT)
    except subprocess.TimeoutExpired:
        raise KeyringError("Keine Antwort vom Schluesselbund (gesperrt oder Dialog wartet)") from None
    if result.returncode != 0:
        detail = (result.stderr or b"").decode(errors="replace").strip()
        raise KeyringError(f"Passwort konnte nicht abgelegt werden: {detail or result.returncode}")


def delete_password(user: str, run=subprocess.run) -> bool:
    if pf.IS_MACOS:
        cmd = [pf.SECURITY, "delete-generic-password", "-s", SERVICE, "-a", user]
    else:
        cmd = [_secret_tool(), "clear", "service", SERVICE, "user", user]
    try:
        result = run(cmd, capture_output=True, timeout=COMMAND_TIMEOUT)
    except subprocess.TimeoutExpired:
        return False
    return result.returncode == 0
