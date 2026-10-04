"""State machine: demand, connecting, idle, error classes, resume."""

from __future__ import annotations

import asyncio
import logging
import random
import ssl
import time
from enum import Enum
from pathlib import Path
from typing import Awaitable, Callable

from . import PROTOCOL, __version__, credentials, pac, sysproxy
from . import platform as pf
from .config import Config
from .forwarder import Forwarder
from .tunnel import Tunnel, remove_stale_token_files


class State(str, Enum):
    idle = "idle"
    offline = "offline"
    blocked = "blocked"
    connecting = "connecting"
    connected = "connected"
    disconnecting = "disconnecting"
    auth_failed = "auth_failed"
    keyring = "keyring"
    error = "error"


FINAL_STATES = {State.auth_failed, State.keyring}
RETRY_STATES = {State.offline, State.blocked, State.error}
OTP_STEP = 30  # seconds per one-time code (RFC 6238, same as openconnect)
BLOCKED_MESSAGE = "Cisco Secure Client is connected, uni-vpn is paused"


async def wait_event(event: asyncio.Event, timeout: float) -> bool:
    """Wait for an event; True if it was set. Unlike asyncio.wait_for on Python 3.11, a cancel
    that races with the event being set is never swallowed (gh-86296)."""
    waiter = asyncio.ensure_future(event.wait())
    try:
        done, _pending = await asyncio.wait({waiter}, timeout=max(timeout, 0))
    finally:
        if not waiter.done():
            waiter.cancel()
    return bool(done)


async def default_probe(host: str, timeout: float) -> bool:
    """TLS handshake with host:443 using the system trust store. False behind a captive portal or without network."""
    context = ssl.create_default_context()
    try:
        _reader, writer = await asyncio.wait_for(
            asyncio.open_connection(host, 443, ssl=context, server_hostname=host), timeout)
    except (OSError, ssl.SSLError, asyncio.TimeoutError):
        return False
    writer.close()
    return True


