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
from . import config as config_mod
from . import universities as unis
from .config import Config
from .forwarder import Forwarder
from . import i18n
from .i18n import t
from .tunnel import SAML_REQUIRED, PasswordEncodingError, Tunnel, remove_stale_token_files
from .updater import UpdateError, Updater


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
BLOCKED_MESSAGE = t("state.blocked")


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
                 config_path: Path | None = None,
                 needs_setup: bool = False,
                 log_tail=None,
                 wrapper: str | None = None,
                 token_dir: Path | None = None,
                 domains_path: Path | None = None,
                 proxy_refresh: Callable[[int], None] | None = None,
                 updater: Updater | None = None,
                 update_first_check: float = 600.0,
                 update_interval: float = 5 * 3600.0,
                 update_jitter: float = 1800.0):
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
        self.config_path = config_path or cfg.path
        # No config.toml yet: the status page shows the setup assistant instead of an error.
        self.needs_setup = needs_setup
        self._disconnects = 0
        self.log_tail = log_tail if log_tail is not None else []
        self.wrapper = wrapper or str(pf.bin_dir() / "uni-vpn-ocproxy")
        # Like Chrome: check a while after the start, then every few hours.
        self.updater = updater or Updater()
        self.update_first_check = update_first_check
        self.update_interval = update_interval
        self.update_jitter = update_jitter
        self.commit: str | None = None
        # Set when the program files were replaced: the caller starts the new code.
        self.restart_requested = False

        self.state = State.idle
        self.message = t("state.not_connected")
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
        # Counts password/TOTP changes: an attempt started with older secrets does not count.
        self._secrets_gen = 0
        self.http = None
        self._loop_task: asyncio.Task | None = None
        self._changed = asyncio.Event()
        self._wake = asyncio.Event()
        self._stop = asyncio.Event()
        self._update_now = asyncio.Event()
        self.started = asyncio.Event()
        self.forwarder = Forwarder("127.0.0.1", cfg.socks_port, self.acquire, self.note_activity,
                                   cfg.halfclose_grace, self.log)
        if needs_setup:
            self.message = t("state.setup_needed")
        elif config_error:
            self._set(State.error, t("state.config_error", error=config_error))

    # --- Helpers ----------------------------------------------------------

    def _make_tunnel(self) -> Tunnel:
        openconnect = pf.find_binary("openconnect", self.cfg.openconnect)
        if not openconnect:
            raise FileNotFoundError("openconnect not found, please run the installer")
        if pf.IS_WINDOWS:
            from .wintunnel import WindowsTunnel

            return WindowsTunnel(self.cfg, openconnect, str(pf.bin_dir() / "uni-vpn-vpnc.js"), self.log,
                                 token_dir=self.token_dir)
        ocproxy = pf.find_binary("ocproxy", self.cfg.ocproxy)
        if not ocproxy:
            raise FileNotFoundError("ocproxy not found, please run the installer")
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
            "commit": self.commit,
            "auto_update": self.cfg.auto_update,
            "language": self.cfg.language,
            "update_pending": self.updater.pending,
            "state": self.state.value,
            "message": self.message,
            # Key and arguments of the message, so the app can show it in the user's language.
            "message_t": i18n.as_json(self.message),
            "since": self.since,
            "host": self.cfg.host,
            "user": self.cfg.user,
            "university": self.cfg.university,
            "university_name": self.cfg.university_name,
            "mfa": self.cfg.mfa,
            "mfa_portal_url": self.cfg.mfa_portal_url,
            "mfa_steps": list(self.cfg.mfa_steps),
            "socks_port": self.cfg.socks_port,
            "http_port": self.cfg.http_port,
            "idle_minutes": self.cfg.idle_minutes,
            "active_connections": self.forwarder.active,
            "bytes_in": self.forwarder.bytes_in,
            "bytes_out": self.forwarder.bytes_out,
            "connects": self.connect_count,
            "last_error": self.last_error,
            "domains": pac.read_domains(self.domains_path, self.cfg.default_domains),
            "pac_url": sysproxy.pac_url(self.cfg.http_port),
            "pac_refresh": "manual" if pf.IS_MACOS else "auto",
            "log_tail": list(self.log_tail)[-30:],
            "setup_needed": self.needs_setup,
            # A connect loop is running (also while retrying in offline, blocked or error).
            "busy": self._loop_task is not None and not self._loop_task.done(),
            "error_kind": self.error_kind(),
            "platform": "windows" if pf.IS_WINDOWS else "macos" if pf.IS_MACOS else "linux",
            "elevated": self._elevated(),
        }

    def _elevated(self) -> bool | None:
        if not pf.IS_WINDOWS:
            return None
        from .windows import is_admin

        return is_admin()

    def error_kind(self) -> str | None:
        """Which factor the current error is about, so the page can offer the right fix."""
        if self.state not in (State.auth_failed, State.keyring):
            return None
        text = self.message.lower()
        if "one-time code" in text or "totp" in text:
            return "totp"
        if "password" in text:
            return "password"
        return None

    async def complete_setup(self, user: str, password: str, token: str | None,
                             university: str = unis.DEFAULT_ID, overrides: dict | None = None) -> None:
        """The setup assistant, on first run or from Settings: config.toml, the secrets, then a test connection.
        Raises unis.FieldError naming the step to change, ValueError otherwise."""
        user = user.strip()
        if not config_mod.valid_user(user):
            raise unis.FieldError("user", t("setup.invalid_user"))
        if self.config_error and not self.needs_setup:
            raise ValueError(t("setup.fix_config", error=self.config_error))
        overrides = config_mod.check_overrides(overrides or {})
        profile = config_mod.profile_config(university, overrides)
        if profile.mfa == "saml":
            raise unis.FieldError("university", SAML_REQUIRED)
        if profile.needs_totp and not token:
            raise unis.FieldError("totp", t("totp.empty"))
        path = self.config_path or config_mod.default_path()
        loop = asyncio.get_running_loop()
        # Secrets first: if the keyring refuses, no config.toml exists yet and the assistant
        # stays the way in after a restart.
        previous_user, previous_university = self.cfg.user, self.cfg.university
        # Settings, Account runs the assistant again: the tunnel of the old account goes.
        if not self.needs_setup and self.tunnel:
            await self.request_disconnect()
        self.cfg.user = user
        try:
            await loop.run_in_executor(None, self.password_setter, password)
            if profile.needs_totp:
                await loop.run_in_executor(None, self.totp_setter, token)
            if path.exists():
                if university != previous_university:
                    stale = [key for key in unis.PROFILE_FIELDS if key not in overrides]
                    await loop.run_in_executor(None, config_mod.remove_keys, path, stale)
                values = {"user": user, "university": university, **overrides}
                await loop.run_in_executor(None, config_mod.set_values, path, values)
            else:
                await loop.run_in_executor(None, config_mod.write_initial, path, user, university, overrides)
        except config_mod.ConfigError as exc:
            self.cfg.user = previous_user
            raise ValueError(str(exc)) from exc
        except BaseException:
            self.cfg.user = previous_user
            raise
        config_mod.copy_profile(self.cfg, profile)
        self.config_path = path
        self.log.info("Setup completed for %s (%s)", user, profile.university_name)
        self.needs_setup = False
        self.config_error = None
        await self._secrets_changed()

    def note_code_shown(self) -> None:
        """A check code was shown: the user may type it into the MFA portal, which uses it up."""
        self.last_otp_step = int(time.time() // OTP_STEP)

    async def _secrets_changed(self) -> None:
        """New password or TOTP secret: an attempt still running with the old ones starts over."""
        self._secrets_gen += 1
        tunnel = self.tunnel
        if tunnel and self.state == State.connecting and not tunnel.exited.is_set():
            self.log.info("Secrets changed while connecting, starting over")
            disconnects = self._disconnects
            await tunnel.stop(self.cfg.stop_grace)
            if self._disconnects != disconnects:
                return  # a Disconnect during the stop wins
        await self.request_connect()

    def pac(self) -> str:
        return pac.build_pac(pac.read_domains(self.domains_path, self.cfg.default_domains), self.cfg.socks_port)

    async def set_domains(self, text: str) -> list[str]:
        domains, errors = pac.parse_domain_list(text)
        if errors:
            raise ValueError(t("app.lines", lines=errors))
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
        if pf.IS_WINDOWS:
            from .wintunnel import remove_stale_state_files

            remove_stale_state_files(self.token_dir)
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
            self._set(State.error, t("state.port_in_use", port=self.cfg.socks_port))
        ticker = asyncio.create_task(self._ticker())
        updates = asyncio.create_task(self._auto_update())
        self.started.set()
        try:
            await self._stop.wait()
        finally:
            ticker.cancel()
            updates.cancel()
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
            self._set(State.idle, t("state.not_connected"))
        self.note_activity()
        self._ensure_loop()
        self._wake.set()

    async def request_disconnect(self) -> None:
        self._disconnects += 1
        self.explicit = False
        self.demand_until = 0.0
        self._wake.set()
        tunnel = self.tunnel
        if tunnel:
            self._set(State.disconnecting, t("state.disconnecting"))
            await self.forwarder.close_all()
            await tunnel.stop(self.cfg.stop_grace)  # the loop may have dropped self.tunnel meanwhile
            return
        if self._loop_task and not self._loop_task.done():
            self._loop_task.cancel()
            try:
                await self._loop_task
            except asyncio.CancelledError:
                pass
        if self.state not in FINAL_STATES:
            self._set(State.idle, t("state.not_connected"))

    async def set_password(self, password: str) -> None:
        await asyncio.get_running_loop().run_in_executor(None, self.password_setter, password)
        self.log.info("Password stored in the keyring")
        await self._secrets_changed()

    async def set_totp(self, token: str) -> None:
        await asyncio.get_running_loop().run_in_executor(None, self.totp_setter, token)
        self.log.info("TOTP secret stored in the keyring")
        self.note_code_shown()  # the page shows the new check code
        await self._secrets_changed()

    # --- Connecting -------------------------------------------------------

    async def _connect_loop(self) -> None:
        cfg = self.cfg
        while self.has_demand():
            if self._elevated() is False:
                self._final(State.error, t("state.wintun_admin"))
                return
            if cfg.mfa == "saml":
                self._final(State.auth_failed, SAML_REQUIRED)
                return
            if await asyncio.get_running_loop().run_in_executor(None, self.cisco_check):
                self._set(State.blocked, BLOCKED_MESSAGE)
                await self._sleep(cfg.retry_interval)
                continue
            if not await self.probe():
                self._set(State.offline, t("state.offline"))
                await self._sleep(cfg.retry_interval)
                continue
            wait = self._otp_wait()
            if wait:
                self._set(State.connecting, t("state.otp_wait"))
                await self._sleep(wait)
                continue  # checks Cisco, network and demand again, then fetches the secrets
            generation = self._secrets_gen
            try:
                password = await self.password_getter()
            except credentials.PasswordMissing:
                self._final(State.keyring, t("state.no_password"))
                return
            except credentials.KeyringLocked:
                self._final(State.keyring, t("state.keyring_locked"))
                return
            except credentials.KeyringError as exc:
                self._final(State.keyring, str(exc))
                return
            totp = None
            if cfg.needs_totp:
                try:
                    totp = (await self.totp_getter()).decode("ascii").strip()
                except credentials.TotpMissing:
                    self._final(State.keyring, t("state.no_totp"))
                    return
                except credentials.KeyringLocked:
                    self._final(State.keyring, t("state.keyring_locked"))
                    return
                except (credentials.KeyringError, UnicodeDecodeError) as exc:
                    self._final(State.keyring, t("state.totp_unreadable", error=exc))
                    return

            self._set(State.connecting, t("state.connecting"))
            try:
                tunnel = self.tunnel_factory()
                self.tunnel = tunnel
                await tunnel.start(password, totp)
            except PasswordEncodingError as exc:
                self.tunnel = None
                self._final(State.keyring, t("state.password_encoding", error=exc))
                return
            except OSError as exc:
                self.tunnel = None
                self._final(State.error, t("state.openconnect_start", error=exc))
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
            if not ready and generation != self._secrets_gen:
                # The secrets changed while connecting: this result says nothing about the new ones.
                if not tunnel.exited.is_set():
                    await tunnel.stop(cfg.stop_grace)
                self.tunnel = None
                if tunnel.stopped_by_us and self.state == State.disconnecting:
                    self._after_stop()
                continue
            if not ready:
                if tunnel.stopped_by_us:
                    self.tunnel = None
                    self._after_stop()
                    continue  # a request_connect in the meantime takes effect via has_demand()
                if not tunnel.exited.is_set():
                    await tunnel.stop(cfg.stop_grace)
                    state, message = State.error, t("state.timeout")
                elif tunnel.classification:
                    state, message = State(tunnel.classification[0]), tunnel.classification[1]
                elif tunnel.returncode == 1:
                    state, message = State.auth_failed, t("state.login_failed_exit")
                else:
                    state, message = State.error, t("state.openconnect_exit", code=tunnel.returncode)
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
            self._set(State.connected, t("state.connected"))
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
            message = tunnel.classification[1] if tunnel.classification else t("state.dropped", code=tunnel.returncode)
            self.last_error = {"message": message, "at": time.time()}
            if not self.has_demand():
                self._set(State.idle, t("state.not_connected_because", reason=message))
                return
            self._set(State.error, message)
            if not await self._backoff():
                break
        # `blocked` stays until the ticker sees Cisco disconnected, so the popup keeps
        # explaining why nothing works.
        if self.state in (State.offline, State.error, State.connecting, State.disconnecting):
            self._set(State.idle, t("state.not_connected"))

    def _after_stop(self) -> None:
        if self.paused_by_cisco:
            self.paused_by_cisco = False
            self._set(State.blocked, BLOCKED_MESSAGE)
        else:
            self._set(State.idle, t("state.disconnected"))

    def _otp_wait(self, now: float | None = None) -> float:
        """Seconds until the next one-time code window, 0 if the current code is still unused."""
        now = time.time() if now is None else now
        if not self.cfg.needs_totp:
            return 0
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

    # --- Updates --------------------------------------------------------

    def _idle_for_update(self) -> bool:
        """Nothing uses or wants the tunnel: replacing the program and restarting goes unnoticed."""
        return (self.tunnel is None and not self.has_demand()
                and (self._loop_task is None or self._loop_task.done())
                and self.state not in (State.connecting, State.connected, State.disconnecting))

    async def set_language(self, code: str) -> None:
        """The app's language; "" follows the system."""
        if not i18n.valid(code):
            raise ValueError(f"unknown language {code!r}")
        if self.needs_setup or not self.config_path or not self.config_path.exists():
            raise ValueError(t("setup.finish_first"))
        await asyncio.get_running_loop().run_in_executor(
            None, lambda: config_mod.set_values(self.config_path, {"language": code}))
        self.cfg.language = code
        self.log.info("Language %s", code or "from the system")

    async def set_auto_update(self, enabled: bool) -> None:
        if self.needs_setup or not self.config_path or not self.config_path.exists():
            raise ValueError(t("setup.finish_first"))
        await asyncio.get_running_loop().run_in_executor(
            None, lambda: config_mod.set_values(self.config_path, {"auto_update": enabled}))
        self.cfg.auto_update = enabled
        self.log.info("Automatic updates %s", "on" if enabled else "off")
        if enabled:
            self._update_now.set()

    async def _auto_update(self) -> None:
        loop = asyncio.get_running_loop()
        try:
            self.commit = await loop.run_in_executor(None, self.updater.installed)
        except UpdateError as exc:
            self.log.warning("Update: %s", exc)
        delay = self.update_first_check
        while True:
            await wait_event(self._update_now, delay)
            self._update_now.clear()
            delay = self.update_interval + random.uniform(0, self.update_jitter)
            if not self.cfg.auto_update:
                continue
            try:
                commit = await loop.run_in_executor(None, self.updater.check)
            except UpdateError as exc:
                self.log.warning("Update: %s", exc)
                continue
            except Exception:  # noqa: BLE001 - an update problem must never stop the service
                self.log.exception("Update check failed")
                continue
            if not commit:
                continue
            self.log.info("Update %s ready, installing once the tunnel is idle", commit[:7])
            while self.cfg.auto_update and not self._idle_for_update():
                await asyncio.sleep(self.cfg.tick)
            if not self.cfg.auto_update:
                self.updater.discard()
                continue
            try:
                self.updater.apply()
            except UpdateError as exc:
                self.log.warning("Update: %s", exc)
                continue
            self.log.info("Updated to %s, restarting", commit[:7])
            self.restart_requested = True
            self.stop()
            return

    # --- Idle and resume ------------------------------------------------

    async def _ticker(self) -> None:
        # A clock that stops during sleep: the difference to the wall clock reveals a resume.
        if pf.IS_WINDOWS:
            from .windows import awake_seconds as awake
        else:
            awake = time.monotonic
        last_awake, last_wall = awake(), time.time()
        last_cisco = time.monotonic()
        while True:
            await asyncio.sleep(self.cfg.tick)
            mono, wall, now_awake = time.monotonic(), time.time(), awake()
            jump = (wall - last_wall) - (now_awake - last_awake)
            last_awake, last_wall = now_awake, wall
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
                    self._set(State.idle, t("state.not_connected"))
            if jump > 30:
                self.log.info("Resume detected (clock jumped by %.0f s)", jump)
                tunnel = self.tunnel
                if tunnel:
                    # Stop cleanly instead of SIGUSR2: the state machine then goes through
                    # disconnecting -> idle and reconnects if there is demand.
                    self.log.info("Resume detected, reconnecting the tunnel")
                    self._set(State.disconnecting, t("state.resume"))
                    await self.forwarder.close_all()
                    await tunnel.stop(self.cfg.stop_grace)
            if self.state == State.connected and self.tunnel and mono - self.last_activity > self.cfg.idle_minutes * 60:
                self.log.info("Idle for %.0f s, tearing down the tunnel", mono - self.last_activity)
                self.explicit = False
                self.demand_until = 0.0
                tunnel = self.tunnel
                self._set(State.disconnecting, t("state.idle_disconnect"))
                await self.forwarder.close_all()
                await tunnel.stop(self.cfg.stop_grace)
