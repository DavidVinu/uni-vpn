"""Set up, remove, update. Called by install.sh and the CLI."""

from __future__ import annotations

import getpass
import os
import shutil
import subprocess
import sys
import time
import xml.etree.ElementTree as ET
from pathlib import Path

from . import config, credentials, doctor, service, sysproxy, totp
from . import platform as pf
from .tunnel import port_open as _port_open

INSTALLED_FILES = "installed-files.txt"
TOTP_HINT = """   Second factor: in the MFA portal https://mfa.uni-heidelberg.de (reachable only on the university network
   or via VPN), set up another token under "Soft-Token (zeitbasiert)", click "Tokendetails einblenden" and copy
   the text between secret= and &issuer=. The app on your phone stays as a second token."""
FINAL_HINT = """
Status page (state, connect/disconnect, domain list): http://127.0.0.1:{port}/
Restart any open browser once so that it reads the proxy rule.
"""
MANUAL_PROXY_HINT = """   The proxy rule could not be registered automatically (no GNOME, KDE, macOS or Windows proxy settings found).
   Enter it by hand in the browser: Settings -> Network/Proxy -> automatic proxy configuration (PAC):
   {url}"""
MANUAL_PROXY_FAILED = """   Registering the proxy rule with the system failed. Enter it by hand in the browser:
   Settings -> Network/Proxy -> automatic proxy configuration (PAC): {url}"""


def _records_path() -> Path:
    return pf.config_dir() / INSTALLED_FILES


def recorded() -> list[Path]:
    path = _records_path()
    if not path.exists():
        return []
    return [Path(line) for line in path.read_text(encoding="utf-8").splitlines() if line.strip()]


def record(paths: list[Path]) -> None:
    existing = recorded()
    merged = existing + [p for p in paths if p not in existing]
    _records_path().parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    _records_path().write_text("\n".join(str(p) for p in merged) + "\n", encoding="utf-8")


def apport_ignore_path() -> Path:
    return Path.home() / ".apport-ignore.xml"


def ensure_apport_ignore(executable: str, dry_run: bool = False) -> Path | None:
    """Entry like apport's mark_ignore(): <ignore program=... mtime=...>. Returns the path if the file was newly created."""
    path = apport_ignore_path()
    created = not path.exists()
    if dry_run:
        print(f"-> would add {executable} to {path}")
        return None
    if created:
        root = ET.Element("apport")
    else:
        try:
            root = ET.parse(path).getroot()
        except ET.ParseError:
            root = ET.Element("apport")
    for entry in root.findall("ignore"):
        if entry.get("program") == executable:
            return path if created else None
    try:
        mtime = str(int(os.stat(executable).st_mtime))
    except OSError:
        mtime = "0"
    ET.SubElement(root, "ignore", program=executable, mtime=mtime)
    path.write_text('<?xml version="1.0"?>\n' + ET.tostring(root, encoding="unicode") + "\n", encoding="utf-8")
    return path if created else None


def _say(text: str) -> None:
    print(f"-> {text}")


def wait_for_port(port: int, *, port_open=_port_open, timeout: float = 5.0, step: float = 0.25,
                  sleep=None, clock=None) -> bool:
    """Waits until the daemon has bound the port after the service start. True as soon as it is reachable."""
    sleep = sleep or time.sleep
    clock = clock or time.monotonic
    deadline = clock() + timeout
    while True:
        if port_open(port):
            return True
        if clock() >= deadline:
            return False
        sleep(step)


def _install_hint() -> str:
    if pf.IS_WINDOWS:
        return "run install.ps1 again"
    if pf.IS_MACOS:
        return "brew install openconnect ocproxy, or run install.sh again"
    return "run install.sh again (Debian/Ubuntu: sudo apt install openconnect ocproxy libsecret-tools)"


def needed_programs() -> list[str]:
    return ["openconnect"] + pf.tunnel_helpers() + ([] if (pf.IS_MACOS or pf.IS_WINDOWS) else ["secret-tool"])


MACOS_BIN_DIRS = ("/opt/homebrew/bin", "/usr/local/bin")


def command_path() -> Path:
    if pf.IS_WINDOWS:
        return pf.config_dir() / "bin" / "uni-vpn.cmd"
    if pf.IS_MACOS:
        # ~/.local/bin is not on the PATH on macOS; Homebrew's bin is.
        for directory in MACOS_BIN_DIRS:
            if os.path.isdir(directory) and os.access(directory, os.W_OK):
                return Path(directory) / "uni-vpn"
    return Path.home() / ".local" / "bin" / "uni-vpn"