class Daemon:
    def __init__(self, cfg: Config, log: logging.Logger | None = None, *,
                 password_getter: Callable[[], Awaitable[bytes]] | None = None,
                 password_setter: Callable[[str], None] | None = None,
                 totp_getter: Callable[[], Awaitable[bytes]] | None = None,
                 totp_setter: Callable[[str], None] | None = None,
                 probe: Callable[[], Awaitable[bool]] | None = None,
                 cisco_check: Callable[[], bool] | None = None,
                 tunnel_factory: Callable[[], Tunnel] | None = None,
                 config_error: str | None = None,
                 log_tail=None,
                 wrapper: str | None = None,
                 token_dir: Path | None = None,
                 domains_path: Path | None = None,
                 proxy_refresh: Callable[[int], None] | None = None):
        self.cfg = cfg
        self.log = log or logging.getLogger("uni-vpn")
        self.password_getter = password_getter or (lambda: credentials.get_password(cfg.user, cfg.keyring_timeout))
        self.password_setter = password_setter or (lambda pw: credentials.store_password(cfg.user, pw))
        self.totp_getter = totp_getter or (lambda: credentials.get_totp(cfg.user, cfg.keyring_timeout))
        self.totp_setter = totp_setter or (lambda token: credentials.store_totp(cfg.user, token))
        self.token_dir = token_dir or pf.state_dir()
        self.domains_path = domains_path or pac.domains_path()
        self.proxy_refresh = proxy_refresh or sysproxy.refresh
        self.probe = probe or (lambda: default_probe(cfg.host, cfg.probe_timeout))
        self.cisco_check = cisco_check or pf.cisco_connected
        self.tunnel_factory = tunnel_factory or self._make_tunnel
        self.config_error = config_error
        self.log_tail = log_tail if log_tail is not None else []
        self.wrapper = wrapper or str(pf.bin_dir() / "uni-vpn-ocproxy")

        self.state = State.idle
        self.message = "Not connected"
        self.since = time.time()
        self.last_error: dict | None = None
        self.failures = 0
        self.explicit = False
        self.demand_until = 0.0
        self.last_activity = time.monotonic()
        self.connect_count = 0
        # The server accepts each one-time code only once (measured 2026-09-08: reconnecting 3 s
        # after the login -> "Login failed"). Remember the window in which one was last used.
        self.last_otp_step: int | None = None
        # Set by the ticker when Cisco connects while the tunnel is up; the connect loop
        # then reports `blocked` instead of "Disconnected".
        self.paused_by_cisco = False
        self.tunnel: Tunnel | None = None
        self.http = None
        self._loop_task: asyncio.Task | None = None
        self._changed = asyncio.Event()
        self._wake = asyncio.Event()
        self._stop = asyncio.Event()
        self.started = asyncio.Event()
        self.forwarder = Forwarder("127.0.0.1", cfg.socks_port, self.acquire, self.note_activity,
                                   cfg.halfclose_grace, self.log)
        if config_error:
            self._set(State.error, f"Configuration error: {config_error}")

    # --- Helpers ----------------------------------------------------------

    def _make_tunnel(self) -> Tunnel:
        openconnect = pf.find_binary("openconnect", self.cfg.openconnect)
        if not openconnect:
            raise FileNotFoundError("openconnect not found, please run install.sh")
        ocproxy = pf.find_binary("ocproxy", self.cfg.ocproxy)
        if not ocproxy:
            raise FileNotFoundError("ocproxy not found, please run install.sh")
        return Tunnel(self.cfg, openconnect, self.wrapper, self.log, ocproxy=ocproxy, token_dir=self.token_dir)

    def _set(self, state: State, message: str) -> None:
        if state != self.state or message != self.message:
            self.log.info("State %s -> %s: %s", self.state.value, state.value, message)
        self.state = state
        self.message = message
        self.since = time.time()
        if state in (State.error, State.auth_failed, State.keyring):
            self.last_error = {"message": message, "at": self.since}
        previous = self._changed
        self._changed = asyncio.Event()
        previous.set()

    def _final(self, state: State, message: str) -> None:
        self.explicit = False
        self._set(state, message)

    def note_activity(self) -> None:
        now = time.monotonic()
        self.last_activity = now
        self.demand_until = max(self.demand_until, now + self.cfg.demand_window)

    def has_demand(self) -> bool:
        return self.explicit or self.forwarder.active > 0 or time.monotonic() < self.demand_until

    def status(self) -> dict:
        return {
            "protocol": PROTOCOL,
            "version": __version__,
            "state": self.state.value,
            "message": self.message,
            "since": self.since,
            "host": self.cfg.host,
            "user": self.cfg.user,
            "socks_port": self.cfg.socks_port,
            "http_port": self.cfg.http_port,
            "idle_minutes": self.cfg.idle_minutes,
            "active_connections": self.forwarder.active,
            "bytes_in": self.forwarder.bytes_in,
            "bytes_out": self.forwarder.bytes_out,
            "connects": self.connect_count,
            "last_error": self.last_error,
            "domains": pac.read_domains(self.domains_path),
            "pac_url": sysproxy.pac_url(self.cfg.http_port),
            "pac_refresh": "manual" if pf.IS_MACOS else "auto",
            "log_tail": list(self.log_tail)[-30:],
        }

    def pac(self) -> str:
        return pac.build_pac(pac.read_domains(self.domains_path), self.cfg.socks_port)

    async def set_domains(self, text: str) -> list[str]:
        domains, errors = pac.parse_domain_list(text)
        if errors:
            raise ValueError("\n".join(errors))
        pac.write_domains(self.domains_path, domains)
        self.log.info("Domain list saved: %s", ", ".join(domains) or "(empty)")
        await asyncio.get_running_loop().run_in_executor(None, self.proxy_refresh, self.cfg.http_port)
        return domains

    # --- Lifecycle --------------------------------------------------------

    async def run(self) -> None:
        from .httpapi import HttpApi

        stale = remove_stale_token_files(self.token_dir)
        if stale:
            self.log.warning("Removed %d stale secret file(s)", stale)
        self.http = HttpApi(self, "127.0.0.1", self.cfg.http_port, self.log)
        try:
            await self.http.start()
        except OSError as exc:
            self.log.error("Status port %s not available: %s", self.cfg.http_port, exc)
            self.http = None
        try:
            await self.forwarder.start()
        except OSError as exc:
            self.log.error("SOCKS port %s not available: %s", self.cfg.socks_port, exc)
            self._set(State.error, f"Port {self.cfg.socks_port} is in use, run uni-vpn doctor")
        ticker = asyncio.create_task(self._ticker())
        self.started.set()
        try:
            await self._stop.wait()
        finally:
            ticker.cancel()
            if self._loop_task and not self._loop_task.done():
                self._loop_task.cancel()
            if self.tunnel:
                await self.tunnel.stop(self.cfg.stop_grace)
            await self.forwarder.stop()
            if self.http:
                await self.http.stop()

    def stop(self) -> None:
        self._stop.set()

    # --- Demand -----------------------------------------------------------

    async def acquire(self) -> int | None:
        """Called by the forwarder for each browser connection. Returns the ocproxy port or None."""
        self.note_activity()
        if self.config_error:
            return None
        deadline = time.monotonic() + self.cfg.client_wait
        while True:
            state = self.state
            if state == State.connected and self.tunnel:
                return self.tunnel.port
            if state in FINAL_STATES:
                return None
            if state in RETRY_STATES:
                self._ensure_loop()
                return None
            if state == State.idle:
                self._ensure_loop()
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                return None
            if not await wait_event(self._changed, remaining):
                return None

    def _ensure_loop(self) -> None:
        if self._loop_task is None or self._loop_task.done():
            self._loop_task = asyncio.create_task(self._connect_loop())

    async def _sleep(self, seconds: float) -> None:
        self._wake.clear()
        await wait_event(self._wake, seconds)

    async def request_connect(self) -> None:
        if self.config_error:
            return
        self.explicit = True
        self.failures = 0
        if self.state in FINAL_STATES or self.state in RETRY_STATES:
            self._set(State.idle, "Not connected")
        self.note_activity()
        self._ensure_loop()
        self._wake.set()

    async def request_disconnect(self) -> None:
        self.explicit = False
        self.demand_until = 0.0
        self._wake.set()
        if self.tunnel:
            self._set(State.disconnecting, "Disconnecting")
            await self.forwarder.close_all()
            await self.tunnel.stop(self.cfg.stop_grace)
            return
        if self._loop_task and not self._loop_task.done():
            self._loop_task.cancel()
            try:
                await self._loop_task
            except asyncio.CancelledError:
                pass
        if self.state not in FINAL_STATES:
            self._set(State.idle, "Not connected")

    async def set_password(self, password: str) -> None:
        await asyncio.get_running_loop().run_in_executor(None, self.password_setter, password)
        self.log.info("Password stored in the keyring")
        await self.request_connect()

    async def set_totp(self, token: str) -> None:
        await asyncio.get_running_loop().run_in_executor(None, self.totp_setter, token)
        self.log.info("TOTP secret stored in the keyring")
        await self.request_connect()

    # --- Connecting -------------------------------------------------------

    async def _connect_loop(self) -> None:
        cfg = self.cfg
        while self.has_demand():
            if self.cisco_check():
                self._set(State.blocked, BLOCKED_MESSAGE)
                await self._sleep(cfg.retry_interval)
                continue
            if not await self.probe():
                self._set(State.offline, "No network or captive portal")
                await self._sleep(cfg.retry_interval)
                continue
            try:
                password = await self.password_getter()
            except credentials.PasswordMissing:
                self._final(State.keyring, "No password stored: uni-vpn password")
                return
            except credentials.KeyringLocked:
                self._final(State.keyring, "Keyring locked, please unlock it and connect again")
                return
            except credentials.KeyringError as exc:
                self._final(State.keyring, str(exc))
                return
            try:
                totp = (await self.totp_getter()).decode("ascii").strip()
            except credentials.TotpMissing:
                self._final(State.keyring, "No TOTP secret stored: uni-vpn totp")
                return
            except credentials.KeyringLocked:
                self._final(State.keyring, "Keyring locked, please unlock it and connect again")
                return
            except (credentials.KeyringError, UnicodeDecodeError) as exc:
                self._final(State.keyring, f"TOTP secret unreadable: {exc}")
                return

            wait = self._otp_wait()
            if wait:
                self._set(State.connecting, "Waiting for the next one-time code (up to 30 s)")
                await self._sleep(wait)
                if not self.has_demand():
                    del password, totp
                    continue
            self._set(State.connecting, "Connecting")
            try:
                tunnel = self.tunnel_factory()
                self.tunnel = tunnel
                await tunnel.start(password, totp)
            except OSError as exc:
                self.tunnel = None
                self._final(State.error, f"Could not start openconnect: {exc}")
                return
            finally:
                del password, totp
            self.connect_count += 1
            # openconnect generates the code right after starting; record the window now, because
            # the output reader can lag behind the port check (macOS CI).
            self.last_otp_step = int(time.time() // OTP_STEP)

            ready = await tunnel.wait_ready(cfg.ready_timeout)
            if tunnel.otp_generated_at:
                self.last_otp_step = int(tunnel.otp_generated_at // OTP_STEP)
            if not ready:
                if tunnel.stopped_by_us:
                    self.tunnel = None
                    self._after_stop()
                    continue  # a request_connect in the meantime takes effect via has_demand()
                if not tunnel.exited.is_set():
                    await tunnel.stop(cfg.stop_grace)
                    state, message = State.error, "Connecting took too long"
                elif tunnel.classification:
                    state, message = State(tunnel.classification[0]), tunnel.classification[1]
                elif tunnel.returncode == 1:
                    state, message = State.auth_failed, "Login failed (openconnect exit code 1), see uni-vpn log"
                else:
                    state, message = State.error, f"openconnect exited with code {tunnel.returncode}"
                self.tunnel = None
                if state == State.auth_failed:
                    self._final(state, message)
                    return
                self._set(State.error, message)
                if not await self._backoff():
                    break
                continue

            self.failures = 0
            self.log.info("Tunnel ready after %.1f s", (tunnel.ready_at or 0) - (tunnel.started_at or 0))
            self._set(State.connected, "Connected")
            # The "connect now" request is fulfilled. From here on only real use counts,
            # the idle timer handles the rest.
            self.explicit = False
            self.note_activity()
            await tunnel.exited.wait()
            self.tunnel = None
            await self.forwarder.close_all()
            if tunnel.stopped_by_us:
                self._after_stop()
                continue  # a request_connect in the meantime takes effect via has_demand()
            message = tunnel.classification[1] if tunnel.classification else f"Tunnel dropped (exit code {tunnel.returncode})"
            self.last_error = {"message": message, "at": time.time()}
            if not self.has_demand():
                self._set(State.idle, f"Not connected ({message})")
                return
            self._set(State.error, message)
            if not await self._backoff():
                break
        # `blocked` stays until the ticker sees Cisco disconnected, so the popup keeps
        # explaining why nothing works.
        if self.state in (State.offline, State.error, State.connecting):
            self._set(State.idle, "Not connected")

    def _after_stop(self) -> None:
        if self.paused_by_cisco:
            self.paused_by_cisco = False
            self._set(State.blocked, BLOCKED_MESSAGE)
        else:
            self._set(State.idle, "Disconnected")

    def _otp_wait(self, now: float | None = None) -> float:
        """Seconds until the next one-time code window, 0 if the current code is still unused."""
        now = time.time() if now is None else now
        if self.last_otp_step is None or int(now // OTP_STEP) != self.last_otp_step:
            return 0
        return OTP_STEP - now % OTP_STEP + 0.5

    async def _backoff(self) -> bool:
        delays = self.cfg.backoff
        delay = delays[min(self.failures, len(delays) - 1)] * random.uniform(0.8, 1.2)
        self.failures += 1
        self.log.info("Retrying in %.1f s", delay)
        await self._sleep(delay)
        return self.has_demand()

    # --- Idle and resume ------------------------------------------------

    async def _ticker(self) -> None:
        last_mono, last_wall = time.monotonic(), time.time()
        last_cisco = time.monotonic()
        while True:
            await asyncio.sleep(self.cfg.tick)
            mono, wall = time.monotonic(), time.time()
            jump = (wall - last_wall) - (mono - last_mono)
            last_mono, last_wall = mono, wall
            if mono - last_cisco >= self.cfg.retry_interval and self.state in (State.connected, State.blocked):
                last_cisco = mono
                # In the executor: "vpn state" takes 2 s on macOS and must not stall the forwarder.
                cisco = await asyncio.get_running_loop().run_in_executor(None, self.cisco_check)
                tunnel = self.tunnel
                if cisco and self.state == State.connected and tunnel:
                    self.log.info("Cisco Secure Client connected, tearing down the tunnel")
                    self.paused_by_cisco = True
                    self._set(State.blocked, BLOCKED_MESSAGE)
                    await self.forwarder.close_all()
                    await tunnel.stop(self.cfg.stop_grace)
                elif not cisco and self.state == State.blocked and (self._loop_task is None or self._loop_task.done()):
                    self._set(State.idle, "Not connected")
            if jump > 30:
                self.log.info("Resume detected (clock jumped by %.0f s)", jump)
                tunnel = self.tunnel
                if tunnel:
                    # Stop cleanly instead of SIGUSR2: the state machine then goes through
                    # disconnecting -> idle and reconnects if there is demand.
                    self.log.info("Resume detected, reconnecting the tunnel")
                    self._set(State.disconnecting, "Resume, reconnecting the tunnel")
                    await self.forwarder.close_all()
                    await tunnel.stop(self.cfg.stop_grace)
            if self.state == State.connected and self.tunnel and mono - self.last_activity > self.cfg.idle_minutes * 60:
                self.log.info("Idle for %.0f s, tearing down the tunnel", mono - self.last_activity)
                self.explicit = False
                self.demand_until = 0.0
                tunnel = self.tunnel
                self._set(State.disconnecting, "Idle, disconnecting")
                await self.forwarder.close_all()
                await tunnel.stop(self.cfg.stop_grace)
