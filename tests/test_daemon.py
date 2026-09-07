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

FAKE = str(Path(__file__).parent / "fake_openconnect.py")


async def wait_state(d, state, timeout=6):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if d.state == state:
            return
        await asyncio.sleep(0.05)
    raise AssertionError(f"Zustand ist {d.state.value} ({d.message}), erwartet {state.value}")


class DaemonHarness(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.pwfile = Path(tempfile.mkdtemp()) / "pw"
        self.env = mock.patch.dict(os.environ, {"FAKE_PASSWORD_FILE": str(self.pwfile), "FAKE_MODE": "ok", "FAKE_DELAY": "0.2"})
        self.env.start()
        # ocproxy=FAKE: irgendein ausfuehrbarer Pfad reicht, der Fake startet den Wrapper nie.
        self.cfg = Config(
            user="u", host="vpn.example", openconnect=FAKE, ocproxy=FAKE,
            socks_port=free_port(), http_port=free_port(),
            idle_minutes=0.01, ready_timeout=4, client_wait=2, stop_grace=1,
            halfclose_grace=0.5, demand_window=0.3, retry_interval=0.2, tick=0.1,
            backoff=[0.1, 0.1],
        )
        self.password = b"geheim"
        self.probe_result = True
        self.cisco = False
        self.stored = []
        self.daemon = None
        self.task = None

    async def start_daemon(self, **kwargs):
        async def getter():
            if isinstance(self.password, Exception):
                raise self.password
            return self.password

        async def probe():
            return self.probe_result

        self.daemon = dm.Daemon(
            self.cfg, logging.getLogger("t"),
            password_getter=getter, password_setter=self.stored.append,
            probe=probe, cisco_check=lambda: self.cisco, **kwargs,
        )
        self.task = asyncio.create_task(self.daemon.run())
        await asyncio.wait_for(self.daemon.started.wait(), 3)
        return self.daemon

    async def asyncTearDown(self):
        if self.daemon:
            self.daemon.stop()
            await asyncio.wait_for(self.task, 5)
        self.env.stop()

    def pw_lines(self):
        return self.pwfile.read_text().splitlines() if self.pwfile.exists() else []

    async def client(self):
        return await asyncio.open_connection("127.0.0.1", self.cfg.socks_port)


class ConnectTests(DaemonHarness):
    async def test_first_connection_starts_tunnel_and_echoes(self):
        d = await self.start_daemon()
        reader, writer = await self.client()
        writer.write(b"hallo")
        await writer.drain()
        self.assertEqual(await asyncio.wait_for(reader.readexactly(5), 4), b"hallo")
        self.assertEqual(d.state, dm.State.connected)
        self.assertEqual(self.pw_lines(), ["geheim"])
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
        # Der Wunsch "jetzt verbinden" ist erfuellt, danach zaehlt nur noch echte Nutzung.
        self.assertFalse(d.explicit)

    async def test_connect_request_during_disconnecting_reconnects(self):
        d = await self.start_daemon()
        await d.request_connect()
        await wait_state(d, dm.State.connected)
        disconnect = asyncio.create_task(d.request_disconnect())
        await asyncio.sleep(0)  # request_disconnect hat den Zustand gesetzt und wartet auf den Prozess
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
        self.assertIn("Passwort", d.message)
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
        self.assertIn("OTP", d.message)

    async def test_tunnel_dies_with_demand_reconnects(self):
        os.environ["FAKE_MODE"] = "exit_after_ready"
        os.environ["FAKE_EXIT_AFTER"] = "0.4"
        self.cfg.demand_window = 1.5  # Bedarf kommt aus der Nutzung, nicht aus request_connect
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
        self.assertIn("Exit 1", d.last_error["message"])

    async def test_tunnel_dies_without_demand_goes_idle(self):
        os.environ["FAKE_MODE"] = "exit_after_ready"
        os.environ["FAKE_EXIT_AFTER"] = "0.8"
        self.cfg.idle_minutes = 1  # Leerlauf-Timer darf hier nicht zuerst greifen
        d = await self.start_daemon()
        reader, writer = await self.client()
        writer.write(b"x")
        await reader.readexactly(1)
        writer.close()
        await wait_state(d, dm.State.idle, timeout=4)
        await asyncio.sleep(0.5)
        self.assertEqual(len(self.pw_lines()), 1)
        self.assertIn("Exit 1", d.last_error["message"])

    async def test_password_missing(self):
        self.password = credentials.PasswordMissing("x")
        d = await self.start_daemon()
        await d.request_connect()
        await wait_state(d, dm.State.keyring)
        self.assertIn("uni-vpn password", d.message)
        self.assertEqual(self.pw_lines(), [])

    async def test_keyring_locked(self):
        self.password = credentials.KeyringLocked("x")
        d = await self.start_daemon()
        await d.request_connect()
        await wait_state(d, dm.State.keyring)
        self.assertIn("gesperrt", d.message)

    async def test_set_password_stores_and_connects(self):
        self.password = credentials.PasswordMissing("x")
        d = await self.start_daemon()
        await d.request_connect()
        await wait_state(d, dm.State.keyring)
        self.password = b"neu"
        await d.set_password("neu")
        self.assertEqual(self.stored, ["neu"])
        await wait_state(d, dm.State.connected)

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
        d = await self.start_daemon(config_error="config.toml Zeile 2: kaputt")
        self.assertEqual(d.state, dm.State.error)
        self.assertIn("Zeile 2", d.message)
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
        self.assertTrue(any("Resume erkannt, Tunnel wird neu aufgebaut" in line for line in logs.output))
        # Der alte Tunnel ist weg, die Browserverbindung wurde geschlossen ...
        self.assertEqual(await asyncio.wait_for(reader.read(10), 3), b"")
        writer.close()
        # ... und weil noch Bedarf bestand, steht ein neuer Tunnel.
        await wait_state(d, dm.State.connected)
        self.assertEqual(len(self.pw_lines()), 2)
