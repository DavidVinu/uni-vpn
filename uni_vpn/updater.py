"""Automatic updates. CI moves the branch "stable" to every green commit on main; the service
follows it. Repository and branch are constants: on Windows the service runs elevated and
writes to Program Files, so nothing the user can change may decide which code it installs."""

from __future__ import annotations

import hashlib
import io
import json
import os
import platform as _platform
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
import urllib.request
import zipfile
import zlib
from pathlib import Path

from . import platform as pf

REPO = "DavidVinu/uni-vpn"
CHANNEL = "stable"
LATEST_URL = f"https://api.github.com/repos/{REPO}/commits/{CHANNEL}"
ARCHIVE_URL = f"https://codeload.github.com/{REPO}/zip/{{commit}}"
GIT_URL = f"https://github.com/{REPO}.git"
# The Go core's release files (docs/go-switch.md). With "handover" set, installing the Go core
# next to bin/uni-vpn moves this installation over to it.
CORE_MANIFEST_URL = f"https://github.com/{REPO}/releases/latest/download/core-manifest.json"
CORE_FILE_URL = f"https://github.com/{REPO}/releases/latest/download/{{name}}"
CORE_MAX_BYTES = 200 * 1024 * 1024
COMMIT_FILE = ".commit"
# Exit code of a daemon that updated itself and wants to be started again (EX_TEMPFAIL).
RESTART_EXIT = 75
SUPERVISED_ENV = "UNI_VPN_SUPERVISED"
_COMMIT = re.compile(r"[0-9a-f]{40}")
# Imports every module of the package from the folder given as argument.
_SMOKE = """
import importlib, os, pkgutil, sys
sys.path.insert(0, sys.argv[1])
import uni_vpn
if not os.path.realpath(uni_vpn.__file__).startswith(os.path.realpath(sys.argv[1]) + os.sep):
    sys.exit("uni_vpn was not imported from " + sys.argv[1])
for module in pkgutil.iter_modules(uni_vpn.__path__):
    importlib.import_module("uni_vpn." + module.name)
"""


class UpdateError(Exception):
    pass


def core_name() -> str | None:
    """The Go core's zip for this computer, None where none is built."""
    system = {"linux": "linux", "darwin": "darwin", "win32": "windows"}.get(sys.platform)
    machine = {"x86_64": "amd64", "amd64": "amd64", "arm64": "arm64", "aarch64": "arm64"}.get(
        _platform.machine().lower())
    return f"uni-vpn-core-{system}-{machine}.zip" if system and machine else None


def core_binary() -> str:
    return "bin/uni-vpn-core.exe" if pf.IS_WINDOWS else "bin/uni-vpn-core"


def core_for(commit: str, opener=None, timeout: float = 20) -> dict | None:
    """{"name", "sha256", "size"} of the Go core to install with commit, when the manifest says
    this installation moves over. Any problem reading it means: stay on Python for now."""
    opener = opener or urllib.request.urlopen
    name = core_name()
    if not name:
        return None
    request = urllib.request.Request(CORE_MANIFEST_URL, headers={"User-Agent": "uni-vpn"})
    try:
        with opener(request, timeout=timeout) as response:
            manifest = json.loads(response.read(1024 * 1024))
        entry = manifest["files"][name]
        if (manifest.get("handover") is not True or manifest.get("commit") != commit
                or not re.fullmatch(r"[0-9a-f]{64}", entry["sha256"])
                or not isinstance(entry["size"], int) or not 0 < entry["size"] <= CORE_MAX_BYTES):
            return None
    except (OSError, ValueError, KeyError, TypeError):
        return None
    return {"name": name, "sha256": entry["sha256"], "size": entry["size"]}


def core_installed(root: Path) -> bool:
    return (root / core_binary()).is_file()


def _no_window() -> dict:
    return {"creationflags": 0x08000000} if pf.IS_WINDOWS else {}  # CREATE_NO_WINDOW


