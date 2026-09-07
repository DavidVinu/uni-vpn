import subprocess
import sys
import unittest
from unittest import mock

from uni_vpn import credentials, platform as pf


class GetPasswordTests(unittest.IsolatedAsyncioTestCase):
    async def test_returns_bytes_without_newline(self):
        cmd = [sys.executable, "-c", "import sys; sys.stdout.write('geheim')"]
        self.assertEqual(await credentials.get_password("u", 2, command=cmd), b"geheim")

    async def test_macos_strips_exactly_one_newline(self):
        cmd = [sys.executable, "-c", "import sys; sys.stdout.write('pw \\n')"]
        with mock.patch.object(pf, "IS_MACOS", True):
            self.assertEqual(await credentials.get_password("u", 2, command=cmd), b"pw ")

    async def test_missing(self):
        cmd = [sys.executable, "-c", "import sys; sys.exit(1)"]
        with self.assertRaises(credentials.PasswordMissing):
            await credentials.get_password("u", 2, command=cmd)

    async def test_empty_output_is_missing(self):
        cmd = [sys.executable, "-c", "pass"]
        with self.assertRaises(credentials.PasswordMissing):
            await credentials.get_password("u", 2, command=cmd)

    async def test_timeout_means_locked(self):
        cmd = [sys.executable, "-c", "import time; time.sleep(5)"]
        with self.assertRaises(credentials.KeyringLocked):
            await credentials.get_password("u", 0.3, command=cmd)


class StoreTests(unittest.TestCase):
    def test_linux_store_pipes_password_without_newline(self):
        calls = []

        def run(cmd, **kwargs):
            calls.append((cmd, kwargs))
            return subprocess.CompletedProcess(cmd, 0, b"", b"")

        with mock.patch.object(pf, "IS_MACOS", False), mock.patch.object(pf, "find_binary", return_value="/usr/bin/secret-tool"):
            credentials.store_password("ab1", "päss word", run=run)
        cmd, kwargs = calls[0]
        self.assertEqual(cmd[:2], ["/usr/bin/secret-tool", "store"])
        self.assertIn("uni-vpn", cmd)
        self.assertIn("ab1", cmd)
        self.assertEqual(kwargs["input"], "päss word".encode())

    def test_macos_store_uses_interactive_security(self):
        calls = []

        def run(cmd, **kwargs):
            calls.append((cmd, kwargs))
            return subprocess.CompletedProcess(cmd, 0, b"", b"")

        with mock.patch.object(pf, "IS_MACOS", True):
            credentials.store_password("ab1", 'a"b\\c', run=run)
        self.assertEqual(calls[0][0][:2], ["/usr/bin/security", "delete-generic-password"])
        cmd, kwargs = calls[1]
        self.assertEqual(cmd, ["/usr/bin/security", "-i"])
        script = kwargs["input"].decode()
        self.assertIn('add-generic-password -a "ab1" -s "uni-vpn" -T /usr/bin/security -w "a\\"b\\\\c"', script)
        self.assertNotIn(" -U ", script)

    def test_store_rejects_newline_before_running_anything(self):
        calls = []

        def run(cmd, **kwargs):
            calls.append(cmd)
            return subprocess.CompletedProcess(cmd, 0, b"", b"")

        for is_macos in (False, True):
            for password in ("a\nb", "a\rb", 'pw"\ndelete-generic-password -s uni-vpn'):
                with mock.patch.object(pf, "IS_MACOS", is_macos), \
                     mock.patch.object(pf, "find_binary", return_value="/usr/bin/secret-tool"):
                    with self.assertRaises(credentials.KeyringError, msg=repr(password)):
                        credentials.store_password("ab1", password, run=run)
        self.assertEqual(calls, [])

    def test_store_failure_raises(self):
        def run(cmd, **kwargs):
            return subprocess.CompletedProcess(cmd, 1, b"", b"kaputt")

        with mock.patch.object(pf, "IS_MACOS", False), mock.patch.object(pf, "find_binary", return_value="/usr/bin/secret-tool"):
            with self.assertRaises(credentials.KeyringError):
                credentials.store_password("ab1", "x", run=run)

    def test_missing_secret_tool(self):
        with mock.patch.object(pf, "IS_MACOS", False), mock.patch.object(pf, "find_binary", return_value=None):
            with self.assertRaises(credentials.KeyringError):
                credentials.lookup_command("ab1")


@unittest.skipUnless(sys.platform == "darwin", "nur auf macOS")
class MacKeychainRoundtrip(unittest.IsolatedAsyncioTestCase):
    """Echter security-Roundtrip in einem Wegwerf-Keychain (auch auf CI-Runnern ohne GUI-Session).

    Jeder Schritt meldet sich auf stderr und hat ein Timeout, damit ein haengender
    Keychain-Dialog den Lauf nicht stumm blockiert.
    """

    def sec(self, *args, check=True):
        print(f"security {' '.join(args[:2])}", file=sys.stderr, flush=True)
        return subprocess.run([pf.SECURITY, *args], capture_output=True, text=True, timeout=30, check=check)

    def setUp(self):
        import os
        self.keychain = f"/tmp/uni-vpn-test-{os.getpid()}.keychain-db"
        self.old_default = self.sec("default-keychain", "-d", "user", check=False).stdout.strip().strip('"')
        self.old_list = [line.strip().strip('"') for line in self.sec("list-keychains", "-d", "user", check=False).stdout.splitlines()]
        self.sec("create-keychain", "-p", "ci", self.keychain)
        self.sec("unlock-keychain", "-p", "ci", self.keychain)
        self.sec("set-keychain-settings", self.keychain)  # kein Auto-Lock
        self.sec("list-keychains", "-d", "user", "-s", self.keychain, *self.old_list)
        self.sec("default-keychain", "-d", "user", "-s", self.keychain)

    def tearDown(self):
        if self.old_default:
            self.sec("default-keychain", "-d", "user", "-s", self.old_default, check=False)
        self.sec("list-keychains", "-d", "user", "-s", *self.old_list, check=False)
        self.sec("delete-keychain", self.keychain, check=False)

    def store(self, user, password):
        print(f"store_password ({len(password)} Zeichen)", file=sys.stderr, flush=True)
        credentials.store_password(user, password)

    async def test_roundtrip_simple(self):
        user = "uni-vpn-citest-simple"
        self.store(user, "einfach123")
        self.assertEqual(await credentials.get_password(user, 10), b"einfach123")
        self.assertTrue(credentials.delete_password(user))

    async def test_roundtrip_special_characters(self):
        user = "uni-vpn-citest"
        self.store(user, "ci pass \"quoted\" \\ back")
        self.assertEqual(await credentials.get_password(user, 10), b'ci pass "quoted" \\ back')
        self.store(user, "zweites")  # Ueberschreiben = loeschen und neu anlegen
        self.assertEqual(await credentials.get_password(user, 10), b"zweites")
        self.assertTrue(credentials.delete_password(user))
        with self.assertRaises(credentials.PasswordMissing):
            await credentials.get_password(user, 10)
