import asyncio
import hashlib
import io
import json
import logging
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
import urllib.error
import zipfile
from pathlib import Path
from unittest import mock

from uni_vpn import cli, config, updater

from tests.test_daemon import DaemonHarness
from tests.test_httpapi import http

A = "a" * 40
B = "b" * 40
GOOD = {"uni_vpn/__init__.py": "__version__ = '9'\n", "uni_vpn/extra.py": "import uni_vpn\n", "bin/uni-vpn": "#!/bin/sh\n"}


class Response(io.BytesIO):
    def __enter__(self):
        return self

    def __exit__(self, *exc):
        return False


def make_zip(files, comment=b""):
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w") as zf:
        zf.writestr("uni-vpn-x/", "")
        for name, data in files.items():
            zf.writestr("uni-vpn-x/" + name, data)
        zf.comment = comment
    return buf.getvalue()


def github(stable=B, files=None, comment=None, release=None):
    """Opener for api.github.com, codeload.github.com and the release files ({name: bytes}).
    Records the URLs it was asked for."""
    asked = []
    release = release or {}

    def opener(request, timeout=None):
        url = getattr(request, "full_url", request)
        asked.append(url)
        for name, data in release.items():
            if url == updater.CORE_FILE_URL.format(name=name):
                return Response(data)
        if url.startswith(updater.CORE_FILE_URL.format(name="")):
            raise urllib.error.HTTPError(url, 404, "Not Found", None, None)
        if url == updater.LATEST_URL:
            if isinstance(stable, Exception):
                raise stable
            return Response(stable.encode())
        if url == updater.ARCHIVE_URL.format(commit=stable):
            return Response(make_zip(files or GOOD, (comment if comment is not None else stable).encode()))
        raise AssertionError(url)

    opener.asked = asked
    return opener


def app(commit=A):
    root = Path(tempfile.mkdtemp()) / "app"
    (root / "uni_vpn").mkdir(parents=True)
    (root / "uni_vpn" / "__init__.py").write_text("__version__ = '1'\n")
    (root / "uni_vpn" / "gone.py").write_text("")
    if commit:
        (root / updater.COMMIT_FILE).write_text(commit + "\n")
    return root


class LatestTests(unittest.TestCase):
    def test_commit_as_plain_text(self):
        self.assertEqual(updater.latest_commit(github()), B)

    def test_garbage_and_network_errors(self):
        with self.assertRaises(updater.UpdateError):
            updater.latest_commit(github(stable='{"message": "Not Found"}'))
        with self.assertRaises(updater.UpdateError):
            updater.latest_commit(github(stable=OSError("offline")))

    def test_source_is_fixed(self):
        # Never from config.toml: the Windows service installs this code elevated.
        self.assertEqual(updater.LATEST_URL, "https://api.github.com/repos/DavidVinu/uni-vpn/commits/stable")
        self.assertTrue(updater.ARCHIVE_URL.startswith("https://codeload.github.com/DavidVinu/uni-vpn/zip/"))