def latest_commit(opener=None, timeout: float = 20) -> str:
    """The commit "stable" points to. One request, the answer is the commit id as plain text."""
    opener = opener or urllib.request.urlopen
    request = urllib.request.Request(LATEST_URL, headers={"Accept": "application/vnd.github.sha",
                                                          "User-Agent": "uni-vpn"})
    try:
        with opener(request, timeout=timeout) as response:
            text = response.read(100).decode("ascii", "replace").strip()
    except OSError as exc:
        raise UpdateError(f"checking for updates failed: {exc}") from None
    if not _COMMIT.fullmatch(text):
        raise UpdateError(f"unexpected answer when checking for updates: {text[:60]!r}")
    return text


def is_git(root: Path) -> bool:
    return (root / ".git").exists()


def _git(root: Path, *args: str, run=subprocess.run) -> subprocess.CompletedProcess:
    try:
        return run(["git", "-C", str(root), *args], capture_output=True, text=True, timeout=120, **_no_window())
    except (OSError, subprocess.SubprocessError) as exc:
        raise UpdateError(f"git {args[0]}: {exc}") from None


def installed_commit(root: Path, run=subprocess.run) -> str | None:
    if is_git(root):
        result = _git(root, "rev-parse", "HEAD", run=run)
        text = (result.stdout or "").strip()
        return text if result.returncode == 0 and _COMMIT.fullmatch(text) else None
    try:
        text = (root / COMMIT_FILE).read_text(encoding="ascii").strip()
    except (OSError, UnicodeDecodeError):
        return None
    return text if _COMMIT.fullmatch(text) else None


def smoke_test(folder: Path, python: str | None = None, run=subprocess.run) -> None:
    """The new code must at least import with the Python the service runs on."""
    try:
        result = run([python or sys.executable, "-I", "-c", _SMOKE, str(folder)], capture_output=True,
                     text=True, timeout=120, **_no_window())
    except (OSError, subprocess.SubprocessError) as exc:
        raise UpdateError(f"testing the new version failed: {exc}") from None
    if result.returncode != 0:
        detail = ((result.stderr or "").strip().splitlines() or ["exit " + str(result.returncode)])[-1]
        raise UpdateError(f"the new version does not start: {detail}")


# --- archive installs ---------------------------------------------------------------------

def archive_files(archive: zipfile.ZipFile, root: Path) -> dict[str, tuple[str, Path]]:
    """Relative path -> (name in the archive, destination). Refuses foreign archives and paths outside root."""
    names = archive.namelist()
    if not names:
        raise ValueError("the download is empty")
    prefix = names[0].split("/", 1)[0] + "/"
    if prefix + "uni_vpn/__init__.py" not in names:
        raise ValueError("the download does not look like uni-vpn")
    files = {}
    for name in names:
        if not name.startswith(prefix):
            raise ValueError(f"unexpected path in the download: {name}")
        relative = name[len(prefix):]
        if not relative or name.endswith("/"):
            continue
        destination = (root / relative).resolve()
        if root not in destination.parents:
            raise ValueError(f"unexpected path in the download: {name}")
        files[relative] = (name, destination)
    return files


def remove_stale(root: Path, keep: set[str]) -> None:
    """Delete files under uni_vpn/ and bin/ that the new version no longer has. Never follows symlinks."""
    fold = str.lower if (pf.IS_MACOS or pf.IS_WINDOWS) else str  # case-insensitive file systems
    keep = {fold(k) for k in keep}
    for top in ("uni_vpn", "bin"):
        base = root / top
        if base.is_symlink() or not base.is_dir():
            continue
        for dirpath, _dirs, filenames in os.walk(base, topdown=False):
            here = Path(dirpath)
            if "__pycache__" in here.relative_to(root).parts:
                continue
            for filename in filenames:
                path = here / filename
                if not path.is_symlink() and fold(path.relative_to(root).as_posix()) not in keep:
                    path.unlink()
            if here != base and not any(here.iterdir()):
                here.rmdir()


