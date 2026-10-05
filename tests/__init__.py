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
