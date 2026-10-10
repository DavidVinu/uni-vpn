import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from uni_vpn import handover

ROOT = Path(__file__).resolve().parent.parent


def install(core: bool):
    root = Path(tempfile.mkdtemp())
    (root / "bin").mkdir()
    if core:
        path = Path(handover.core_path(str(root)))
        path.write_text("#!/bin/sh\n")
        path.chmod(0o755)
    return root


class HandoverTests(unittest.TestCase):
    def test_without_the_go_core_python_carries_on(self):
        self.assertIsNone(handover.handover(str(install(False)), ["daemon"], environ={},
                                            execv=lambda *a: self.fail("exec")))

    def test_posix_replaces_the_process(self):
        root = install(True)
        calls = []
        rc = handover.handover(str(root), ["daemon"], environ={}, windows=False,
                               execv=lambda exe, argv: calls.append((exe, argv)))
        core = handover.core_path(str(root))
        self.assertEqual(calls, [(core, [core, "daemon"])])
        self.assertEqual(rc, 1)

    def test_switched_off(self):
        root = install(True)
        self.assertIsNone(handover.handover(str(root), ["daemon"], environ={handover.DISABLE_ENV: "1"},
                                            execv=lambda *a: self.fail("exec")))

    def test_windows_passes_the_exit_code_on(self):
        root = install(True)
        calls = []

        def call(command, **kwargs):
            calls.append((command, kwargs))
            return 75

        rc = handover.handover(str(root), ["daemon"], environ={}, windows=True, call=call, has_console=False)
        self.assertEqual(rc, 75)  # the supervisor in cli.restart_daemon starts it again
        self.assertEqual(calls[0][0][1:], ["daemon"])
        self.assertEqual(calls[0][1], {"creationflags": handover.CREATE_NO_WINDOW})
        handover.handover(str(root), ["setup"], environ={}, windows=True, call=call, has_console=True)
        self.assertEqual(calls[1][1], {})

    @unittest.skipIf(sys.platform == "win32", "needs exec")
    def test_launcher_runs_the_go_core(self):
        root = install(False)
        (root / "bin" / "uni-vpn").write_text((ROOT / "bin" / "uni-vpn").read_text())
        os.symlink(ROOT / "uni_vpn", root / "uni_vpn")
        core = Path(handover.core_path(str(root)))
        core.write_text('#!/bin/sh\necho "go core $*"\n')
        core.chmod(0o755)
        env = dict(os.environ)
        env.pop(handover.DISABLE_ENV, None)
        out = subprocess.run([sys.executable, "-I", str(root / "bin" / "uni-vpn"), "daemon", "--x"],
                             capture_output=True, text=True, env=env, timeout=30)
        self.assertEqual(out.stdout, "go core daemon --x\n")
        core.unlink()
        out = subprocess.run([sys.executable, "-I", str(root / "bin" / "uni-vpn"), "--version"],
                             capture_output=True, text=True, env=env, timeout=30)
        self.assertTrue(out.stdout.startswith("uni-vpn "), out)


if __name__ == "__main__":
    unittest.main()
