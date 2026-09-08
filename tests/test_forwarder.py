import asyncio
import logging
import unittest

from uni_vpn.forwarder import Forwarder
from uni_vpn.tunnel import free_port


async def echo_server():
    async def handle(reader, writer):
        while data := await reader.read(65536):
            writer.write(data)
            await writer.drain()
        writer.close()

    server = await asyncio.start_server(handle, "127.0.0.1", 0)
    return server, server.sockets[0].getsockname()[1]


class ForwarderTests(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.echo, self.echo_port = await echo_server()
        self.activity = 0

        def on_activity():
            self.activity += 1

        self.target = self.echo_port
        self.fwd = Forwarder("127.0.0.1", free_port(), self.get_target, on_activity, 0.5, logging.getLogger("t"))
        await self.fwd.start()

    async def get_target(self):
        return self.target

    async def asyncTearDown(self):
        await self.fwd.stop()
        self.echo.close()
        await self.echo.wait_closed()

    async def test_pipes_both_directions_and_counts(self):
        reader, writer = await asyncio.open_connection("127.0.0.1", self.fwd.port)
        writer.write(b"hallo")
        await writer.drain()
        self.assertEqual(await reader.readexactly(5), b"hallo")
        self.assertEqual(self.fwd.active, 1)
        self.assertEqual(self.fwd.bytes_out, 5)
        self.assertEqual(self.fwd.bytes_in, 5)
        self.assertGreaterEqual(self.activity, 2)
        writer.close()
        await writer.wait_closed()
        await asyncio.sleep(0.2)
        self.assertEqual(self.fwd.active, 0)

    async def test_no_target_closes_immediately(self):
        self.target = None
        reader, writer = await asyncio.open_connection("127.0.0.1", self.fwd.port)
        self.assertEqual(await asyncio.wait_for(reader.read(10), 2), b"")
        writer.close()

    async def test_unreachable_target_closes(self):
        self.target = free_port()
        reader, writer = await asyncio.open_connection("127.0.0.1", self.fwd.port)
        self.assertEqual(await asyncio.wait_for(reader.read(10), 2), b"")
        writer.close()

    async def test_close_all(self):
        reader, writer = await asyncio.open_connection("127.0.0.1", self.fwd.port)
        writer.write(b"x")
        await reader.readexactly(1)
        await self.fwd.close_all()
        self.assertEqual(await asyncio.wait_for(reader.read(10), 2), b"")
        await asyncio.sleep(0.2)
        self.assertEqual(self.fwd.active, 0)
        writer.close()

    async def test_close_all_cancels_waiting_handlers(self):
        gate = asyncio.Event()

        async def blocked():
            await gate.wait()
            return self.echo_port

        self.fwd.get_target = blocked
        reader, writer = await asyncio.open_connection("127.0.0.1", self.fwd.port)
        await asyncio.sleep(0.1)
        self.assertEqual(self.fwd.active, 1)
        await self.fwd.close_all()
        self.assertEqual(self.fwd.active, 0)
        self.assertEqual(await asyncio.wait_for(reader.read(10), 2), b"")
        writer.close()

    async def test_halfclose_grace(self):
        reader, writer = await asyncio.open_connection("127.0.0.1", self.fwd.port)
        writer.write(b"abc")
        writer.write_eof()
        self.assertEqual(await asyncio.wait_for(reader.read(3), 2), b"abc")
        self.assertEqual(await asyncio.wait_for(reader.read(3), 2), b"")
        writer.close()