def cmd_bytes(text: str, codec: str = "oem") -> bytes:
    """cmd.exe reads batch files in the OEM code page. Paths it cannot encode: UTF-8 with chcp 65001."""
    try:
        return text.encode(codec)
    except (LookupError, UnicodeEncodeError):  # "oem" exists only on Windows
        return ("@chcp 65001 >nul\r\n" + text).encode("utf-8")


def install_command(dry: bool, created) -> None:
    """`uni-vpn` on the PATH, always running the interpreter used for setup (otherwise on macOS
    /usr/bin/python3 3.9 if Homebrew is not first in PATH)."""
    link = command_path()
    target = pf.bin_dir() / "uni-vpn"
    if pf.IS_WINDOWS:
        wrapper = f'@echo off\r\n"{pf.python_executable()}" "{target}" %*\r\n'
    else:
        wrapper = f'#!/bin/sh\nexec "{pf.python_executable()}" "{target}" "$@"\n'
    if dry:
        _say(f"would create {link} as a wrapper for {target}")
        return
    link.parent.mkdir(parents=True, exist_ok=True)
    if link.is_symlink() or link.exists():
        link.unlink()
    if pf.IS_WINDOWS:
        link.write_bytes(cmd_bytes(wrapper))
    else:
        link.write_text(wrapper, encoding="utf-8", newline="")
    link.chmod(0o755)
    created(link)
    _say(f"Command created: {link}")
    if pf.IS_WINDOWS:
        from . import windows

        if windows.add_user_path(str(link.parent)):
            print("   Open a new terminal to use the uni-vpn command")
    elif str(link.parent) not in os.environ.get("PATH", "").split(os.pathsep):
        if pf.IS_MACOS:
            print(f"   Note: {link.parent} is not in PATH, add it to PATH to use the uni-vpn command")
        else:
            # Ubuntu's ~/.profile adds ~/.local/bin at login once it exists.
            print(f"   Note: {link.parent} is not in PATH yet, the uni-vpn command works after logging out and in")


def launcher_path() -> Path:
    if pf.IS_WINDOWS:
        from .windows import start_menu_dir

        return Path(start_menu_dir()) / "Uni VPN.url"
    if pf.IS_MACOS:
        return Path.home() / "Applications" / "Uni VPN.app"
    base = os.environ.get("XDG_DATA_HOME") or str(Path.home() / ".local" / "share")
    return Path(base) / "applications" / "uni-vpn.desktop"


def install_launcher(port: int, dry: bool, created, run=subprocess.run) -> None:
    """An app entry in the start menu, Launchpad or app grid that opens the status page."""
    url = f"http://127.0.0.1:{int(port)}/"
    path = launcher_path()
    if dry:
        _say(f"would add {path} to open {url}")
        return
    path.parent.mkdir(parents=True, exist_ok=True)
    if pf.IS_WINDOWS:
        path.write_text(f"[InternetShortcut]\r\nURL={url}\r\n", encoding="utf-8", newline="")
    elif pf.IS_MACOS:
        result = run(["osacompile", "-o", str(path), "-e", f'open location "{url}"'], capture_output=True, text=True)
        if result.returncode != 0:
            print(f"   App entry could not be created: {(result.stderr or '').strip()}")
            return
    else:
        path.write_text("[Desktop Entry]\nType=Application\nName=Uni VPN\nComment=University VPN for selected websites\n"
                        f"Exec=xdg-open {url}\nIcon=network-vpn\nCategories=Network;\nTerminal=false\n", encoding="utf-8")
    created(path)
    _say(f"App entry created: {path}")


