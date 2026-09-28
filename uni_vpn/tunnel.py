"""openconnect-Prozess: Start, Bereitschaft, Stopp, stderr-Auswertung."""

from __future__ import annotations

import asyncio
import collections
import logging
import os
import shlex
import signal
import socket
import subprocess
import tempfile
import time
from pathlib import Path

from . import platform as pf
from .config import Config

# Gemessen am 2026-09-08 gegen vpn-ac: Der ASA lehnt ein falsches Passwort mit "Login failed."
# ab, bevor er nach dem OTP fragt. Bei falschem Einmalcode kommt erst die OTP-Abfrage
# ("Generating OATH TOTP token code"), dann "Login failed.". In beiden Faellen zeigt er das
# Formular erneut, stdin ist zu, "User input required", dann "Failed to complete
# authentication". Die Reihenfolge entscheidet also, welcher Faktor falsch war.
PASSWORD_REJECTED = "Anmeldung abgelehnt: Passwort pruefen (uni-vpn password)"
TOTP_REJECTED = (
    "Einmalcode abgelehnt: Uhrzeit des Rechners pruefen, sonst TOTP-Schluessel neu eintragen (uni-vpn totp)"
)
# Ohne vorheriges "Login failed." hat der Server etwas verlangt, das uni-vpn nicht ausfuellen kann.
AUTH_REJECTED = (
    "Anmeldung abgelehnt: Passwort pruefen (uni-vpn password). "
    "Stimmt es, hat der Server etwas verlangt, das uni-vpn nicht kennt, siehe uni-vpn log"
)
OTP_GENERATED = "Generating OATH TOTP token code"
LOGIN_FAILED = "Login failed"
TOKEN_PREFIX = "totp-"

# (Teilstring in openconnect-Ausgabe, Zustand, Meldung). Erste Uebereinstimmung gewinnt.
MARKERS: list[tuple[str, str, str]] = [
    ("Server is rejecting the soft token", "auth_failed", TOTP_REJECTED),
    ("Soft token string is invalid", "auth_failed", "TOTP-Schluessel unbrauchbar, neu eintragen (uni-vpn totp)"),
    ("User input required in non-interactive mode", "auth_failed", AUTH_REJECTED),
    ("Server asked us to run CSD", "auth_failed", "Server verlangt HostScan, uni-vpn braucht ein Update"),
    ("Cisco Secure Desktop", "auth_failed", "Server verlangt HostScan, uni-vpn braucht ein Update"),
    ("SAML", "auth_failed", "Login-Verfahren geaendert (SAML), uni-vpn braucht ein Update"),
    ("external browser", "auth_failed", "Login-Verfahren geaendert, uni-vpn braucht ein Update"),
    ("Failed to complete authentication", "auth_failed", AUTH_REJECTED),
    ("certificate", "error", "Zertifikatsproblem beim Server"),
]


def classify_line(line: str) -> tuple[str, str] | None:
    lowered = line.lower()
    for needle, state, message in MARKERS:
        if needle.lower() in lowered:
            return state, message
    return None


class Classifier:
    """Bewertet die openconnect-Ausgabe zeilenweise; das erste Urteil bleibt bestehen."""

    def __init__(self) -> None:
        self.otp_generated = False
        self.verdict: tuple[str, str] | None = None

    def feed(self, line: str) -> tuple[str, str] | None:
        if self.verdict is not None:
            return self.verdict
        if OTP_GENERATED.lower() in line.lower():
            self.otp_generated = True
            return None
        if LOGIN_FAILED.lower() in line.lower():
            self.verdict = ("auth_failed", TOTP_REJECTED if self.otp_generated else PASSWORD_REJECTED)
        else:
            self.verdict = classify_line(line)
        return self.verdict


def remove_stale_token_files(directory: Path) -> int:
    """Schluesseldateien eines abgestuerzten Daemons entfernen. Liefert die Anzahl."""
    removed = 0
    try:
        entries = list(directory.iterdir())
    except OSError:
        return 0
    for entry in entries:
        if entry.name.startswith(TOKEN_PREFIX) and entry.is_file():
            try:
                entry.unlink()
                removed += 1
            except OSError:
                pass
    return removed


def free_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def port_open(port: int) -> bool:
    with socket.socket() as sock:
        sock.settimeout(0.25)
        try:
            sock.connect(("127.0.0.1", port))
            return True
        except OSError:
            return False


