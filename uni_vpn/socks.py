"""Minimal SOCKS5 server that dials out from a fixed source address.

Windows stand-in for ocproxy: openconnect cannot hand the tunnel to a script there, so it
brings up a Wintun adapter instead and this server connects from the adapter's address.
Windows sends such sockets through the adapter (strong host model) while everything else
keeps using the normal routes. Like ocproxy it supports CONNECT to IPv4 and host names only,
and resolves names with the VPN's DNS servers.
"""

from __future__ import annotations

import asyncio
import ipaddress
import logging
import random
import struct

VERSION = 5
NO_AUTH = 0
NO_ACCEPTABLE = 0xFF
CMD_CONNECT = 1
ATYP_IPV4, ATYP_DOMAIN, ATYP_IPV6 = 1, 3, 4
REP_OK, REP_FAILURE, REP_NET_UNREACHABLE, REP_HOST_UNREACHABLE, REP_REFUSED = 0, 1, 3, 4, 5
REP_CMD_UNSUPPORTED, REP_ATYP_UNSUPPORTED = 7, 8
HANDSHAKE_TIMEOUT = 30.0  # seconds for each read of the handshake


class DnsError(Exception):
    pass


def reason(exc: BaseException) -> str:
    """Text of an exception for the log; a timeout has none of its own."""
    text = str(exc)
    if text:
        return text
    if isinstance(exc, (TimeoutError, asyncio.TimeoutError)):
        return "timed out"
    return type(exc).__name__


def build_query(name: str, qid: int) -> bytes:
    header = struct.pack(">HHHHHH", qid, 0x0100, 1, 0, 0, 0)  # recursion desired, one question
    labels = b""
    for label in name.rstrip(".").split("."):
        try:
            raw = label.encode("idna") if label else b""
        except UnicodeError:
            raw = b""
        if not raw or len(raw) > 63:
            raise DnsError(f"invalid host name: {name}")
        labels += bytes([len(raw)]) + raw
    return header + labels + b"\x00" + struct.pack(">HH", 1, 1)  # type A, class IN


def _skip_name(data: bytes, offset: int) -> int:
    while True:
        if offset >= len(data):
            raise DnsError("truncated name")
        length = data[offset]
        if length & 0xC0 == 0xC0:
            return offset + 2
        if length == 0:
            return offset + 1
        offset += 1 + length


def parse_response(data: bytes, qid: int) -> list[str]:
    """IPv4 addresses from the answer section, in order. CNAME records are skipped."""
    if len(data) < 12:
        raise DnsError("short response")
    rid, flags, qdcount, ancount = struct.unpack(">HHHH", data[:8])
    if rid != qid:
        raise DnsError("response id mismatch")
    rcode = flags & 0x0F
    if rcode == 3:
        raise DnsError("host not found")
    if rcode:
        raise DnsError(f"DNS error {rcode}")
    offset = 12
    for _ in range(qdcount):
        offset = _skip_name(data, offset) + 4
    addresses = []
    for _ in range(ancount):
        offset = _skip_name(data, offset)
        if offset + 10 > len(data):
            raise DnsError("truncated answer")
        rtype, rclass, _ttl, rdlength = struct.unpack(">HHIH", data[offset:offset + 10])
        offset += 10
        if offset + rdlength > len(data):
            raise DnsError("truncated answer")
        if rtype == 1 and rclass == 1 and rdlength == 4:
            addresses.append(str(ipaddress.IPv4Address(data[offset:offset + 4])))
        offset += rdlength
    return addresses


class _DnsProtocol(asyncio.DatagramProtocol):
    def __init__(self, future: asyncio.Future):
        self.future = future

    def datagram_received(self, data, addr):
        if not self.future.done():
            self.future.set_result(data)

    def error_received(self, exc):
        if not self.future.done():
            self.future.set_exception(exc)


async def resolve(name: str, servers: list[str], source: str, timeout: float = 3.0, port: int = 53) -> str:
    """First IPv4 address of name, asked from each server in turn over UDP from source."""
    try:
        return str(ipaddress.IPv4Address(name))
    except ValueError:
        pass
    if not servers:
        raise DnsError("no DNS server from the VPN")
    loop = asyncio.get_running_loop()
    last: Exception = DnsError("no answer")
    for server in servers:
        qid = random.randrange(1 << 16)
        future = loop.create_future()
        try:
            transport, _ = await loop.create_datagram_endpoint(
                lambda: _DnsProtocol(future), local_addr=(source, 0), remote_addr=(server, port))
        except (OSError, ValueError) as exc:  # ValueError: a server address of another family
            last = exc if isinstance(exc, OSError) else DnsError(f"unusable DNS server {server}")
            continue
        try:
            transport.sendto(build_query(name, qid))
            data = await asyncio.wait_for(future, timeout)
            addresses = parse_response(data, qid)
            if addresses:
                return addresses[0]
            last = DnsError("no IPv4 address")
        except (OSError, asyncio.TimeoutError, DnsError) as exc:
            last = exc
            if isinstance(exc, DnsError) and str(exc) == "host not found":
                break
        finally:
            transport.close()
    raise DnsError(f"{name}: {reason(last)}")