def setup(args, *, input_fn=input, getpass_fn=getpass.getpass, service_install=service.install,
          store=credentials.store_password, store_totp=credentials.store_totp,
          keyring_probe=doctor.keyring_state, proxy_install=sysproxy.install, run_doctor=True,
          port_open=_port_open, open_url=None, has_desktop=None) -> int:
    dry = bool(getattr(args, "dry_run", False))
    open_url = open_url or pf.open_url
    has_desktop = has_desktop or pf.has_desktop
    # With a desktop the browser does the rest (setup assistant); otherwise ask here.
    gui = not dry and not getattr(args, "no_gui", False) and has_desktop()

    def created(path: Path) -> None:
        # Record right away so that an abort further down leaves nothing unrecorded behind.
        record([path])

    if sys.version_info < (3, 11):
        print(f"Python {sys.version_info.major}.{sys.version_info.minor} is too old, at least 3.11 is required")
        return 1

    missing = [name for name in needed_programs() if not pf.find_binary(name)]
    if missing:
        if dry:
            _say(f"would require: {', '.join(missing)} ({_install_hint()})")
        else:
            print(f"Missing: {', '.join(missing)}. To install: {_install_hint()}")
            return 1
    else:
        _say(f"{' and '.join(needed_programs()[:2])} found")

    cfg_path = config.default_path()
    user = (getattr(args, "user", None) or "").strip()
    broken = None
    configured = cfg_path.exists()
    if configured:
        try:
            cfg = config.load(cfg_path)
        except config.ConfigError as exc:
            # An update must still go through; the file stays as it is and the daemon reports the error.
            broken = exc
            cfg = config.Config()
            print(f"Config {cfg_path} is invalid ({exc}), please fix it; continuing with defaults")
        else:
            _say(f"Config found: {cfg_path} (university ID {cfg.user})")
        if user and broken is None and user != cfg.user:
            if not config.valid_user(user):
                print(f"Not a university ID: {user!r}")
                return 1
            if dry:
                _say(f"would change the university ID to {user}")
            else:
                config.set_user(cfg_path, user)
                cfg = config.load(cfg_path)
                _say(f"University ID changed to {user}")
    elif gui and not user:
        cfg = config.Config()
    else:
        if not user:
            if dry and not sys.stdin.isatty():
                user = "example"
            else:
                user = input_fn("University ID (e.g. ab123): ").strip()
        if not user:
            print("No university ID given")
            return 1
        if not config.valid_user(user):
            print(f"Not a university ID: {user!r}")
            return 1
        if dry:
            _say(f"would create {cfg_path} with university ID {user}")
            cfg = config.Config(user=user)
        else:
            config.write_initial(cfg_path, user=user)
            created(cfg_path)
            cfg = config.load(cfg_path)
            _say(f"Config created: {cfg_path}")
    if gui and not cfg_path.exists():
        # The setup assistant writes it; record it now so uninstall removes it.
        created(cfg_path)

    install_command(dry, created)

    if not (pf.IS_MACOS or pf.IS_WINDOWS):
        openconnect = pf.find_binary("openconnect", cfg.openconnect) or "/usr/sbin/openconnect"
        new_file = ensure_apport_ignore(openconnect, dry_run=dry)
        if new_file:
            created(new_file)
        if not dry:
            _say("Crash reports for openconnect disabled (~/.apport-ignore.xml)")

    try:
        service_files = service_install(dry_run=dry)
    except service.ServiceError as exc:
        for path in exc.files:
            created(path)
        print(f"Service could not be loaded: {exc}")
        return 1
    if not dry:
        for path in service_files:
            created(path)
        _say("Service set up and started")

    if dry:
        _say(f"would register the proxy rule {sysproxy.pac_url(cfg.http_port)} with the system (the previous setting is backed up)")
    else:
        result = proxy_install(cfg.http_port)
        if result == "unavailable":
            print(MANUAL_PROXY_HINT.format(url=sysproxy.pac_url(cfg.http_port)))
        elif result == "failed":
            print(MANUAL_PROXY_FAILED.format(url=sysproxy.pac_url(cfg.http_port)))
        elif result == "replaced":
            _say("Proxy rule registered with the system; an existing proxy setting was replaced (backed up, uninstall restores it)")
        else:
            _say("Proxy rule registered with the system (Chrome, Edge and Firefox read it on their own)")

    install_launcher(cfg.http_port, dry, created)

    if pf.cisco_installed():
        print("   Note: Cisco Secure Client is installed. Do not connect both at once; uni-vpn pauses while Cisco is connected.")
        print("   Recommendation: in the Cisco client, turn off automatic connect on start ('Beim Start automatisch verbinden').")

    if broken is not None:
        # Nothing to ask for without a valid university ID; the service is in place again.
        print(f"\nFix {cfg_path} (or delete it and run setup again), then: uni-vpn service restart")
        return 0

    url = f"http://127.0.0.1:{cfg.http_port}/"
    if gui:
        # The service has started, but the daemon needs a moment until bind().
        if not wait_for_port(cfg.http_port, port_open=port_open, timeout=15):
            print(f"\nThe service did not open {url} within 15 seconds. See: uni-vpn log, uni-vpn doctor")
            if run_doctor:
                print()
                print(doctor.format_checks(doctor.run_checks(cfg_path)))
            return 1
        if getattr(args, "no_browser", False):
            # install.ps1 runs this elevated and opens the browser itself, unelevated (not on update).
            print(f"\nStatus page: {url}" if configured else f"\nFinish in the browser ({url}).")
            return 0
        if open_url(url):
            print(f"\nFinish in the browser window that just opened ({url}).")
            print("Restart any other open browser once so that it reads the proxy rule.")
            return 0
        gui = False
        if not cfg_path.exists():
            print(f"\nOpen {url} in a browser to finish the setup.")
            return 0

    if dry:
        _say("would ask for the university password and the TOTP secret and store both in the keyring")
    else:
        state = keyring_probe(cfg.user)
        if state == "present":
            _say("Password is already in the keyring")
        else:
            password = getpass_fn(f"University password for {cfg.user} (stored only in the keyring): ")
            if password:
                store(cfg.user, password)
                _say("Password stored in the keyring")
            else:
                print("   No password entered, set it later with: uni-vpn password")
        state = keyring_probe(cfg.user, kind="totp")
        if state == "present":
            _say("TOTP secret is already in the keyring")
        else:
            print(TOTP_HINT)
            text = getpass_fn(f"TOTP secret for {cfg.user} (otpauth URL or Base32, input stays hidden): ")
            if not text.strip():
                print("   No secret entered, set it later with: uni-vpn totp")
            else:
                try:
                    token = totp.normalize(text)
                except ValueError as exc:
                    print(f"   {exc}. Try again later with: uni-vpn totp")
                else:
                    store_totp(cfg.user, token)
                    _say(f"TOTP secret stored in the keyring. Check code now: {totp.code(token)} (must match the app)")

    if run_doctor and not dry:
        # The service has started, but the daemon needs a moment until bind().
        wait_for_port(cfg.http_port, port_open=port_open, timeout=15)
        print()
        print(doctor.format_checks(doctor.run_checks(cfg_path)))
    print(FINAL_HINT.format(port=cfg.http_port))
    return 0


