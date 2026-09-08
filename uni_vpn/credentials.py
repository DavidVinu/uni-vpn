"""Passwort und TOTP-Schluessel im OS-Keyring: GNOME-Keyring/KDE (secret-tool) oder macOS-Schluesselbund (security).

Zwei getrennte Eintraege mit eigenem Dienstnamen: secret-tool sucht nach Attributmengen, ein
Eintrag mit Zusatzattribut wuerde die Suche nach dem Passwort ebenfalls treffen.
"""

from __future__ import annotations

import asyncio
import subprocess

from . import platform as pf

SERVICE = "uni-vpn"
LABEL = "Uni VPN"
SERVICES = {"password": SERVICE, "totp": "uni-vpn-totp"}
LABELS = {"password": LABEL, "totp": "Uni VPN (zweiter Faktor)"}


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
        raise KeyringError("secret-tool fehlt (Paket libsecret-tools)")
    return tool


def lookup_command(user: str, kind: str = "password") -> list[str]:
    service = SERVICES[kind]
    if pf.IS_MACOS:
        return [pf.SECURITY, "find-generic-password", "-s", service, "-a", user, "-w"]
    return [_secret_tool(), "lookup", "service", service, "user", user]


MISSING = {"password": (PasswordMissing, "Kein Passwort hinterlegt"),
           "totp": (TotpMissing, "Kein TOTP-Schluessel hinterlegt")}


async def get_secret(user: str, kind: str, timeout: float, command: list[str] | None = None) -> bytes:
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
        raise KeyringLocked("Schluesselbund gesperrt oder Zugriff verweigert") from None
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


# Ein Keyring-Dialog, den niemand sieht (Dienst ohne GUI, CI), darf nichts ewig blockieren.
COMMAND_TIMEOUT = 60


def store_secret(user: str, kind: str, value: str, run=subprocess.run) -> None:
    # Letzte Verteidigung: `security -i` liest zeilenweise Kommandos, openconnect
    # --passwd-on-stdin genau eine Zeile. CLI und HTTP-API weisen das vorher ab.
    if "\n" in value or "\r" in value:
        raise KeyringError("Passwort darf keinen Zeilenumbruch enthalten")
    service, label = SERVICES[kind], LABELS[kind]
    try:
        if pf.IS_MACOS:
            # Erst loeschen, dann neu anlegen: "-U" (Update) loest auf macOS einen
            # Bestaetigungsdialog aus, der ohne GUI ewig wartet (in CI gemessen).
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
        raise KeyringError("Keine Antwort vom Schluesselbund (gesperrt oder Dialog wartet)") from None
    if result.returncode != 0:
        detail = (result.stderr or b"").decode(errors="replace").strip()
        raise KeyringError(f"{label} konnte nicht abgelegt werden: {detail or result.returncode}")


def store_password(user: str, password: str, run=subprocess.run) -> None:
    store_secret(user, "password", password, run=run)


def store_totp(user: str, token: str, run=subprocess.run) -> None:
    store_secret(user, "totp", token, run=run)


def delete_secret(user: str, kind: str, run=subprocess.run) -> bool:
    service = SERVICES[kind]
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