class Staged:
    """A new version unpacked next to the program, not yet in place. partial: only some files
    (the Go core), the others stay."""

    def __init__(self, root: Path, folder: Path, files: dict[str, tuple[str, Path]], commit: str | None,
                 partial: bool = False):
        self.root, self.folder, self.files, self.commit, self.partial = root, folder, files, commit, partial

    def install(self) -> None:
        # File by file: on Windows the running service has the program folder as its working
        # directory, so the folder itself cannot be swapped.
        for relative, (_name, destination) in self.files.items():
            destination.parent.mkdir(parents=True, exist_ok=True)
            os.replace(self.folder / relative, destination)
        self.discard()
        if not self.partial:
            remove_stale(self.root, set(self.files))
        if self.commit:
            (self.root / COMMIT_FILE).write_text(self.commit + "\n", encoding="ascii")

    def discard(self) -> None:
        shutil.rmtree(self.folder, ignore_errors=True)


def stage(root: Path, url: str, commit: str | None = None, opener=None, check=None) -> Staged:
    """Download the archive and unpack it into a folder next to root (same file system).
    ValueError for a broken or unexpected download; nothing in root changes."""
    opener = opener or urllib.request.urlopen
    with opener(url, timeout=60) as response:
        data = response.read()
    root = root.resolve()
    folder = None
    try:
        with zipfile.ZipFile(io.BytesIO(data)) as archive:
            files = archive_files(archive, root)
            if archive.testzip() is not None:
                raise ValueError("the download is damaged, try again")
            # GitHub writes the commit id into the zip comment.
            if commit and archive.comment and archive.comment.decode("ascii", "replace").strip() != commit:
                raise ValueError("the download is not the requested version")
            folder = Path(tempfile.mkdtemp(prefix=".uni-vpn-update-", dir=root.parent))
            for relative, (name, _destination) in files.items():
                staged = folder / relative
                staged.parent.mkdir(parents=True, exist_ok=True)
                staged.write_bytes(archive.read(name))
                if relative.startswith("bin/") or relative.endswith(".sh"):
                    staged.chmod(0o755)
        if check:
            check(folder)
    except (zipfile.BadZipFile, zlib.error, EOFError) as exc:
        if folder:
            shutil.rmtree(folder, ignore_errors=True)
        raise ValueError(f"the download is damaged, try again ({exc})") from None
    except BaseException:
        if folder:
            shutil.rmtree(folder, ignore_errors=True)
        raise
    return Staged(root, folder, files, commit)


def stage_core(staged: Staged | None, root: Path, core: dict, commit: str, opener=None, run=subprocess.run) -> Staged:
    """Adds the Go core to staged (or stages it alone): download, check size and SHA-256, unpack
    bin/uni-vpn-core and test that it starts and is the expected version."""
    opener = opener or urllib.request.urlopen
    url = CORE_FILE_URL.format(name=core["name"])
    with opener(urllib.request.Request(url, headers={"User-Agent": "uni-vpn"}), timeout=120) as response:
        data = response.read(core["size"] + 1)
    if len(data) != core["size"] or hashlib.sha256(data).hexdigest() != core["sha256"]:
        raise ValueError("the download is damaged, try again")
    binary = core_binary()
    root = root.resolve()
    folder = staged.folder if staged else Path(tempfile.mkdtemp(prefix=".uni-vpn-update-", dir=root.parent))
    try:
        try:
            with zipfile.ZipFile(io.BytesIO(data)) as archive:
                content = archive.read(binary)
        except (zipfile.BadZipFile, zlib.error, EOFError, KeyError) as exc:
            raise ValueError(f"the download is damaged, try again ({exc})") from None
        target = folder / binary
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(content)
        target.chmod(0o755)
        try:
            result = run([str(target), "--version"], capture_output=True, text=True, timeout=60, **_no_window())
        except (OSError, subprocess.SubprocessError) as exc:
            raise ValueError(f"the new version does not start: {exc}") from None
        if result.returncode != 0 or commit not in (result.stdout or ""):
            raise ValueError("the new version does not start: " + ((result.stdout or "").strip()[:80] or
                                                                   "exit " + str(result.returncode)))
    except BaseException:
        if staged:
            staged.discard()
        else:
            shutil.rmtree(folder, ignore_errors=True)
        raise
    if staged:
        staged.files[binary] = (binary, root / binary)
        return staged
    return Staged(root, folder, {binary: (binary, root / binary)}, commit, partial=True)


