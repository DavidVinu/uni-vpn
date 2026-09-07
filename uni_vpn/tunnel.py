"""openconnect-Prozess: Start, Bereitschaft, Stopp, stderr-Auswertung."""

from __future__ import annotations

import asyncio
import collections
import logging
import os
import signal
import socket
import subprocess
import time

from .config import Config

# (Teilstring in openconnect-Ausgabe, Zustand, Meldung). Erste Uebereinstimmung gewinnt.
MARKERS: list[tuple[str, str, str]] = [
    ("User input required in non-interactive mode", "auth_failed",
     "Server verlangt eine weitere Eingabe (OTP?), Login nicht automatisierbar"),
    ("Server asked us to run CSD", "auth_failed", "Server verlangt HostScan, uni-vpn braucht ein Update"),
    ("Cisco Secure Desktop", "auth_failed", "Server verlangt HostScan, uni-vpn braucht ein Update"),
    ("SAML", "auth_failed", "Login-Verfahren geaendert (SAML), uni-vpn braucht ein Update"),
    ("external browser", "auth_failed", "Login-Verfahren geaendert, uni-vpn braucht ein Update"),
    ("Failed to complete authentication", "auth_failed", "Anmeldung abgelehnt: Passwort pruefen (uni-vpn password)"),
    ("certificate", "error", "Zertifikatsproblem beim Server"),
]


def classify_line(line: str) -> tuple[str, str] | None:
    lowered = line.lower()
    for needle, state, message in MARKERS:
        if needle.lower() in lowered:
            return state, message
    return None


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
    def __init__(self, cfg: Config, openconnect: str, wrapper: str, log: logging.Logger, ocproxy: str | None = None):
        self.cfg = cfg
        self.openconnect = openconnect
        self.wrapper = wrapper
        self.ocproxy = ocproxy
        self.log = log
        self.port: int | None = None
        self.proc: asyncio.subprocess.Process | None = None
        self.exited = asyncio.Event()
        self.stderr_tail: collections.deque[str] = collections.deque(maxlen=20)
        self.classification: tuple[str, str] | None = None
        self.stopped_by_us = False
        self.started_at: float | None = None
        self.ready_at: float | None = None
        self._reader: asyncio.Task | None = None

    def command(self, port: int) -> list[str]:
        return [
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
            f"--script={self.wrapper} {port}",
            self.cfg.host,
        ]

    @property
    def returncode(self) -> int | None:
        return self.proc.returncode if self.proc else None

    async def start(self, password: bytes) -> None:
        self.port = free_port()
        self.started_at = time.monotonic()
        env = dict(os.environ)
        if self.ocproxy:
            env["OCPROXY"] = self.ocproxy
        self.proc = await asyncio.create_subprocess_exec(
            *self.command(self.port),
            stdin=asyncio.subprocess.PIPE,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.STDOUT,
            start_new_session=True,
            env=env,
        )
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
                self.classification = classify_line(text)
        await self.proc.wait()
        self.log.info("openconnect beendet, Exit %s", self.proc.returncode)
        self.exited.set()

    async def wait_ready(self, timeout: float) -> bool:
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            if self.exited.is_set():
                return False
            if self.port and port_open(self.port):
                self.ready_at = time.monotonic()
                return True
            await asyncio.sleep(0.25)
        return False

    async def stop(self, grace: float) -> None:
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
        pattern = f"-D 127.0.0.1:{self.port} "
        self.log.warning("ocproxy haelt Port %s noch, pkill", self.port)
        subprocess.run(["pkill", "-9", "-f", pattern], check=False)

    def reconnect(self) -> None:
        if self.proc and not self.exited.is_set():
            try:
                os.kill(self.proc.pid, signal.SIGUSR2)
            except ProcessLookupError:
                pass
