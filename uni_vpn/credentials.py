"""Password and TOTP secret in the OS keyring: GNOME keyring/KDE (secret-tool) or macOS keychain (security).

Two separate entries, each with its own service name: secret-tool searches by attribute sets, so an
entry with an extra attribute would also match the search for the password.
"""

from __future__ import annotations

import asyncio
import subprocess

from . import platform as pf

SERVICE = "uni-vpn"
LABEL = "Uni VPN"
SERVICES = {"password": SERVICE, "totp": "uni-vpn-totp"}
LABELS = {"password": LABEL, "totp": "Uni VPN (second factor)"}


class SecretMissing(Exception):
    pass


class PasswordMissing(SecretMissing):
    pass


class TotpMissing(SecretMissing):
    pass


class KeyringLocked(Exception):
    pass


class KeyringError(Exception):
    pass


def _secret_tool() -> str:
    tool = pf.find_binary("secret-tool")
    if not tool:
        raise KeyringError("secret-tool is missing (package libsecret-tools)")
    return tool


def lookup_command(user: str, kind: str = "password") -> list[str]:
    service = SERVICES[kind]
    if pf.IS_MACOS:
        return [pf.SECURITY, "find-generic-password", "-s", service, "-a", user, "-w"]
    return [_secret_tool(), "lookup", "service", service, "user", user]


MISSING = {"password": (PasswordMissing, "No password saved"),
           "totp": (TotpMissing, "No TOTP secret saved")}


def _windows_target(user: str, kind: str) -> str:
    from .windows import credential_target

    return credential_target(SERVICES[kind], user)


async def _get_secret_windows(user: str, kind: str, timeout: float) -> bytes:
    from .windows import CredentialError, cred_read

    loop = asyncio.get_running_loop()
    try:
        out = await asyncio.wait_for(loop.run_in_executor(None, cred_read, _windows_target(user, kind)), timeout)
    except asyncio.TimeoutError:
        raise KeyringLocked("Credential Manager did not answer") from None
    except CredentialError as exc:
        raise KeyringError(str(exc)) from None
    if not out:
        error, message = MISSING[kind]
        raise error(message)
    return out


async def get_secret(user: str, kind: str, timeout: float, command: list[str] | None = None) -> bytes:
    if pf.IS_WINDOWS and command is None:
        return await _get_secret_windows(user, kind, timeout)
    cmd = command or lookup_command(user, kind)
    proc = await asyncio.create_subprocess_exec(
        *cmd, stdin=asyncio.subprocess.DEVNULL,
        stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE,
    )
    try:
        out, _err = await asyncio.wait_for(proc.communicate(), timeout)
    except asyncio.TimeoutError:
        proc.kill()
        await proc.wait()
        raise KeyringLocked("Keyring locked or access denied") from None
    if proc.returncode != 0 or not out:
        error, message = MISSING[kind]
        raise error(message)
    if pf.IS_MACOS and out.endswith(b"\n"):
        out = out[:-1]
    return out


async def get_password(user: str, timeout: float, command: list[str] | None = None) -> bytes:
    return await get_secret(user, "password", timeout, command)


async def get_totp(user: str, timeout: float, command: list[str] | None = None) -> bytes:
    return await get_secret(user, "totp", timeout, command)


def _quote_security(value: str) -> str:
    return value.replace("\\", "\\\\").replace('"', '\\"')


# A keyring dialog nobody sees (service without GUI, CI) must not block anything forever.
COMMAND_TIMEOUT = 60


def store_secret(user: str, kind: str, value: str, run=subprocess.run) -> None:
    # Last line of defence: `security -i` reads commands line by line, openconnect
    # --passwd-on-stdin exactly one line. The CLI and HTTP API reject this earlier.
    if "\n" in value or "\r" in value:
        raise KeyringError("Password must not contain a line break")
    service, label = SERVICES[kind], LABELS[kind]
    if pf.IS_WINDOWS:
        from .windows import CredentialError, cred_write

        try:
            cred_write(_windows_target(user, kind), user, value.encode("utf-8"), comment=label)
        except CredentialError as exc:
            raise KeyringError(f"{label} could not be saved: {exc}") from None
        return
    try:
        if pf.IS_MACOS:
            # Delete first, then create anew: "-U" (update) triggers a confirmation dialog
            # on macOS that waits forever without a GUI (measured in CI).
            delete_secret(user, kind, run=run)
            script = (
                f'add-generic-password -a "{_quote_security(user)}" -s "{service}" '
                f'-T {pf.SECURITY} -w "{_quote_security(value)}"\n'
            )
            result = run([pf.SECURITY, "-i"], input=script.encode(), capture_output=True, timeout=COMMAND_TIMEOUT)
        else:
            cmd = [_secret_tool(), "store", "--label", label, "service", service, "user", user]
            result = run(cmd, input=value.encode(), capture_output=True, timeout=COMMAND_TIMEOUT)
    except subprocess.TimeoutExpired:
        raise KeyringError("No answer from the keyring (locked, or a dialog is waiting)") from None
    if result.returncode != 0:
        detail = (result.stderr or b"").decode(errors="replace").strip()
        raise KeyringError(f"{label} could not be saved: {detail or result.returncode}")


def store_password(user: str, password: str, run=subprocess.run) -> None:
    store_secret(user, "password", password, run=run)


def store_totp(user: str, token: str, run=subprocess.run) -> None:
    store_secret(user, "totp", token, run=run)


def delete_secret(user: str, kind: str, run=subprocess.run) -> bool:
    service = SERVICES[kind]
    if pf.IS_WINDOWS:
        from .windows import cred_delete

        return cred_delete(_windows_target(user, kind))
    if pf.IS_MACOS:
        cmd = [pf.SECURITY, "delete-generic-password", "-s", service, "-a", user]
    else:
        cmd = [_secret_tool(), "clear", "service", service, "user", user]
    try:
        result = run(cmd, capture_output=True, timeout=COMMAND_TIMEOUT)
    except subprocess.TimeoutExpired:
        return False
    return result.returncode == 0


def delete_password(user: str, run=subprocess.run) -> bool:
    return delete_secret(user, "password", run=run)


def delete_totp(user: str, run=subprocess.run) -> bool:
    return delete_secret(user, "totp", run=run)