def uninstall(args, *, input_fn=input, service_uninstall=service.uninstall, delete=credentials.delete_password,
              delete_totp=credentials.delete_totp, proxy_uninstall=sysproxy.uninstall) -> int:
    dry = bool(getattr(args, "dry_run", False))
    user = None
    try:
        user = config.load(config.default_path()).user
    except config.ConfigError:
        pass
    if dry:
        _say("would remove the proxy rule from the system, remove the service and delete these files:")
        for path in recorded():
            print(f"   {path}")
        return 0
    failed = False
    proxy = proxy_uninstall()
    if proxy == "failed":
        failed = True
        print(f"Proxy setting could not be restored, the backup stays in {sysproxy.backup_path()}")
    else:
        _say("Proxy setting restored" if proxy == "restored" else "Proxy setting was not set by uni-vpn")
    if service_uninstall() is False:
        failed = True
        print("Service could not be removed")
    else:
        _say("Service removed")
    for path in recorded():
        try:
            if path.is_dir() and not path.is_symlink():
                shutil.rmtree(path, ignore_errors=True)  # macOS app entry
                _say(f"deleted: {path}")
            elif path.is_symlink() or path.exists():
                path.unlink()
                _say(f"deleted: {path}")
        except OSError as exc:
            print(f"   Could not delete {path}: {exc.strerror or exc}")
    if pf.IS_WINDOWS:
        from . import windows

        windows.remove_user_path(str(command_path().parent))
    if _records_path().exists():
        _records_path().unlink()
    for name in ("daemon.lock",):
        try:
            (pf.config_dir() / name).unlink(missing_ok=True)
        except OSError:
            pass  # Windows: still locked by a daemon that did not stop
    if user:
        if getattr(args, "yes", False):
            answer = "y"
        else:
            try:
                answer = input_fn(f"Delete the password and TOTP secret for {user} from the keyring? [y/N] ").strip().lower()
            except EOFError:  # no terminal
                print()
                answer = "n"
        if answer in ("j", "ja", "y", "yes"):
            _say("Password deleted" if delete(user) else "Password was not in the keyring")
            _say("TOTP secret deleted" if delete_totp(user) else "TOTP secret was not in the keyring")
    app = pf.repo_root()
    # The copy get.sh/get.ps1 made goes too; a git clone is the user's own. After a failure it stays,
    # so that install.sh --uninstall can run again.
    if not failed and app == pf.app_install_dir().resolve() and not (app / ".git").exists():
        shutil.rmtree(app, ignore_errors=True)
        if app.exists():
            # For example the folder a shell is in, or a file still open on Windows.
            print(f"   Could not delete everything in {app}, remove it by hand")
        else:
            _say(f"deleted: {app}")
            try:
                app.parent.rmdir()
            except OSError:
                pass
        print(f"Left in place: packages (openconnect, ocproxy) and the log in {pf.state_dir()}")
    else:
        print(f"Left in place: packages (openconnect, ocproxy), the repo {app} and the log in {pf.state_dir()}")
    return 1 if failed else 0


