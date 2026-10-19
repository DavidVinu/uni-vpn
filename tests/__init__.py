import socket
import sys
import unittest
from unittest import mock

WINDOWS = sys.platform == "win32"
posix_only = unittest.skipIf(WINDOWS, "needs POSIX processes, signals or file modes")


def simulate_posix():
    """setUpModule/tearDownModule for modules that exercise the Linux and macOS code paths
    with mocks, so they also run on Windows."""
    from uni_vpn import platform as pf

    patcher = mock.patch.object(pf, "IS_WINDOWS", False)
    return patcher.start, patcher.stop


def free_ports(count: int) -> list[int]:
    """Different free ports: two free_port() calls in a row can return the same one."""
    socks = [socket.socket() for _ in range(count)]
    try:
        for sock in socks:
            sock.bind(("127.0.0.1", 0))
        return [sock.getsockname()[1] for sock in socks]
    finally:
        for sock in socks:
            sock.close()