class ArchiveUpdaterTests(unittest.TestCase):
    def test_installed_commit(self):
        self.assertEqual(updater.installed_commit(app(A)), A)
        self.assertIsNone(updater.installed_commit(app(None)))
        root = app(None)
        (root / updater.COMMIT_FILE).write_text("../../etc\n")
        self.assertIsNone(updater.installed_commit(root))

    def test_check_stages_and_apply_installs(self):
        root = app(A)
        u = updater.Updater(root, opener=github())
        self.assertEqual(u.check(), B)
        # Nothing in place yet: the service applies once the tunnel is idle.
        self.assertEqual((root / "uni_vpn" / "__init__.py").read_text(), "__version__ = '1'\n")
        self.assertEqual(u.pending, B)
        self.assertEqual(u.apply(), B)
        self.assertEqual((root / "uni_vpn" / "__init__.py").read_text(), GOOD["uni_vpn/__init__.py"])
        self.assertFalse((root / "uni_vpn" / "gone.py").exists())
        self.assertEqual(updater.installed_commit(root), B)
        self.assertIsNone(u.pending)
        self.assertEqual(sorted(p.name for p in root.parent.iterdir()), ["app"])

    def test_current_version_downloads_nothing(self):
        opener = github(stable=A)
        self.assertIsNone(updater.Updater(app(A), opener=opener).check())
        # Besides the commit only the Go core's manifest, which says whether to move over.
        self.assertEqual(opener.asked, [updater.LATEST_URL, updater.CORE_MANIFEST_URL])

    def test_unknown_installed_commit_updates_once(self):
        root = app(None)
        u = updater.Updater(root, opener=github())
        self.assertEqual(u.check(), B)
        u.apply()
        self.assertIsNone(updater.Updater(root, opener=github()).check())

    def test_code_that_does_not_import_changes_nothing(self):
        root = app(A)
        broken = dict(GOOD, **{"uni_vpn/extra.py": "import does_not_exist_anywhere\n"})
        with self.assertRaises(updater.UpdateError) as ctx:
            updater.Updater(root, opener=github(files=broken)).check()
        self.assertIn("does not start", str(ctx.exception))
        self.assertEqual((root / "uni_vpn" / "__init__.py").read_text(), "__version__ = '1'\n")
        self.assertEqual(sorted(p.name for p in root.parent.iterdir()), ["app"])

    def test_wrong_commit_in_the_archive_is_refused(self):
        root = app(A)
        with self.assertRaises(updater.UpdateError):
            updater.Updater(root, opener=github(comment="c" * 40)).check()
        self.assertEqual(sorted(p.name for p in root.parent.iterdir()), ["app"])

    def test_discard_removes_the_staged_files(self):
        root = app(A)
        u = updater.Updater(root, opener=github())
        u.check()
        u.discard()
        self.assertIsNone(u.pending)
        self.assertEqual(sorted(p.name for p in root.parent.iterdir()), ["app"])
        with self.assertRaises(updater.UpdateError):
            u.apply()

    def test_update_now(self):
        root = app(A)
        self.assertEqual(updater.update_now(root, opener=github()), B)
        self.assertIsNone(updater.update_now(root, opener=github()))


@unittest.skipUnless(shutil.which("git"), "needs git")
class GitUpdaterTests(unittest.TestCase):
    def git(self, cwd, *args):
        env = dict(os.environ, GIT_AUTHOR_NAME="t", GIT_AUTHOR_EMAIL="t@t", GIT_COMMITTER_NAME="t",
                   GIT_COMMITTER_EMAIL="t@t", GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_NOSYSTEM="1")
        return subprocess.run(["git", "-C", str(cwd), *args], check=True, capture_output=True, text=True,
                              env=env).stdout.strip()

    def setUp(self):
        base = Path(tempfile.mkdtemp())
        self.upstream = base / "upstream"
        (self.upstream / "uni_vpn").mkdir(parents=True)
        (self.upstream / "bin").mkdir()
        (self.upstream / "bin" / "uni-vpn").write_text("#!/bin/sh\n")
        (self.upstream / "uni_vpn" / "__init__.py").write_text("V = 1\n")
        self.git(self.upstream, "init", "-q", "-b", "main")
        self.git(self.upstream, "add", ".")
        self.git(self.upstream, "commit", "-qm", "one")
        self.clone = base / "clone"
        self.git(base, "clone", "-q", str(self.upstream), str(self.clone))
        (self.upstream / "uni_vpn" / "__init__.py").write_text("V = 2\n")
        self.git(self.upstream, "commit", "-qam", "two")
        self.git(self.upstream, "branch", "stable")
        self.new = self.git(self.upstream, "rev-parse", "HEAD")
        patcher = mock.patch.object(updater, "GIT_URL", str(self.upstream))
        patcher.start()
        self.addCleanup(patcher.stop)

    def test_clean_checkout_behind_stable_fast_forwards(self):
        u = updater.Updater(self.clone)
        self.assertEqual(u.check(), self.new)
        self.assertEqual((self.clone / "uni_vpn" / "__init__.py").read_text(), "V = 1\n")
        u.apply()
        self.assertEqual((self.clone / "uni_vpn" / "__init__.py").read_text(), "V = 2\n")
        self.assertEqual(updater.installed_commit(self.clone), self.new)
        self.assertIsNone(updater.Updater(self.clone).check())

    def test_local_changes_are_never_touched(self):
        (self.clone / "uni_vpn" / "__init__.py").write_text("V = 'mine'\n")
        self.assertIsNone(updater.Updater(self.clone).check())

    def test_own_commits_are_never_touched(self):
        (self.clone / "uni_vpn" / "mine.py").write_text("")
        self.git(self.clone, "add", ".")
        self.git(self.clone, "commit", "-qm", "mine")
        self.assertIsNone(updater.Updater(self.clone).check())

    def test_broken_stable_is_not_merged(self):
        (self.upstream / "uni_vpn" / "__init__.py").write_text("this is not python\n")
        self.git(self.upstream, "commit", "-qam", "broken")
        self.git(self.upstream, "branch", "-f", "stable")
        with self.assertRaises(updater.UpdateError):
            updater.Updater(self.clone).check()
        self.assertEqual((self.clone / "uni_vpn" / "__init__.py").read_text(), "V = 1\n")