class SocksServer:
    def __init__(self, port: int, source: str, dns: list[str], log: logging.Logger, *,
                 dns_port: int = 53, connect_timeout: float = 20.0, host: str = "127.0.0.1"):
        self.port = port
        self.source = source
        self.dns = list(dns)
        self.dns_port = dns_port
        self.connect_timeout = connect_timeout
        self.host = host
        self.log = log
        self._server: asyncio.AbstractServer | None = None
        self._tasks: set[asyncio.Task] = set()

    async def start(self) -> None:
        self._server = await asyncio.start_server(self._handle, self.host, self.port)

    async def stop(self) -> None:
        if self._server:
            self._server.close()
        for task in list(self._tasks):
            task.cancel()
        if self._tasks:
            await asyncio.wait(self._tasks, timeout=2)
        self._server = None

    async def _handle(self, reader: asyncio.StreamReader, writer: asyncio.StreamWriter) -> None:
        task = asyncio.current_task()
        self._tasks.add(task)
        upstream: asyncio.StreamWriter | None = None
        try:
            upstream_reader, upstream = await self._negotiate(reader, writer)
            if upstream is None:
                return
            await asyncio.gather(_pipe(reader, upstream), _pipe(upstream_reader, writer))
        except (asyncio.IncompleteReadError, ConnectionError, OSError, asyncio.TimeoutError):
            pass
        except Exception as exc:  # noqa: BLE001 - one bad client must not stop the server
            self.log.warning("SOCKS: %s", exc)
        finally:
            self._tasks.discard(task)
            for w in (writer, upstream):
                if w is not None:
                    w.close()

    async def _negotiate(self, reader, writer):
        version, count = await _read(reader, 2)
        if version != VERSION:
            return None, None
        methods = await _read(reader, count)
        if NO_AUTH not in methods:
            writer.write(bytes([VERSION, NO_ACCEPTABLE]))
            await writer.drain()
            return None, None
        writer.write(bytes([VERSION, NO_AUTH]))
        version, cmd, _rsv, atyp = await _read(reader, 4)
        if version != VERSION:
            await self._reply(writer, REP_FAILURE)
            return None, None
        if atyp == ATYP_IPV4:
            host = str(ipaddress.IPv4Address(await _read(reader, 4)))
        elif atyp == ATYP_DOMAIN:
            length = (await _read(reader, 1))[0]
            host = (await _read(reader, length)).decode("ascii", errors="replace")
        elif atyp == ATYP_IPV6:
            await _read(reader, 16)
            host = None
        else:
            host = None
        port = struct.unpack(">H", await _read(reader, 2))[0]
        if cmd != CMD_CONNECT:
            await self._reply(writer, REP_CMD_UNSUPPORTED)
            return None, None
        if host is None:
            await self._reply(writer, REP_ATYP_UNSUPPORTED)
            return None, None
        try:
            address = await resolve(host, self.dns, self.source, port=self.dns_port)
        except (DnsError, OSError) as exc:
            self.log.info("SOCKS: %s", exc)
            await self._reply(writer, REP_HOST_UNREACHABLE)
            return None, None
        try:
            upstream_reader, upstream = await asyncio.wait_for(
                asyncio.open_connection(address, port, local_addr=(self.source, 0)), self.connect_timeout)
        except ConnectionRefusedError:
            await self._reply(writer, REP_REFUSED)
            return None, None
        except (OSError, asyncio.TimeoutError) as exc:
            self.log.info("SOCKS: %s:%s not reachable: %s", host, port, reason(exc))
            await self._reply(writer, REP_HOST_UNREACHABLE)
            return None, None
        bound = upstream.get_extra_info("sockname") or ("0.0.0.0", 0)
        await self._reply(writer, REP_OK, bound[0], bound[1])
        return upstream_reader, upstream

    async def _reply(self, writer, code: int, address: str = "0.0.0.0", port: int = 0) -> None:
        try:
            packed = ipaddress.IPv4Address(address).packed
        except ValueError:
            packed = b"\x00\x00\x00\x00"
        writer.write(bytes([VERSION, code, 0, ATYP_IPV4]) + packed + struct.pack(">H", port))
        await writer.drain()


async def _read(reader: asyncio.StreamReader, count: int) -> bytes:
    return await asyncio.wait_for(reader.readexactly(count), HANDSHAKE_TIMEOUT)


async def _pipe(reader: asyncio.StreamReader, writer: asyncio.StreamWriter) -> None:
    try:
        while True:
            data = await reader.read(65536)
            if not data:
                break
            writer.write(data)
            await writer.drain()
    except (ConnectionError, OSError):
        pass
    finally:
        try:
            if writer.can_write_eof():
                writer.write_eof()
        except (ConnectionError, OSError, RuntimeError):
            pass
