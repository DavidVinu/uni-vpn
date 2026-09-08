"""Zustandsautomat: Bedarf, Aufbau, Leerlauf, Fehlerklassen, Resume."""

from __future__ import annotations

import asyncio
import logging
import random
import ssl
import time
from enum import Enum
from pathlib import Path
from typing import Awaitable, Callable

from . import PROTOCOL, __version__, credentials
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
OTP_STEP = 30  # Sekunden je Einmalcode (RFC 6238, wie openconnect)
BLOCKED_MESSAGE = "Cisco Secure Client ist verbunden, uni-vpn pausiert"


async def default_probe(host: str, timeout: float) -> bool:
    """TLS-Handshake auf host:443 mit Systemtruststore. False bei Captive Portal oder ohne Netz."""
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
                 token_dir: Path | None = None):
        self.cfg = cfg
        self.log = log or logging.getLogger("uni-vpn")
        self.password_getter = password_getter or (lambda: credentials.get_password(cfg.user, cfg.keyring_timeout))
        self.password_setter = password_setter or (lambda pw: credentials.store_password(cfg.user, pw))
        self.totp_getter = totp_getter or (lambda: credentials.get_totp(cfg.user, cfg.keyring_timeout))
        self.totp_setter = totp_setter or (lambda token: credentials.store_totp(cfg.user, token))
        self.token_dir = token_dir or pf.state_dir()
        self.probe = probe or (lambda: default_probe(cfg.host, cfg.probe_timeout))
        self.cisco_check = cisco_check or pf.cisco_connected
        self.tunnel_factory = tunnel_factory or self._make_tunnel
        self.config_error = config_error
        self.log_tail = log_tail if log_tail is not None else []
        self.wrapper = wrapper or str(pf.bin_dir() / "uni-vpn-ocproxy")

        self.state = State.idle
        self.message = "Nicht verbunden"
        self.since = time.time()
        self.last_error: dict | None = None
        self.failures = 0
        self.explicit = False
        self.demand_until = 0.0
        self.last_activity = time.monotonic()
        self.connect_count = 0
        # Der Server nimmt jeden Einmalcode nur einmal an (gemessen 2026-09-08: Neuaufbau 3 s
        # nach dem Login -> "Login failed"). Merken, in welchem Fenster zuletzt einer verbraucht wurde.
        self.last_otp_step: int | None = None
        # Vom Ticker gesetzt, wenn Cisco bei stehendem Tunnel verbindet; die Aufbau-Schleife
        # meldet dann `blocked` statt "Getrennt".
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
            self._set(State.error, f"Konfiguration fehlerhaft: {config_error}")

    # --- Hilfen -----------------------------------------------------------

    def _make_tunnel(self) -> Tunnel:
        openconnect = pf.find_binary("openconnect", self.cfg.openconnect)
        if not openconnect:
            raise FileNotFoundError("openconnect nicht gefunden, bitte install.sh ausfuehren")
        ocproxy = pf.find_binary("ocproxy", self.cfg.ocproxy)
        if not ocproxy:
            raise FileNotFoundError("ocproxy nicht gefunden, bitte install.sh ausfuehren")
        return Tunnel(self.cfg, openconnect, self.wrapper, self.log, ocproxy=ocproxy, token_dir=self.token_dir)

    def _set(self, state: State, message: str) -> None:
        if state != self.state or message != self.message:
            self.log.info("Zustand %s -> %s: %s", self.state.value, state.value, message)
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
            "log_tail": list(self.log_tail)[-30:],
        }

    # --- Lebenszyklus -----------------------------------------------------

    async def run(self) -> None:
        from .httpapi import HttpApi

        stale = remove_stale_token_files(self.token_dir)
        if stale:
            self.log.warning("%d alte Schluesseldatei(en) entfernt", stale)
        self.http = HttpApi(self, "127.0.0.1", self.cfg.http_port, self.log)
        try:
            await self.http.start()
        except OSError as exc:
            self.log.error("Statusport %s nicht verfuegbar: %s", self.cfg.http_port, exc)
            self.http = None
        try:
            await self.forwarder.start()
        except OSError as exc:
            self.log.error("SOCKS-Port %s nicht verfuegbar: %s", self.cfg.socks_port, exc)
            self._set(State.error, f"Port {self.cfg.socks_port} ist belegt, uni-vpn doctor ausfuehren")
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

    # --- Bedarf -----------------------------------------------------------

    async def acquire(self) -> int | None:
        """Vom Forwarder pro Browserverbindung aufgerufen. Liefert den ocproxy-Port oder None."""
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
            changed = self._changed
            try:
                await asyncio.wait_for(changed.wait(), remaining)
            except asyncio.TimeoutError:
                return None

    def _ensure_loop(self) -> None:
        if self._loop_task is None or self._loop_task.done():
            self._loop_task = asyncio.create_task(self._connect_loop())

    async def _sleep(self, seconds: float) -> None:
        self._wake.clear()
        try:
            await asyncio.wait_for(self._wake.wait(), seconds)
        except asyncio.TimeoutError:
            pass

    async def request_connect(self) -> None:
        if self.config_error:
            return
        self.explicit = True
        self.failures = 0
        if self.state in FINAL_STATES or self.state in RETRY_STATES:
            self._set(State.idle, "Nicht verbunden")
        self.note_activity()
        self._ensure_loop()
        self._wake.set()

    async def request_disconnect(self) -> None:
        self.explicit = False
        self.demand_until = 0.0
        self._wake.set()
        if self.tunnel:
            self._set(State.disconnecting, "Wird getrennt")
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
            self._set(State.idle, "Nicht verbunden")

    async def set_password(self, password: str) -> None:
        await asyncio.get_running_loop().run_in_executor(None, self.password_setter, password)
        self.log.info("Passwort im Keyring abgelegt")
        await self.request_connect()

    async def set_totp(self, token: str) -> None:
        await asyncio.get_running_loop().run_in_executor(None, self.totp_setter, token)
        self.log.info("TOTP-Schluessel im Keyring abgelegt")
        await self.request_connect()

    # --- Aufbau -----------------------------------------------------------

    async def _connect_loop(self) -> None:
        cfg = self.cfg
        while self.has_demand():
            if self.cisco_check():
                self._set(State.blocked, BLOCKED_MESSAGE)
                await self._sleep(cfg.retry_interval)
                continue
            if not await self.probe():
                self._set(State.offline, "Kein Netz oder Captive Portal")
                await self._sleep(cfg.retry_interval)
                continue
            try:
                password = await self.password_getter()
            except credentials.PasswordMissing:
                self._final(State.keyring, "Kein Passwort hinterlegt: uni-vpn password")
                return
            except credentials.KeyringLocked:
                self._final(State.keyring, "Schluesselbund gesperrt, bitte entsperren und erneut verbinden")
                return
            except credentials.KeyringError as exc:
                self._final(State.keyring, str(exc))
                return
            try:
                totp = (await self.totp_getter()).decode("ascii").strip()
            except credentials.TotpMissing:
                self._final(State.keyring, "Kein TOTP-Schluessel hinterlegt: uni-vpn totp")
                return
            except credentials.KeyringLocked:
                self._final(State.keyring, "Schluesselbund gesperrt, bitte entsperren und erneut verbinden")
                return
            except (credentials.KeyringError, UnicodeDecodeError) as exc:
                self._final(State.keyring, f"TOTP-Schluessel unlesbar: {exc}")
                return

            wait = self._otp_wait()
            if wait:
                self._set(State.connecting, "Warte auf den naechsten Einmalcode (bis zu 30 s)")
                await self._sleep(wait)
                if not self.has_demand():
                    del password, totp
                    continue
            self._set(State.connecting, "Verbindung wird aufgebaut")
            try:
                tunnel = self.tunnel_factory()
                self.tunnel = tunnel
                await tunnel.start(password, totp)
            except OSError as exc:
                self.tunnel = None
                self._final(State.error, f"openconnect konnte nicht gestartet werden: {exc}")
                return
            finally:
                del password, totp
            self.connect_count += 1

            ready = await tunnel.wait_ready(cfg.ready_timeout)
            if tunnel.classifier.otp_generated:
                self.last_otp_step = int(time.time() // OTP_STEP)
            if not ready:
                if tunnel.stopped_by_us:
                    self.tunnel = None
                    self._after_stop()
                    continue  # ein zwischenzeitliches request_connect greift ueber has_demand()
                if not tunnel.exited.is_set():
                    await tunnel.stop(cfg.stop_grace)
                    state, message = State.error, "Verbindungsaufbau dauerte zu lange"
                elif tunnel.classification:
                    state, message = State(tunnel.classification[0]), tunnel.classification[1]
                elif tunnel.returncode == 1:
                    state, message = State.auth_failed, "Anmeldung fehlgeschlagen (openconnect Exit 1), siehe uni-vpn log"
                else:
                    state, message = State.error, f"openconnect endete mit Exit {tunnel.returncode}"
                self.tunnel = None
                if state == State.auth_failed:
                    self._final(state, message)
                    return
                self._set(State.error, message)
                if not await self._backoff():
                    break
                continue

            self.failures = 0
            self.log.info("Tunnel bereit nach %.1f s", (tunnel.ready_at or 0) - (tunnel.started_at or 0))
            self._set(State.connected, "Verbunden")
            # Der Wunsch "jetzt verbinden" ist erfuellt. Ab hier zaehlt nur noch echte Nutzung,
            # den Rest regelt der Leerlauf-Timer.
            self.explicit = False
            self.note_activity()
            await tunnel.exited.wait()
            self.tunnel = None
            await self.forwarder.close_all()
            if tunnel.stopped_by_us:
                self._after_stop()
                continue  # ein zwischenzeitliches request_connect greift ueber has_demand()
            message = tunnel.classification[1] if tunnel.classification else f"Tunnel abgebrochen (Exit {tunnel.returncode})"
            self.last_error = {"message": message, "at": time.time()}
            if not self.has_demand():
                self._set(State.idle, f"Nicht verbunden ({message})")
                return
            self._set(State.error, message)
            if not await self._backoff():
                break
        # `blocked` bleibt stehen, bis der Ticker Cisco als getrennt sieht: so erklaert das Popup
        # weiter, warum nichts geht.
        if self.state in (State.offline, State.error, State.connecting):
            self._set(State.idle, "Nicht verbunden")

    def _after_stop(self) -> None:
        if self.paused_by_cisco:
            self.paused_by_cisco = False
            self._set(State.blocked, BLOCKED_MESSAGE)
        else:
            self._set(State.idle, "Getrennt")

    def _otp_wait(self, now: float | None = None) -> float:
        """Sekunden bis zum naechsten Einmalcode-Fenster, 0 wenn der aktuelle Code noch unverbraucht ist."""
        now = time.time() if now is None else now
        if self.last_otp_step is None or int(now // OTP_STEP) != self.last_otp_step:
            return 0
        return OTP_STEP - now % OTP_STEP + 0.5

    async def _backoff(self) -> bool:
        delays = self.cfg.backoff
        delay = delays[min(self.failures, len(delays) - 1)] * random.uniform(0.8, 1.2)
        self.failures += 1
        self.log.info("Neuer Versuch in %.1f s", delay)
        await self._sleep(delay)
        return self.has_demand()

    # --- Leerlauf und Resume ---------------------------------------------

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
                # Im Executor: "vpn state" auf macOS braucht 2 s und darf den Forwarder nicht anhalten.
                cisco = await asyncio.get_running_loop().run_in_executor(None, self.cisco_check)
                tunnel = self.tunnel
                if cisco and self.state == State.connected and tunnel:
                    self.log.info("Cisco Secure Client hat sich verbunden, Tunnel wird abgebaut")
                    self.paused_by_cisco = True
                    self._set(State.blocked, BLOCKED_MESSAGE)
                    await self.forwarder.close_all()
                    await tunnel.stop(self.cfg.stop_grace)
                elif not cisco and self.state == State.blocked and (self._loop_task is None or self._loop_task.done()):
                    self._set(State.idle, "Nicht verbunden")
            if jump > 30:
                self.log.info("Resume erkannt (Uhr sprang um %.0f s)", jump)
                tunnel = self.tunnel
                if tunnel:
                    # Sauber beenden statt SIGUSR2: der Zustandsautomat laeuft dann ueber
                    # disconnecting -> idle und baut bei Bedarf neu auf.
                    self.log.info("Resume erkannt, Tunnel wird neu aufgebaut")
                    self._set(State.disconnecting, "Resume, Tunnel wird neu aufgebaut")
                    await self.forwarder.close_all()
                    await tunnel.stop(self.cfg.stop_grace)
            if self.state == State.connected and self.tunnel and mono - self.last_activity > self.cfg.idle_minutes * 60:
                self.log.info("Leerlauf seit %.0f s, Tunnel wird abgebaut", mono - self.last_activity)
                self.explicit = False
                self.demand_until = 0.0
                tunnel = self.tunnel
                self._set(State.disconnecting, "Leerlauf, wird getrennt")
                await self.forwarder.close_all()
                await tunnel.stop(self.cfg.stop_grace)