class FakeUpdater:
    def __init__(self, commit=B, error=None):
        self.commit, self.error = commit, error
        self.pending = None
        self.checks = self.applied = self.discarded = 0

    def installed(self):
        return A

    def check(self):
        self.checks += 1
        if self.error:
            raise self.error
        self.pending = self.commit
        return self.commit

    def apply(self):
        self.applied += 1
        commit, self.pending = self.pending, None
        return commit

    def discard(self):
        self.discarded += 1
        self.pending = None


async def until(predicate, timeout=3):
    deadline = asyncio.get_running_loop().time() + timeout
    while not predicate():
        if asyncio.get_running_loop().time() > deadline:
            raise AssertionError("timed out")
        await asyncio.sleep(0.02)


class DaemonUpdateTests(DaemonHarness):
    async def test_idle_service_installs_and_asks_for_a_restart(self):
        fake = FakeUpdater()
        d = await self.start_daemon(updater=fake, update_first_check=0.05)
        await asyncio.wait_for(self.task, 3)
        self.assertEqual(fake.applied, 1)
        self.assertTrue(d.restart_requested)
        self.assertEqual(d.commit, A)
        self.daemon = None

    async def test_waits_while_the_tunnel_is_wanted(self):
        fake = FakeUpdater()
        d = await self.start_daemon(updater=fake, update_first_check=0.05)
        d.explicit = True
        await until(lambda: fake.checks == 1)
        await asyncio.sleep(0.4)
        self.assertEqual(fake.applied, 0)
        self.assertFalse(d.restart_requested)
        self.assertEqual(d.status()["update_pending"], B)
        d.explicit = False
        await asyncio.wait_for(self.task, 3)
        self.assertEqual(fake.applied, 1)
        self.daemon = None

    async def test_switched_off_checks_nothing(self):
        self.cfg.auto_update = False
        fake = FakeUpdater()
        await self.start_daemon(updater=fake, update_first_check=0.05)
        await asyncio.sleep(0.3)
        self.assertEqual(fake.checks, 0)

    async def test_a_failed_check_keeps_the_service_running(self):
        fake = FakeUpdater(error=updater.UpdateError("offline"))
        d = await self.start_daemon(updater=fake, update_first_check=0.05, update_interval=0.05, update_jitter=0)
        await until(lambda: fake.checks >= 2)
        self.assertFalse(self.task.done())
        self.assertFalse(d.restart_requested)

    async def test_switch_in_settings(self):
        path = Path(tempfile.mkdtemp()) / "config.toml"
        config.write_initial(path, "u")
        fake = FakeUpdater(commit=None)
        d = await self.start_daemon(updater=fake, config_path=path)
        self.assertTrue(json.loads((await http(self.cfg.http_port, "GET", "/status.json"))[2])["auto_update"])
        headers = {"X-Uni-VPN": "1", "Content-Type": "application/json"}
        status, _, _ = await http(self.cfg.http_port, "POST", "/api/auto-update", headers, b'{"enabled": false}')
        self.assertEqual(status, 200)
        self.assertFalse(config.load(path).auto_update)
        self.assertFalse(d.status()["auto_update"])
        status, _, _ = await http(self.cfg.http_port, "POST", "/api/auto-update", headers, b'{"enabled": "no"}')
        self.assertEqual(status, 400)
        # Switching it on checks right away, like Tailscale.
        status, _, _ = await http(self.cfg.http_port, "POST", "/api/auto-update", headers, b'{"enabled": true}')
        self.assertEqual(status, 200)
        self.assertTrue(config.load(path).auto_update)
        await until(lambda: fake.checks == 1)

    async def test_switch_needs_a_config(self):
        await self.start_daemon(updater=FakeUpdater(commit=None), config_path=Path(tempfile.mkdtemp()) / "none.toml")
        headers = {"X-Uni-VPN": "1", "Content-Type": "application/json"}
        status, _, _ = await http(self.cfg.http_port, "POST", "/api/auto-update", headers, b'{"enabled": false}')
        self.assertEqual(status, 409)


