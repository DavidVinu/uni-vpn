import asyncio
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from uni_vpn import removal

from tests import posix_only
from tests.test_daemon import DaemonHarness
from tests.test_updater import until


def package(root: Path) -> Path:
    """An installer folder with the program copy inside, and a user copy pointing at it."""
    installer = root / "installer"
    (installer / "app" / "uni_vpn").mkdir(parents=True)
    (installer / "app" / "uni_vpn" / "__init__.py").write_text("")
    copy = root / "copy"
    copy.mkdir()
    (copy / removal.MARKER).write_text(f"{installer}\n")
    return copy


class PackageTests(unittest.TestCase):
    def test_gone_only_for_a_copy_from_an_installer(self):
        root = Path(tempfile.mkdtemp())
        self.assertFalse(removal.package_gone(root))  # a git checkout or get.sh copy
        copy = package(root)
        self.assertFalse(removal.package_gone(copy))
        (root / "installer" / "app" / "uni_vpn" / "__init__.py").unlink()
        self.assertTrue(removal.package_gone(copy))

    def test_remove_keeps_the_keyring_and_ends_the_service_last(self):
        calls = []

        def uninstall(args, input_fn, service_uninstall):
            calls.append(("uninstall", args.yes, input_fn("Delete?")))
            service_uninstall()
            return 0

        def run(cmd, **_kwargs):
            calls.append(tuple(cmd))
            return mock.Mock(returncode=0)

        target = Path(tempfile.mkdtemp()) / "uni-vpn.service"
        target.write_text("")
        with mock.patch.object(removal.pf, "IS_MACOS", False), \
                mock.patch("uni_vpn.service.unit_target_path", return_value=target):
            self.assertEqual(removal.remove(run=run, uninstall=uninstall), 0)
        self.assertEqual(calls[0], ("uninstall", False, "n"))
        self.assertFalse(target.exists())
        self.assertNotIn(("systemctl", "--user", "stop", "uni-vpn"), calls[:-1])
        self.assertEqual(calls[-1], ("systemctl", "--user", "stop", "uni-vpn"))


@posix_only
class DaemonRemovalTests(DaemonHarness):
    async def test_removed_app_uninstalls_after_two_checks(self):
        root = Path(tempfile.mkdtemp())
        copy = package(root)
        removed = []
        d = await self.start_daemon(package_root=copy, package_check=0.05, remove_self=lambda: removed.append(1))
        await asyncio.sleep(0.2)
        self.assertEqual(removed, [])
        (root / "installer" / "app" / "uni_vpn" / "__init__.py").unlink()
        await asyncio.wait_for(self.task, 3)
        self.assertEqual(removed, [1])
        self.assertFalse(d.restart_requested)
        self.daemon = None

    async def test_a_new_version_being_installed_is_not_a_removal(self):
        root = Path(tempfile.mkdtemp())
        copy = package(root)
        removed = []
        await self.start_daemon(package_root=copy, package_check=0.3, remove_self=lambda: removed.append(1))
        marker = root / "installer" / "app" / "uni_vpn" / "__init__.py"
        marker.unlink()
        await asyncio.sleep(0.1)  # shorter than one check interval: at most one check sees it
        marker.write_text("")
        await asyncio.sleep(0.8)
        self.assertEqual(removed, [])
        self.assertFalse(self.task.done())
