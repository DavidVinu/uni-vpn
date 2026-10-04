"""openconnect process: start, readiness, stop, stderr classification."""

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

# Measured on 2026-09-08 against vpn-ac: the ASA rejects a wrong password with "Login failed."
# before it asks for the OTP. With a wrong one-time code the OTP prompt comes first
# ("Generating OATH TOTP token code"), then "Login failed.". In both cases it shows the form
# again, stdin is closed, "User input required", then "Failed to complete authentication".
# So the order decides which factor was wrong.
PASSWORD_REJECTED = "Login rejected: check your password (uni-vpn password)"
TOTP_REJECTED = (
    "One-time code rejected: check the computer's clock, otherwise re-enter the TOTP secret (uni-vpn totp)"
)
# Without a preceding "Login failed." the server asked for something uni-vpn cannot fill in.
AUTH_REJECTED = (
    "Login rejected: check your password (uni-vpn password). "
    "If it is correct, the server asked for something uni-vpn does not know, see uni-vpn log"
)
OTP_GENERATED = "Generating OATH TOTP token code"
LOGIN_FAILED = "Login failed"
TOKEN_PREFIX = "totp-"

# (substring of the openconnect output, state, message). The first match wins.
MARKERS: list[tuple[str, str, str]] = [
    ("Server is rejecting the soft token", "auth_failed", TOTP_REJECTED),
    ("Soft token string is invalid", "auth_failed", "TOTP secret unusable, enter it again (uni-vpn totp)"),
    ("User input required in non-interactive mode", "auth_failed", AUTH_REJECTED),
    ("Server asked us to run CSD", "auth_failed", "Server requires HostScan, uni-vpn needs an update"),
    ("Cisco Secure Desktop", "auth_failed", "Server requires HostScan, uni-vpn needs an update"),
    ("SAML", "auth_failed", "Login method changed (SAML), uni-vpn needs an update"),
    ("external browser", "auth_failed", "Login method changed, uni-vpn needs an update"),
    ("Failed to complete authentication", "auth_failed", AUTH_REJECTED),
    ("certificate", "error", "Certificate problem on the server"),
]


def classify_line(line: str) -> tuple[str, str] | None:
    lowered = line.lower()
    for needle, state, message in MARKERS:
        if needle.lower() in lowered:
            return state, message
    return None


class Classifier:
    """Classifies the openconnect output line by line; the first verdict sticks."""

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
    """Remove secret files left behind by a crashed daemon. Returns how many."""
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


class PasswordEncodingError(OSError):
    """The password cannot be passed to openconnect on this system."""


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
        self.otp_generated_at: float | None = None  # wall clock time when openconnect generated a code
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
            # openconnect runs the value via /bin/sh -c, so the path may contain spaces.
            # "exec" so that dash does not leave an sh running next to ocproxy.
            f"--script=exec {shlex.quote(self.wrapper)} {port}",
        ]
        if self.token_file:
            # The secret is passed via a 0600 file, never via the process list. openconnect
            # rereads it for every code it generates, so it stays until the tunnel is up.
            cmd += ["--token-mode=totp", f"--token-secret=@{self.token_file}"]
        return cmd + [self.cfg.host]

    def _write_token_file(self, totp: str) -> None:
        self.token_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
        fd, name = tempfile.mkstemp(prefix=TOKEN_PREFIX, dir=self.token_dir)  # mkstemp creates it as 0600
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
            self.log.warning("Could not delete secret file %s: %s", self.token_file, exc)
        self.token_file = None

    def _env(self) -> dict[str, str]:
        env = dict(os.environ)
        if self.ocproxy:
            env["OCPROXY"] = self.ocproxy
        return env

    def _spawn_kwargs(self) -> dict:
        return {"start_new_session": True}

    @property
    def returncode(self) -> int | None:
        return self.proc.returncode if self.proc else None

    def _password_bytes(self, password: bytes) -> bytes:
        """The password as openconnect reads it from stdin."""
        return password

    async def start(self, password: bytes, totp: str | None = None) -> None:
        data = self._password_bytes(password) + b"\n"
        self.port = free_port()
        self.started_at = time.monotonic()
        env = self._env()
        if totp:
            self._write_token_file(totp)
        try:
            self.proc = await asyncio.create_subprocess_exec(
                *self.command(self.port),
                stdin=asyncio.subprocess.PIPE,
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.STDOUT,
                env=env,
                **self._spawn_kwargs(),
            )
        except OSError:
            self._remove_token_file()
            raise
        self.proc.stdin.write(data)
        try:
            await self.proc.stdin.drain()
        except (BrokenPipeError, ConnectionResetError):
            pass
        self.proc.stdin.close()
        self._reader = asyncio.create_task(self._read_output())
        if self.stopped_by_us:  # stop() came while the process was being started
            self._remove_token_file()
            try:
                self.proc.kill()
            except ProcessLookupError:
                pass

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
        self.log.info("openconnect exited with code %s", self.proc.returncode)
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
        if self.proc is None:
            self.stopped_by_us = True  # still starting: the attempt does not count
            return
        if self.exited.is_set():
            return
        self.stopped_by_us = True
        try:
            self.proc.terminate()
        except ProcessLookupError:
            pass
        try:
            await asyncio.wait_for(self.exited.wait(), grace)
        except asyncio.TimeoutError:
            self.log.warning("openconnect does not respond to SIGTERM, sending SIGKILL")
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
        # Pattern without a leading hyphen and after "--", otherwise pkill reads it as an option.
        pattern = f"ocproxy -D 127.0.0.1:{self.port} "
        self.log.warning("ocproxy still holds port %s, running pkill", self.port)
        result = subprocess.run(["pkill", "-9", "-U", str(os.getuid()), "-f", "--", pattern], check=False)
        self.log.warning("pkill exit code %s (0 = matched, 1 = no match, 2 = syntax error)", result.returncode)

    def reconnect(self) -> None:
        if self.proc and not self.exited.is_set() and hasattr(signal, "SIGUSR2"):
            try:
                os.kill(self.proc.pid, signal.SIGUSR2)
            except ProcessLookupError:
                pass