ARCHIVE_URL = "https://codeload.github.com/DavidVinu/uni-vpn/zip/refs/heads/main"


def _archive_files(archive, root: Path) -> dict[str, tuple[str, Path]]:
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


def _remove_stale(root: Path, keep: set[str]) -> None:
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


def download_release(target: Path, url: str = ARCHIVE_URL, opener=None) -> None:
    """Replace the program files in target with the current main branch (installs without git).
    ValueError for a broken or unexpected download, OSError when files cannot be written."""
    import io
    import tempfile
    import urllib.request
    import zipfile
    import zlib

    opener = opener or urllib.request.urlopen
    with opener(url, timeout=60) as response:
        data = response.read()
    root = target.resolve()
    try:
        with zipfile.ZipFile(io.BytesIO(data)) as archive:
            files = _archive_files(archive, root)
            if archive.testzip() is not None:
                raise ValueError("the download is damaged, try again")
            # Unpack everything first, next to target (same file system), so that a broken download
            # changes nothing. Then overwrite file by file: on Windows the running service has this
            # folder as its working directory, so the folder itself cannot be swapped.
            staging = Path(tempfile.mkdtemp(prefix=".uni-vpn-update-", dir=root.parent))
            try:
                for relative, (name, _destination) in files.items():
                    staged = staging / relative
                    staged.parent.mkdir(parents=True, exist_ok=True)
                    staged.write_bytes(archive.read(name))
                    if relative.startswith("bin/") or relative.endswith(".sh"):
                        staged.chmod(0o755)
                for relative, (_name, destination) in files.items():
                    destination.parent.mkdir(parents=True, exist_ok=True)
                    os.replace(staging / relative, destination)
            finally:
                shutil.rmtree(staging, ignore_errors=True)
    except (zipfile.BadZipFile, zlib.error, EOFError) as exc:
        raise ValueError(f"the download is damaged, try again ({exc})") from None
    _remove_stale(root, set(files))


GET_PS1_URL = "https://raw.githubusercontent.com/DavidVinu/uni-vpn/main/get.ps1"


def update(args, run=subprocess.run, download=download_release) -> int:
    dry = bool(getattr(args, "dry_run", False))
    if pf.IS_WINDOWS:
        # The program lives in Program Files: get.ps1 downloads it and install.ps1 copies it there
        # with one administrator prompt, then restarts the service.
        if dry:
            _say(f"would run {GET_PS1_URL} to update {pf.app_install_dir()}")
            return 0
        command = f"& {{ irm {GET_PS1_URL} | iex }}; exit $LASTEXITCODE"
        return run(["powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", command],
                   env=dict(os.environ, UNI_VPN_UPDATE="1")).returncode
    repo = pf.repo_root()
    git = (repo / ".git").exists()
    if dry:
        how = "git pull" if git else "download the current version"
        _say(f"would {how} in {repo} and restart the service")
        return 0
    if git:
        result = run(["git", "-C", str(repo), "pull", "--ff-only"], text=True)
        if result.returncode != 0:
            print("git pull failed")
            return result.returncode
    else:
        try:
            download(repo)
        except (OSError, ValueError) as exc:
            print(f"Update failed: {exc}")
            return 1
        _say("Downloaded the current version")
    rc = service.control("restart", run=run)
    print("Service restarted." if rc == 0 else "Restarting the service failed, see: uni-vpn log")
    return rc
