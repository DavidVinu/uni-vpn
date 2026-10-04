import asyncio
import logging
import socket
import struct
import unittest

from uni_vpn import socks
from uni_vpn.tunnel import free_port


def answer(query: bytes, addresses: list[str], rcode: int = 0, cname: bool = False) -> bytes:
    qid = query[:2]
    question = query[12:]
    records = b""
    count = 0
    if cname:
        target = b"\x05alias\x07example\x00"
        records += b"\xc0\x0c" + struct.pack(">HHIH", 5, 1, 60, len(target)) + target
        count += 1
    for address in addresses:
        records += b"\xc0\x0c" + struct.pack(">HHIH", 1, 1, 60, 4) + socket.inet_aton(address)
        count += 1
    header = qid + struct.pack(">HHHHH", 0x8180 | rcode, 1, count, 0, 0)
    return header + question + records


class FakeDns(asyncio.DatagramProtocol):
    def __init__(self, table):
        self.table = table
        self.queries = []

    def connection_made(self, transport):
        self.transport = transport

    def datagram_received(self, data, addr):
        name = []
        offset = 12
        while data[offset]:
            length = data[offset]
            name.append(data[offset + 1:offset + 1 + length].decode())
            offset += 1 + length
        host = ".".join(name)
        self.queries.append(host)
        if host in self.table:
            self.transport.sendto(answer(data, [self.table[host]], cname=True), addr)
        else:
            self.transport.sendto(answer(data, [], rcode=3), addr)


class ParseTests(unittest.TestCase):
    def test_query_roundtrip(self):
        query = socks.build_query("sogo.uni-heidelberg.de", 0x1234)
        self.assertEqual(query[:2], b"\x12\x34")
        self.assertEqual(socks.parse_response(answer(query, ["10.1.2.3"], cname=True), 0x1234), ["10.1.2.3"])

    def test_nxdomain_and_mismatch(self):
        query = socks.build_query("x.example", 7)
        with self.assertRaisesRegex(socks.DnsError, "not found"):
            socks.parse_response(answer(query, [], rcode=3), 7)
        with self.assertRaisesRegex(socks.DnsError, "mismatch"):
            socks.parse_response(answer(query, ["1.2.3.4"]), 8)

    def test_bad_names(self):
        for name in ("", "a..b", "x" * 64 + ".de"):
            with self.assertRaises(socks.DnsError):
                socks.build_query(name, 1)

    def test_truncated(self):
        with self.assertRaises(socks.DnsError):
            socks.parse_response(b"\x00\x01", 1)


class ServerTests(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        loop = asyncio.get_running_loop()
        self.dns_port = free_port()
        self.dns_transport, self.dns = await loop.create_datagram_endpoint(
            lambda: FakeDns({"intra.example": "127.0.0.1"}), local_addr=("127.0.0.1", self.dns_port))

        async def echo(reader, writer):
            while data := await reader.read(1024):
                writer.write(data)
                await writer.drain()
            writer.close()

        self.echo = await asyncio.start_server(echo, "127.0.0.1", 0)
        self.echo_port = self.echo.sockets[0].getsockname()[1]
        self.port = free_port()
        self.server = socks.SocksServer(self.port, "127.0.0.1", ["127.0.0.1"], logging.getLogger("t"),
                                        dns_port=self.dns_port, connect_timeout=5)
        await self.server.start()

    async def asyncTearDown(self):
        await self.server.stop()
        self.echo.close()
        self.dns_transport.close()

    async def socks_connect(self, atyp: int, address: bytes, port: int):
        reader, writer = await asyncio.open_connection("127.0.0.1", self.port)
        writer.write(b"\x05\x01\x00")
        self.assertEqual(await reader.readexactly(2), b"\x05\x00")
        writer.write(bytes([5, 1, 0, atyp]) + address + struct.pack(">H", port))
        reply = await asyncio.wait_for(reader.readexactly(10), 5)
        return reader, writer, reply[1]

    async def test_connect_by_name_resolves_via_vpn_dns(self):
        name = b"intra.example"
        reader, writer, code = await self.socks_connect(3, bytes([len(name)]) + name, self.echo_port)
        self.assertEqual(code, socks.REP_OK)
        writer.write(b"hello")
        self.assertEqual(await asyncio.wait_for(reader.readexactly(5), 3), b"hello")
        writer.close()
        self.assertEqual(self.dns.queries, ["intra.example"])

    async def test_connect_by_ipv4(self):
        reader, writer, code = await self.socks_connect(1, socket.inet_aton("127.0.0.1"), self.echo_port)
        self.assertEqual(code, socks.REP_OK)
        writer.write(b"x")
        self.assertEqual(await asyncio.wait_for(reader.readexactly(1), 3), b"x")
        writer.close()
        self.assertEqual(self.dns.queries, [])

    async def test_unknown_host(self):
        name = b"nope.example"
        _reader, writer, code = await self.socks_connect(3, bytes([len(name)]) + name, self.echo_port)
        self.assertEqual(code, socks.REP_HOST_UNREACHABLE)
        writer.close()

    async def test_refused(self):
        _reader, writer, code = await self.socks_connect(1, socket.inet_aton("127.0.0.1"), free_port())
        self.assertEqual(code, socks.REP_REFUSED)
        writer.close()

    async def test_ipv6_unsupported(self):
        _reader, writer, code = await self.socks_connect(4, b"\x00" * 16, 80)
        self.assertEqual(code, socks.REP_ATYP_UNSUPPORTED)
        writer.close()

    async def test_auth_required_is_refused(self):
        reader, writer = await asyncio.open_connection("127.0.0.1", self.port)
        writer.write(b"\x05\x01\x02")
        self.assertEqual(await reader.readexactly(2), b"\x05\xff")
        writer.close()

    async def test_bind_command_unsupported(self):
        reader, writer = await asyncio.open_connection("127.0.0.1", self.port)
        writer.write(b"\x05\x01\x00" + b"\x05\x02\x00\x01" + socket.inet_aton("127.0.0.1") + b"\x00\x50")
        self.assertEqual(await reader.readexactly(2), b"\x05\x00")
        self.assertEqual((await reader.readexactly(10))[1], socks.REP_CMD_UNSUPPORTED)
        writer.close()


if __name__ == "__main__":
    unittest.main()
