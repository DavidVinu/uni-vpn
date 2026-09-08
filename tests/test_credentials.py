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


class TotpEntryTests(unittest.IsolatedAsyncioTestCase):
    """Der TOTP-Schluessel liegt unter eigenem Dienstnamen, damit secret-tool ihn nie mit dem Passwort verwechselt."""

    def test_lookup_commands_use_separate_service_names(self):
        with mock.patch.object(pf, "IS_MACOS", False), mock.patch.object(pf, "find_binary", return_value="/usr/bin/secret-tool"):
            self.assertEqual(credentials.lookup_command("ab1"),
                             ["/usr/bin/secret-tool", "lookup", "service", "uni-vpn", "user", "ab1"])
            self.assertEqual(credentials.lookup_command("ab1", kind="totp"),
                             ["/usr/bin/secret-tool", "lookup", "service", "uni-vpn-totp", "user", "ab1"])
        with mock.patch.object(pf, "IS_MACOS", True):
            self.assertEqual(credentials.lookup_command("ab1", kind="totp"),
                             ["/usr/bin/security", "find-generic-password", "-s", "uni-vpn-totp", "-a", "ab1", "-w"])

    async def test_get_totp_returns_token(self):
        cmd = [sys.executable, "-c", "import sys; sys.stdout.write('base32:GEZDGNBVGY3TQOJQ')"]
        self.assertEqual(await credentials.get_totp("u", 2, command=cmd), b"base32:GEZDGNBVGY3TQOJQ")

    async def test_get_totp_missing_is_its_own_error(self):
        cmd = [sys.executable, "-c", "import sys; sys.exit(1)"]
        with self.assertRaises(credentials.TotpMissing):
            await credentials.get_totp("u", 2, command=cmd)
        self.assertFalse(issubclass(credentials.TotpMissing, credentials.PasswordMissing))
        self.assertTrue(issubclass(credentials.TotpMissing, credentials.SecretMissing))
        self.assertTrue(issubclass(credentials.PasswordMissing, credentials.SecretMissing))

    def test_store_totp_linux(self):
        calls = []

        def run(cmd, **kwargs):
            calls.append((cmd, kwargs))
            return subprocess.CompletedProcess(cmd, 0, b"", b"")

        with mock.patch.object(pf, "IS_MACOS", False), mock.patch.object(pf, "find_binary", return_value="/usr/bin/secret-tool"):
            credentials.store_totp("ab1", "base32:GEZDGNBVGY3TQOJQ", run=run)
        cmd, kwargs = calls[0]
        self.assertEqual(cmd, ["/usr/bin/secret-tool", "store", "--label", "Uni VPN (zweiter Faktor)",
                               "service", "uni-vpn-totp", "user", "ab1"])
        self.assertEqual(kwargs["input"], b"base32:GEZDGNBVGY3TQOJQ")

    def test_store_totp_macos_deletes_then_adds_under_own_service(self):
        calls = []

        def run(cmd, **kwargs):
            calls.append((cmd, kwargs))
            return subprocess.CompletedProcess(cmd, 0, b"", b"")

        with mock.patch.object(pf, "IS_MACOS", True):
            credentials.store_totp("ab1", "base32:GEZDGNBVGY3TQOJQ", run=run)
        self.assertEqual(calls[0][0], ["/usr/bin/security", "delete-generic-password", "-s", "uni-vpn-totp", "-a", "ab1"])
        script = calls[1][1]["input"].decode()
        self.assertIn('-s "uni-vpn-totp"', script)
        self.assertIn('-w "base32:GEZDGNBVGY3TQOJQ"', script)

    def test_delete_totp_uses_own_service(self):
        calls = []

        def run(cmd, **kwargs):
            calls.append(cmd)
            return subprocess.CompletedProcess(cmd, 0, b"", b"")

        with mock.patch.object(pf, "IS_MACOS", False), mock.patch.object(pf, "find_binary", return_value="/usr/bin/secret-tool"):
            self.assertTrue(credentials.delete_totp("ab1", run=run))
        self.assertEqual(calls, [["/usr/bin/secret-tool", "clear", "service", "uni-vpn-totp", "user", "ab1"]])


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

    async def test_roundtrip_totp_is_separate_entry(self):
        user = "uni-vpn-citest-totp"
        self.store(user, "passwort")
        credentials.store_totp(user, "base32:GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ")
        self.assertEqual(await credentials.get_password(user, 10), b"passwort")
        self.assertEqual(await credentials.get_totp(user, 10), b"base32:GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ")
        self.assertTrue(credentials.delete_totp(user))
        with self.assertRaises(credentials.TotpMissing):
            await credentials.get_totp(user, 10)
        self.assertEqual(await credentials.get_password(user, 10), b"passwort")
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