class ConfigTests(unittest.TestCase):
    def test_auto_update_key(self):
        path = Path(tempfile.mkdtemp()) / "config.toml"
        config.write_initial(path, "u")
        self.assertTrue(config.load(path).auto_update)
        config.set_values(path, {"auto_update": False})
        self.assertFalse(config.load(path).auto_update)
        path.write_text(path.read_text().replace("auto_update = false", 'auto_update = "no"'))
        with self.assertRaises(config.ConfigError):
            config.load(path)


CORE = "uni-vpn-core-linux-amd64.zip"


def core_zip(content=b"\x7fELF go core"):
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w") as zf:
        zf.writestr("bin/uni-vpn-core", content)
    return buf.getvalue()


def release(commit=B, handover=True, data=None, sha=None, size=None):
    data = data if data is not None else core_zip()
    manifest = {"commit": commit, "handover": handover,
                "files": {CORE: {"sha256": sha or hashlib.sha256(data).hexdigest(),
                                 "size": size if size is not None else len(data)}}}
    return {"core-manifest.json": json.dumps(manifest).encode(), CORE: data}


def runner(version_output):
    """subprocess.run for the updater: the Go core's --version answers version_output, the
    Python smoke test runs for real."""
    calls = []

    def run(command, **kwargs):
        if command[-1] == "--version":
            calls.append(command)
            return subprocess.CompletedProcess(command, 0, version_output, "")
        return subprocess.run(command, **kwargs)

    run.calls = calls
    return run


@mock.patch.object(updater, "core_name", lambda: CORE)
@mock.patch.object(updater.pf, "IS_WINDOWS", False)
class CoreHandoverTests(unittest.TestCase):
    def updater_for(self, root, rel, version=f"uni-vpn 0.1.0 {B}\n", stable=B):
        self.run_ = runner(version)
        return updater.Updater(root, opener=github(stable=stable, release=rel), run=self.run_)

    def test_without_handover_python_updates_as_before(self):
        root = app(A)
        u = self.updater_for(root, release(handover=False))
        self.assertEqual(u.check(), B)
        u.apply()
        self.assertFalse((root / "bin" / "uni-vpn-core").exists())
        self.assertEqual(self.run_.calls, [])

    def test_handover_installs_the_go_core_with_the_new_version(self):
        root = app(A)
        u = self.updater_for(root, release())
        self.assertEqual(u.check(), B)
        self.assertFalse((root / "bin" / "uni-vpn-core").exists())
        u.apply()
        core = root / "bin" / "uni-vpn-core"
        self.assertEqual(core.read_bytes(), b"\x7fELF go core")
        self.assertTrue(os.access(core, os.X_OK))
        self.assertEqual((root / "uni_vpn" / "__init__.py").read_text(), GOOD["uni_vpn/__init__.py"])
        self.assertEqual(updater.installed_commit(root), B)
        self.assertEqual(len(self.run_.calls), 1)
        self.assertEqual(sorted(p.name for p in root.parent.iterdir()), ["app"])

    def test_current_python_still_moves_over(self):
        root = app(B)
        u = self.updater_for(root, release())
        self.assertEqual(u.check(), B)
        u.apply()
        self.assertTrue((root / "bin" / "uni-vpn-core").exists())
        # Only the Go core was added; nothing of the Python version was touched or removed.
        self.assertTrue((root / "uni_vpn" / "gone.py").exists())
        self.assertEqual((root / "uni_vpn" / "__init__.py").read_text(), "__version__ = '1'\n")

    def test_once_moved_over_nothing_more_happens(self):
        root = app(B)
        (root / "bin").mkdir()
        (root / "bin" / "uni-vpn-core").write_bytes(b"old")
        opener = github(release=release())
        self.assertIsNone(updater.Updater(root, opener=opener).check())
        self.assertEqual(opener.asked, [updater.LATEST_URL])

    def test_manifest_of_another_commit_waits(self):
        root = app(B)
        self.assertIsNone(self.updater_for(root, release(commit="c" * 40)).check())

    def test_missing_manifest_waits(self):
        root = app(B)
        self.assertIsNone(self.updater_for(root, {}).check())

    def test_damaged_download_changes_nothing(self):
        for rel in (release(sha="0" * 64), release(size=5), release(data=b"not a zip")):
            root = app(A)
            with self.assertRaises(updater.UpdateError):
                self.updater_for(root, rel).check()
            self.assertFalse((root / "bin").exists())
            self.assertEqual((root / "uni_vpn" / "__init__.py").read_text(), "__version__ = '1'\n")
            self.assertEqual(sorted(p.name for p in root.parent.iterdir()), ["app"])

    def test_core_that_does_not_start_changes_nothing(self):
        root = app(A)
        with self.assertRaises(updater.UpdateError) as ctx:
            self.updater_for(root, release(), version="uni-vpn 0.1.0 " + "c" * 40).check()
        self.assertIn("does not start", str(ctx.exception))
        self.assertEqual(sorted(p.name for p in root.parent.iterdir()), ["app"])