class Tunnel:
    def __init__(self, cfg: Config, openconnect: str, wrapper: str, log: logging.Logger, ocproxy: str | None = None,
                 token_dir: Path | None = None):
        self.cfg = cfg
        self.openconnect = openconnect
        self.wrapper = wrapper
        self.ocproxy = ocproxy
        self.log = log
        self.token_dir = token_dir or pf.state_dir()
        self.token_file: Path | None = None
        self.port: int | None = None
        self.proc: asyncio.subprocess.Process | None = None
        self.exited = asyncio.Event()
        self.stderr_tail: collections.deque[str] = collections.deque(maxlen=20)
        self.classifier = Classifier()
        self.classification: tuple[str, str] | None = None
        self.otp_generated_at: float | None = None  # Wallclock, wenn openconnect einen Code erzeugt hat
        self.stopped_by_us = False
        self.started_at: float | None = None
        self.ready_at: float | None = None
        self._reader: asyncio.Task | None = None

    def command(self, port: int) -> list[str]:
        cmd = [
            self.openconnect,
            "--protocol=anyconnect",
            f"--useragent={self.cfg.useragent}",
            f"--user={self.cfg.user}",
            "--passwd-on-stdin",
            "--non-inter",
            "--no-dtls",
            "--force-dpd=30",
            "--reconnect-timeout=60",
            "--script-tun",
            # openconnect fuehrt den Wert per /bin/sh -c aus, der Pfad darf Leerzeichen enthalten.
            # "exec", damit dash kein sh neben ocproxy stehen laesst.
            f"--script=exec {shlex.quote(self.wrapper)} {port}",
        ]
        if self.token_file:
            # Der Schluessel geht ueber eine 0600-Datei, nie ueber die Prozessliste. openconnect
            # liest sie bei jeder Code-Erzeugung neu, sie bleibt bis zum fertigen Aufbau.
            cmd += ["--token-mode=totp", f"--token-secret=@{self.token_file}"]
        return cmd + [self.cfg.host]

    def _write_token_file(self, totp: str) -> None:
        self.token_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
        fd, name = tempfile.mkstemp(prefix=TOKEN_PREFIX, dir=self.token_dir)  # mkstemp legt 0600 an
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            handle.write(totp + "\n")
        self.token_file = Path(name)

    def _remove_token_file(self) -> None:
        if self.token_file is None:
            return
        try:
            self.token_file.unlink()
        except FileNotFoundError:
            pass
        except OSError as exc:
            self.log.warning("Schluesseldatei %s konnte nicht geloescht werden: %s", self.token_file, exc)
        self.token_file = None

    @property
    def returncode(self) -> int | None:
        return self.proc.returncode if self.proc else None

    async def start(self, password: bytes, totp: str | None = None) -> None:
        self.port = free_port()
        self.started_at = time.monotonic()
        env = dict(os.environ)
        if self.ocproxy:
            env["OCPROXY"] = self.ocproxy
        if totp:
            self._write_token_file(totp)
        try:
            self.proc = await asyncio.create_subprocess_exec(
                *self.command(self.port),
                stdin=asyncio.subprocess.PIPE,
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.STDOUT,
                start_new_session=True,
                env=env,
            )
        except OSError:
            self._remove_token_file()
            raise
        self.proc.stdin.write(password + b"\n")
        try:
            await self.proc.stdin.drain()
        except (BrokenPipeError, ConnectionResetError):
            pass
        self.proc.stdin.close()
        self._reader = asyncio.create_task(self._read_output())

    async def _read_output(self) -> None:
        assert self.proc and self.proc.stdout
        while True:
            line = await self.proc.stdout.readline()
            if not line:
                break
            text = line.decode(errors="replace").rstrip()
            self.stderr_tail.append(text)
            self.log.info("openconnect: %s", text)
            if self.classification is None:
                self.classification = self.classifier.feed(text)
            if self.classifier.otp_generated and self.otp_generated_at is None:
                self.otp_generated_at = time.time()
        await self.proc.wait()
        self._remove_token_file()
        self.log.info("openconnect beendet, Exit %s", self.proc.returncode)
        self.exited.set()

    async def wait_ready(self, timeout: float) -> bool:
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            if self.exited.is_set():
                return False
            if self.port and port_open(self.port):
                self.ready_at = time.monotonic()
                self._remove_token_file()
                return True
            await asyncio.sleep(0.25)
        return False

    async def stop(self, grace: float) -> None:
        self._remove_token_file()
        if self.proc is None or self.exited.is_set():
            return
        self.stopped_by_us = True
        try:
            self.proc.terminate()
        except ProcessLookupError:
            pass
        try:
            await asyncio.wait_for(self.exited.wait(), grace)
        except asyncio.TimeoutError:
            self.log.warning("openconnect reagiert nicht auf SIGTERM, SIGKILL")
            try:
                self.proc.kill()
            except ProcessLookupError:
                pass
            await self.exited.wait()
        for _ in range(20):
            if not (self.port and port_open(self.port)):
                return
            await asyncio.sleep(0.25)
        self._kill_wrapper()

    def _kill_wrapper(self) -> None:
        # Muster ohne fuehrenden Bindestrich und hinter "--", sonst liest pkill es als Option.
        pattern = f"ocproxy -D 127.0.0.1:{self.port} "
        self.log.warning("ocproxy haelt Port %s noch, pkill", self.port)
        result = subprocess.run(["pkill", "-9", "-U", str(os.getuid()), "-f", "--", pattern], check=False)
        self.log.warning("pkill Exit %s (0 = getroffen, 1 = kein Treffer, 2 = Syntaxfehler)", result.returncode)

    def reconnect(self) -> None:
        if self.proc and not self.exited.is_set():
            try:
                os.kill(self.proc.pid, signal.SIGUSR2)
            except ProcessLookupError:
                pass
