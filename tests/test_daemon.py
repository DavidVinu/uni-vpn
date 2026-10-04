import asyncio
import logging
import os
import tempfile
import time
import unittest
from pathlib import Path
from unittest import mock

from uni_vpn import credentials, daemon as dm
from uni_vpn.config import Config
from uni_vpn.tunnel import free_port

from tests import posix_only

FAKE = str(Path(__file__).parent / "fake_openconnect.py")


async def wait_state(d, state, timeout=6):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if d.state == state:
            return
        await asyncio.sleep(0.05)
    raise AssertionError(f"state is {d.state.value} ({d.message}), expected {state.value}")


@posix_only
class DaemonHarness(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        tmp = Path(tempfile.mkdtemp())
        self.pwfile = tmp / "pw"
        self.tokenfile = tmp / "token"
        self.token_dir = tmp / "state"
        self.token_dir.mkdir()
        self.env = mock.patch.dict(os.environ, {"FAKE_PASSWORD_FILE": str(self.pwfile), "FAKE_TOKEN_FILE": str(self.tokenfile),
                                                "FAKE_MODE": "ok", "FAKE_DELAY": "0.2"})
        self.env.start()
        # The tests reconnect the tunnel every second; really waiting for the next one-time
        # code window (up to 30 s) would slow them down. Only the OTP tests use the original.
        self.real_otp_wait = dm.Daemon._otp_wait
        self.otp_patch = mock.patch.object(dm.Daemon, "_otp_wait", return_value=0)
        self.otp_patch.start()
        # ocproxy=FAKE: any executable path will do, the fake never starts the wrapper.
        self.cfg = Config(
            user="u", host="vpn.example", openconnect=FAKE, ocproxy=FAKE,
            socks_port=free_port(), http_port=free_port(),
            idle_minutes=0.01, ready_timeout=4, client_wait=2, stop_grace=1,
            halfclose_grace=0.5, demand_window=0.3, retry_interval=0.2, tick=0.1,
            backoff=[0.1, 0.1],
        )
        self.password = b"pw-s3cret"
        self.totp = b"base32:GEZDGNBVGY3TQOJQ"
        self.probe_result = True
        self.cisco = False
        self.stored = []
        self.stored_totp = []
        self.refreshed = []
        self.domains_path = tmp / "domains.txt"
        self.daemon = None
        self.task = None

    async def start_daemon(self, **kwargs):
        async def getter():
            if isinstance(self.password, Exception):
                raise self.password
            return self.password

        async def totp_getter():
            if isinstance(self.totp, Exception):
                raise self.totp
            return self.totp

        async def probe():
            return self.probe_result

        kwargs.setdefault("token_dir", self.token_dir)
        kwargs.setdefault("domains_path", self.domains_path)
        kwargs.setdefault("proxy_refresh", self.refreshed.append)
        self.daemon = dm.Daemon(
            self.cfg, logging.getLogger("t"),
            password_getter=getter, password_setter=self.stored.append,
            totp_getter=totp_getter, totp_setter=self.stored_totp.append,
            probe=probe, cisco_check=lambda: self.cisco, **kwargs,
        )
        self.task = asyncio.create_task(self.daemon.run())
        await asyncio.wait_for(self.daemon.started.wait(), 3)
        return self.daemon

    async def asyncTearDown(self):
        if self.daemon:
            self.daemon.stop()
            await asyncio.wait_for(self.task, 5)
        self.otp_patch.stop()
        self.env.stop()

    def pw_lines(self):
        return self.pwfile.read_text().splitlines() if self.pwfile.exists() else []

    async def client(self):
        return await asyncio.open_connection("127.0.0.1", self.cfg.socks_port)


class ConnectTests(DaemonHarness):
    async def test_first_connection_starts_tunnel_and_echoes(self):
        d = await self.start_daemon()
        reader, writer = await self.client()
        writer.write(b"hello")
        await writer.drain()
        self.assertEqual(await asyncio.wait_for(reader.readexactly(5), 4), b"hello")
        self.assertEqual(d.state, dm.State.connected)
        self.assertEqual(self.pw_lines(), ["pw-s3cret"])
        self.assertEqual(d.status()["active_connections"], 1)
        writer.close()

    async def test_second_connection_does_not_start_second_process(self):
        await self.start_daemon()
        c1 = await self.client()
        c2 = await self.client()
        for reader, writer in (c1, c2):
            writer.write(b"x")
            await writer.drain()
            self.assertEqual(await asyncio.wait_for(reader.readexactly(1), 4), b"x")
        self.assertEqual(len(self.pw_lines()), 1)
        for _, writer in (c1, c2):
            writer.close()

    async def test_client_wait_exceeded_then_connected(self):
        os.environ["FAKE_DELAY"] = "1.2"
        self.cfg.client_wait = 0.5
        d = await self.start_daemon()
        reader, writer = await self.client()
        started = time.monotonic()
        self.assertEqual(await asyncio.wait_for(reader.read(10), 3), b"")
        self.assertLess(time.monotonic() - started, 1.1)
        writer.close()
        await wait_state(d, dm.State.connected)

    async def test_disconnect_request_then_reconnect(self):
        d = await self.start_daemon()
        reader, writer = await self.client()
        writer.write(b"x")
        await reader.readexactly(1)
        await d.request_disconnect()
        await wait_state(d, dm.State.idle)
        self.assertEqual(await asyncio.wait_for(reader.read(10), 3), b"")
        writer.close()
        self.assertIsNone(d.tunnel)
        reader2, writer2 = await self.client()
        writer2.write(b"y")
        self.assertEqual(await asyncio.wait_for(reader2.readexactly(1), 4), b"y")
        self.assertEqual(len(self.pw_lines()), 2)
        writer2.close()

    async def test_disconnect_while_connecting_does_not_restart(self):
        os.environ["FAKE_DELAY"] = "3"
        d = await self.start_daemon()
        reader, writer = await self.client()
        await wait_state(d, dm.State.connecting)
        # Disconnect only once the process has read the password: `connecting` is set before
        # the start, and on the macOS runner the fake took longer for that than the test.
        deadline = time.monotonic() + 3
        while len(self.pw_lines()) < 1 and time.monotonic() < deadline:
            await asyncio.sleep(0.05)
        await d.request_disconnect()
        await wait_state(d, dm.State.idle)
        self.assertEqual(await asyncio.wait_for(reader.read(10), 2), b"")
        writer.close()
        await asyncio.sleep(0.5)
        self.assertEqual(d.state, dm.State.idle)
        self.assertEqual(d.forwarder.active, 0)
        self.assertEqual(len(self.pw_lines()), 1)

    async def test_explicit_connect_without_client(self):
        d = await self.start_daemon()
        await d.request_connect()
        await wait_state(d, dm.State.connected)
        self.assertEqual(len(self.pw_lines()), 1)
        # The "connect now" request is fulfilled, after that only real use counts.
        self.assertFalse(d.explicit)

    async def test_connect_request_during_disconnecting_reconnects(self):
        d = await self.start_daemon()
        await d.request_connect()
        await wait_state(d, dm.State.connected)
        disconnect = asyncio.create_task(d.request_disconnect())
        await asyncio.sleep(0)  # request_disconnect has set the state and is waiting for the process
        self.assertEqual(d.state, dm.State.disconnecting)
        await d.request_connect()
        await disconnect
        await wait_state(d, dm.State.connected)
        self.assertEqual(len(self.pw_lines()), 2)


class IdleTests(DaemonHarness):
    async def test_idle_disconnects_and_closes_clients(self):
        d = await self.start_daemon()
        reader, writer = await self.client()
        writer.write(b"x")
        await reader.readexactly(1)
        await wait_state(d, dm.State.idle, timeout=4)
        self.assertIsNone(d.tunnel)
        self.assertEqual(await asyncio.wait_for(reader.read(10), 3), b"")
        writer.close()

    async def test_ignore_sigterm_is_killed(self):
        os.environ["FAKE_MODE"] = "ignore_sigterm"
        self.cfg.stop_grace = 0.3
        d = await self.start_daemon()
        reader, writer = await self.client()
        writer.write(b"x")
        await reader.readexactly(1)
        writer.close()
        await wait_state(d, dm.State.idle, timeout=5)
        self.assertIsNone(d.tunnel)


class FailureTests(DaemonHarness):
    async def test_auth_fail_no_retry_until_explicit_connect(self):
        os.environ["FAKE_MODE"] = "auth_fail"
        d = await self.start_daemon()
        reader, writer = await self.client()
        self.assertEqual(await asyncio.wait_for(reader.read(10), 4), b"")
        writer.close()
        await wait_state(d, dm.State.auth_failed)
        self.assertIn("password", d.message)
        await asyncio.sleep(0.6)
        self.assertEqual(len(self.pw_lines()), 1)
        reader2, writer2 = await self.client()
        self.assertEqual(await asyncio.wait_for(reader2.read(10), 2), b"")
        writer2.close()
        self.assertEqual(len(self.pw_lines()), 1)
        await d.request_connect()
        await asyncio.sleep(0.8)
        self.assertEqual(len(self.pw_lines()), 2)

    async def test_input_required_message(self):
        os.environ["FAKE_MODE"] = "input_required"
        d = await self.start_daemon()
        await d.request_connect()
        await wait_state(d, dm.State.auth_failed)
        self.assertIn("uni-vpn log", d.message)

    async def test_tunnel_dies_with_demand_reconnects(self):
        os.environ["FAKE_MODE"] = "exit_after_ready"
        os.environ["FAKE_EXIT_AFTER"] = "0.4"
        self.cfg.demand_window = 1.5  # demand comes from usage, not from request_connect
        d = await self.start_daemon()
        reader, writer = await self.client()
        writer.write(b"x")
        await reader.readexactly(1)
        self.assertEqual(d.state, dm.State.connected)
        deadline = time.monotonic() + 4
        while len(self.pw_lines()) < 2 and time.monotonic() < deadline:
            await asyncio.sleep(0.05)
        self.assertGreaterEqual(len(self.pw_lines()), 2)
        self.assertIsNotNone(d.last_error)
        self.assertIn("exit code 1", d.last_error["message"])

    async def test_tunnel_dies_without_demand_goes_idle(self):
        os.environ["FAKE_MODE"] = "exit_after_ready"
        os.environ["FAKE_EXIT_AFTER"] = "0.8"
        self.cfg.idle_minutes = 1  # the idle timer must not fire first here
        d = await self.start_daemon()
        reader, writer = await self.client()
        writer.write(b"x")
        await reader.readexactly(1)
        writer.close()
        await wait_state(d, dm.State.idle, timeout=4)
        await asyncio.sleep(0.5)
        self.assertEqual(len(self.pw_lines()), 1)
        self.assertIn("exit code 1", d.last_error["message"])

    async def test_password_missing(self):
        self.password = credentials.PasswordMissing("x")
        d = await self.start_daemon()
        await d.request_connect()
        await wait_state(d, dm.State.keyring)
        self.assertIn("uni-vpn password", d.message)
        self.assertEqual(self.pw_lines(), [])

    async def test_totp_secret_reaches_openconnect(self):
        d = await self.start_daemon()
        await d.request_connect()
        await wait_state(d, dm.State.connected)
        self.assertEqual(self.tokenfile.read_text().splitlines(), ["base32:GEZDGNBVGY3TQOJQ"])
        self.assertEqual([p.name for p in self.token_dir.iterdir()], [])

    async def test_totp_missing(self):
        self.totp = credentials.TotpMissing("x")
        d = await self.start_daemon()
        await d.request_connect()
        await wait_state(d, dm.State.keyring)
        self.assertIn("uni-vpn totp", d.message)
        self.assertEqual(self.pw_lines(), [], "openconnect must not start without a secret")

    async def test_set_totp_stores_and_connects(self):
        self.totp = credentials.TotpMissing("x")
        d = await self.start_daemon()
        await d.request_connect()
        await wait_state(d, dm.State.keyring)
        self.totp = b"base32:GEZDGNBVGY3TQOJQ"
        await d.set_totp("base32:GEZDGNBVGY3TQOJQ")
        self.assertEqual(self.stored_totp, ["base32:GEZDGNBVGY3TQOJQ"])
        await wait_state(d, dm.State.connected)

    async def test_rejected_totp_is_final_auth_failure(self):
        os.environ["FAKE_MODE"] = "totp_rejected"
        d = await self.start_daemon()
        await d.request_connect()
        await wait_state(d, dm.State.auth_failed)
        self.assertIn("One-time code", d.message)
        await asyncio.sleep(0.5)
        self.assertEqual(len(self.pw_lines()), 1, "no automatic second attempt")

    async def test_successful_login_records_otp_window(self):
        d = await self.start_daemon()
        before = int(time.time() // 30)
        await d.request_connect()
        await wait_state(d, dm.State.connected)
        self.assertIn(d.last_otp_step, (before, before + 1))

    async def test_otp_wait_only_within_same_window(self):
        d = await self.start_daemon()
        wait = self.real_otp_wait
        self.assertEqual(wait(d, now=1000.0), 0)
        d.last_otp_step = int(1000.0 // 30)  # window 990..1020
        self.assertAlmostEqual(wait(d, now=1000.0), 20.5, places=1)
        self.assertAlmostEqual(wait(d, now=1019.0), 1.5, places=1)
        self.assertEqual(wait(d, now=1020.0), 0)
        self.assertEqual(wait(d, now=1100.0), 0)

    async def test_reconnect_in_same_window_waits_for_next_code(self):
        # Measured 2026-09-08: the same one-time code is valid only once. Reconnecting 3 s after
        # the login failed with "Login failed". So the daemon waits for the window to pass.
        d = await self.start_daemon()
        await d.request_connect()
        await wait_state(d, dm.State.connected)
        await d.request_disconnect()
        await wait_state(d, dm.State.idle)
        waits = iter([0.8])
        with mock.patch.object(d, "_otp_wait", side_effect=lambda: next(waits, 0)):
            started = time.monotonic()
            await d.request_connect()
            await wait_state(d, dm.State.connecting)
            self.assertIn("one-time code", d.message)
            await wait_state(d, dm.State.connected)
        self.assertGreaterEqual(time.monotonic() - started, 0.8)
        self.assertEqual(len(self.pw_lines()), 2)

    async def test_new_password_while_connecting_starts_over_with_it(self):
        # Measured with the assistant: Back, corrected password, Next. The attempt still running
        # used the old password and its result was shown for the new one.
        os.environ["FAKE_DELAY"] = "3"
        d = await self.start_daemon()
        await d.request_connect()
        await wait_state(d, dm.State.connecting)
        for _ in range(40):
            if self.pw_lines():
                break
            await asyncio.sleep(0.05)
        os.environ["FAKE_DELAY"] = "0.2"
        self.password = b"second"
        await d.set_password("second")
        await wait_state(d, dm.State.connected)
        self.assertEqual(self.pw_lines()[-1], "second")

    async def test_new_password_during_the_keyring_read_is_used(self):
        # The attempt already read the old password when the new one arrived; its result
        # must not be shown as the result for the new one.
        os.environ["FAKE_MODE"] = "auth_fail"
        d = await self.start_daemon()
        release = asyncio.Event()
        old = self.password

        async def slow_getter():
            value = self.password
            await release.wait()
            return value

        d.password_getter = slow_getter
        await d.request_connect()
        await asyncio.sleep(0.2)
        self.password = b"second"
        await d.set_password("second")
        release.set()
        # Both attempts are rejected by the fake; only the second one may decide the state.
        await wait_state(d, dm.State.auth_failed)
        self.assertEqual(self.pw_lines(), [old.decode(), "second"])

    async def test_stale_token_files_are_removed_at_start(self):
        (self.token_dir / "totp-old").write_text("base32:OLD")
        await self.start_daemon()
        self.assertEqual([p.name for p in self.token_dir.iterdir()], [])

    async def test_keyring_locked(self):
        self.password = credentials.KeyringLocked("x")
        d = await self.start_daemon()
        await d.request_connect()
        await wait_state(d, dm.State.keyring)
        self.assertIn("locked", d.message)

    async def test_set_password_stores_and_connects(self):
        self.password = credentials.PasswordMissing("x")
        d = await self.start_daemon()
        await d.request_connect()
        await wait_state(d, dm.State.keyring)
        self.password = b"new"
        await d.set_password("new")
        self.assertEqual(self.stored, ["new"])
        await wait_state(d, dm.State.connected)

    async def test_cisco_connecting_later_pauses_running_tunnel(self):
        # Measured 2026-09-08: Cisco was connected while the tunnel was up, and uni-vpn kept
        # running because the check only happened while connecting.
        d = await self.start_daemon()
        await d.request_connect()
        await wait_state(d, dm.State.connected)
        self.cisco = True
        await wait_state(d, dm.State.blocked)
        self.assertIn("Cisco", d.message)
        deadline = time.monotonic() + 3
        while d.tunnel is not None and time.monotonic() < deadline:
            await asyncio.sleep(0.05)
        self.assertIsNone(d.tunnel)
        await asyncio.sleep(0.5)
        self.assertEqual(d.state, dm.State.blocked, "without demand, blocked stays visible")
        self.assertEqual(len(self.pw_lines()), 1, "no reconnect while Cisco is connected")
        self.cisco = False
        await wait_state(d, dm.State.idle)
        await d.request_connect()
        await wait_state(d, dm.State.connected)
        self.assertEqual(len(self.pw_lines()), 2)

    async def test_cisco_pause_closes_browser_connections(self):
        d = await self.start_daemon()
        reader, writer = await self.client()
        writer.write(b"x")
        await asyncio.wait_for(reader.readexactly(1), 4)
        self.cisco = True
        await wait_state(d, dm.State.blocked)
        self.assertEqual(await asyncio.wait_for(reader.read(10), 3), b"")
        writer.close()

    async def test_windows_without_elevation_explains_instead_of_starting(self):
        d = await self.start_daemon()
        with mock.patch.object(d, "_elevated", return_value=False):
            await d.request_connect()
            await wait_state(d, dm.State.error)
        self.assertIn("administrator", d.message)
        self.assertEqual(self.pw_lines(), [])

    async def test_cisco_blocked_then_released(self):
        self.cisco = True
        d = await self.start_daemon()
        await d.request_connect()
        await wait_state(d, dm.State.blocked)
        self.assertEqual(self.pw_lines(), [])
        self.cisco = False
        await wait_state(d, dm.State.connected)

    async def test_offline_then_online(self):
        self.probe_result = False
        d = await self.start_daemon()
        await d.request_connect()
        await wait_state(d, dm.State.offline)
        self.assertEqual(self.pw_lines(), [])
        self.probe_result = True
        await wait_state(d, dm.State.connected)

    async def test_missing_openconnect_binary(self):
        self.cfg.openconnect = "/nonexistent/openconnect"
        d = await self.start_daemon()
        await d.request_connect()
        await wait_state(d, dm.State.error)
        self.assertIn("openconnect", d.message)

    async def test_config_error_daemon(self):
        d = await self.start_daemon(config_error="config.toml line 2: broken")
        self.assertEqual(d.state, dm.State.error)
        self.assertIn("line 2", d.message)
        self.assertIsNone(await d.acquire())


class StatusTests(DaemonHarness):
    async def test_status_keys(self):
        d = await self.start_daemon()
        s = d.status()
        for key in ("protocol", "version", "state", "message", "since", "host", "user", "socks_port",
                    "http_port", "active_connections", "bytes_in", "bytes_out", "last_error", "log_tail"):
            self.assertIn(key, s)
        self.assertEqual(s["protocol"], 1)
        self.assertEqual(s["state"], "idle")

    async def test_resume_triggers_reconnect(self):
        d = await self.start_daemon()
        reader, writer = await self.client()
        writer.write(b"x")
        await reader.readexactly(1)
        self.assertEqual(d.state, dm.State.connected)
        real_time = time.time
        with self.assertLogs("t", logging.INFO) as logs:
            with mock.patch.object(dm.time, "time", lambda: real_time() + 120):
                await asyncio.sleep(0.4)
        self.assertTrue(any("Resume detected, reconnecting the tunnel" in line for line in logs.output))
        # The old tunnel is gone, the browser connection was closed ...
        self.assertEqual(await asyncio.wait_for(reader.read(10), 3), b"")
        writer.close()
        # ... and since there was still demand, a new tunnel is up.
        await wait_state(d, dm.State.connected)
        self.assertEqual(len(self.pw_lines()), 2)