class CoreNameTests(unittest.TestCase):
    def test_names(self):
        cases = [("linux", "x86_64", "uni-vpn-core-linux-amd64.zip"), ("linux", "aarch64", "uni-vpn-core-linux-arm64.zip"),
                 ("darwin", "arm64", "uni-vpn-core-darwin-arm64.zip"), ("win32", "AMD64", "uni-vpn-core-windows-amd64.zip"),
                 ("linux", "riscv64", None), ("freebsd14", "amd64", None)]
        for system, machine, name in cases:
            with mock.patch.object(updater.sys, "platform", system), \
                    mock.patch.object(updater._platform, "machine", lambda m=machine: m):
                self.assertEqual(updater.core_name(), name, (system, machine))

    def test_source_is_fixed(self):
        self.assertEqual(updater.CORE_MANIFEST_URL,
                         "https://github.com/DavidVinu/uni-vpn/releases/latest/download/core-manifest.json")


class RestartTests(unittest.TestCase):
    def test_posix_replaces_the_process(self):
        calls = []
        with mock.patch.object(cli.pf, "IS_WINDOWS", False), \
                mock.patch.object(sys, "orig_argv", ["python3", "-I", "/app/bin/uni-vpn", "daemon"]):
            cli.restart_daemon(execv=lambda exe, argv: calls.append((exe, argv)))
        self.assertEqual(calls, [(sys.executable, [sys.executable, "-I", "/app/bin/uni-vpn", "daemon"])])

    def test_windows_supervises_the_new_code(self):
        codes = [updater.RESTART_EXIT, updater.RESTART_EXIT, 0]
        calls = []

        def call(command, env):
            calls.append((command, env.get(updater.SUPERVISED_ENV)))
            return codes.pop(0)

        with mock.patch.object(cli.pf, "IS_WINDOWS", True), \
                mock.patch.object(sys, "orig_argv", ["pythonw.exe", "-I", "C:\\uni-vpn\\bin\\uni-vpn", "daemon"]):
            rc = cli.restart_daemon(call=call, environ={})
        self.assertEqual(rc, 0)
        self.assertEqual(len(calls), 3)
        self.assertEqual(calls[0], ([sys.executable, "-I", "C:\\uni-vpn\\bin\\uni-vpn", "daemon"], "1"))

    def test_windows_child_hands_back_to_its_supervisor(self):
        with mock.patch.object(cli.pf, "IS_WINDOWS", True):
            rc = cli.restart_daemon(call=lambda *a, **k: self.fail("nested"), environ={updater.SUPERVISED_ENV: "1"})
        self.assertEqual(rc, updater.RESTART_EXIT)


if __name__ == "__main__":
    logging.basicConfig()
    unittest.main()
