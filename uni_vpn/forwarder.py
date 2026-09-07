"""SOCKS-Passthrough: nimmt Browserverbindungen an und reicht Bytes an ocproxy durch."""

from __future__ import annotations

import asyncio
import logging
from typing import Awaitable, Callable


class Forwarder:
    def __init__(self, host: str, port: int, get_target: Callable[[], Awaitable[int | None]],
                 on_activity: Callable[[], None], halfclose_grace: float, log: logging.Logger):
        self.host = host
        self.port = port
        self.get_target = get_target
        self.on_activity = on_activity
        self.halfclose_grace = halfclose_grace
        self.log = log
        self.active = 0
        self.bytes_in = 0
        self.bytes_out = 0
        self.rejected = 0
        self._server: asyncio.AbstractServer | None = None
        self._writers: set[asyncio.StreamWriter] = set()

    async def start(self) -> None:
        self._server = await asyncio.start_server(self._handle, self.host, self.port)

    async def stop(self) -> None:
        if self._server:
            self._server.close()
            await self._server.wait_closed()
        await self.close_all()

    async def close_all(self) -> None:
        for writer in list(self._writers):
            writer.close()
        await asyncio.sleep(0)

    async def _handle(self, reader: asyncio.StreamReader, writer: asyncio.StreamWriter) -> None:
        self.active += 1
        self._writers.add(writer)
        upstream_writer: asyncio.StreamWriter | None = None
        try:
            target = await self.get_target()
            if target is None:
                self.rejected += 1
                return
            try:
                upstream_reader, upstream_writer = await asyncio.open_connection("127.0.0.1", target)
            except OSError as exc:
                self.log.warning("ocproxy-Port %s nicht erreichbar: %s", target, exc)
                self.rejected += 1
                return
            self._writers.add(upstream_writer)
            to_upstream = asyncio.create_task(self._pipe(reader, upstream_writer, "out"))
            to_client = asyncio.create_task(self._pipe(upstream_reader, writer, "in"))
            done, pending = await asyncio.wait({to_upstream, to_client}, return_when=asyncio.FIRST_COMPLETED)
            if pending:
                _done, still = await asyncio.wait(pending, timeout=self.halfclose_grace)
                for task in still:
                    task.cancel()
        finally:
            for w in (writer, upstream_writer):
                if w is not None:
                    self._writers.discard(w)
                    w.close()
            self.active -= 1

    async def _pipe(self, reader: asyncio.StreamReader, writer: asyncio.StreamWriter, direction: str) -> None:
        try:
            while True:
                data = await reader.read(65536)
                if not data:
                    break
                if direction == "in":
                    self.bytes_in += len(data)
                else:
                    self.bytes_out += len(data)
                self.on_activity()
                writer.write(data)
                await writer.drain()
        except (ConnectionError, asyncio.IncompleteReadError, OSError):
            pass
        finally:
            try:
                if writer.can_write_eof():
                    writer.write_eof()
            except (ConnectionError, OSError, RuntimeError):
                pass