# --- the service's updater ---------------------------------------------------------------

class Updater:
    """check() runs in a worker thread and may take a while; apply() only moves files and is
    called by the service once the tunnel is idle."""

    def __init__(self, root: Path | None = None, *, run=subprocess.run, opener=None, python: str | None = None):
        self.root = (root or pf.repo_root()).resolve()
        self.run = run
        self.opener = opener
        self.python = python or sys.executable
        self.pending: str | None = None
        self._staged: Staged | None = None

    def installed(self) -> str | None:
        return installed_commit(self.root, run=self.run)

    def check(self) -> str | None:
        """The new commit, ready to apply, or None when this one is current. Raises UpdateError."""
        if self.pending:
            return self.pending
        if is_git(self.root):
            commit = self._check_git()
        else:
            commit = latest_commit(self.opener)
            current = commit == self.installed()
            core = None if core_installed(self.root) else core_for(commit, self.opener)
            if current and not core:
                return None
            try:
                if not current:
                    self._staged = stage(self.root, ARCHIVE_URL.format(commit=commit), commit, self.opener,
                                         check=lambda folder: smoke_test(folder, self.python, self.run))
                if core:
                    self._staged = stage_core(self._staged, self.root, core, commit, self.opener, self.run)
            except ValueError as exc:
                self.discard()
                raise UpdateError(str(exc)) from None
            except OSError as exc:
                self.discard()
                raise UpdateError(f"downloading the update failed: {exc}") from None
        self.pending = commit
        return commit

    def _check_git(self) -> str | None:
        """Only a clean checkout whose commit is behind stable: a developer's own commits and
        changes are never touched."""
        if _git(self.root, "status", "--porcelain", "--untracked-files=no", run=self.run).stdout.strip():
            return None
        if _git(self.root, "fetch", "--quiet", GIT_URL, CHANNEL, run=self.run).returncode:
            raise UpdateError("git fetch failed")
        commit = _git(self.root, "rev-parse", "FETCH_HEAD", run=self.run).stdout.strip()
        head = self.installed()
        if not _COMMIT.fullmatch(commit) or commit == head:
            return None
        if _git(self.root, "merge-base", "--is-ancestor", "HEAD", commit, run=self.run).returncode:
            return None
        # Test the new code from an export, before the checkout moves.
        folder = Path(tempfile.mkdtemp(prefix="uni-vpn-update-"))
        try:
            try:
                archive = self.run(["git", "-C", str(self.root), "archive", "--format=tar", commit, "uni_vpn", "bin"],
                                   capture_output=True, timeout=120, **_no_window())
                with tarfile.open(fileobj=io.BytesIO(archive.stdout)) as tar:
                    # Our own repository; the filter only exists from Python 3.11.4 on.
                    if hasattr(tarfile, "data_filter"):
                        tar.extractall(folder, filter="data")
                    else:
                        tar.extractall(folder)
            except (OSError, subprocess.SubprocessError, tarfile.TarError) as exc:
                raise UpdateError(f"git archive failed: {exc}") from None
            smoke_test(folder, self.python, self.run)
        finally:
            shutil.rmtree(folder, ignore_errors=True)
        return commit

    def apply(self) -> str:
        commit = self.pending
        if not commit:
            raise UpdateError("no update pending")
        try:
            if self._staged:
                self._staged.install()
            elif _git(self.root, "merge", "--ff-only", "--quiet", commit, run=self.run).returncode:
                raise UpdateError("git merge --ff-only failed")
        except OSError as exc:
            raise UpdateError(f"installing the update failed: {exc}") from None
        finally:
            self.pending, self._staged = None, None
        return commit

    def discard(self) -> None:
        if self._staged:
            self._staged.discard()
        self.pending, self._staged = None, None


def update_now(root: Path, opener=None, run=subprocess.run) -> str | None:
    """For "uni-vpn update" in an installation without git: the current stable version.
    The new commit, or None when this one is current. Raises UpdateError."""
    updater = Updater(root, run=run, opener=opener)
    commit = updater.check()
    if commit:
        updater.apply()
    return commit
