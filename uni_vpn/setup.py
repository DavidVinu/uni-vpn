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


def command_path() -> Path:
    if pf.IS_WINDOWS:
        return pf.config_dir() / "bin" / "uni-vpn.cmd"
    return Path.home() / ".local" / "bin" / "uni-vpn"


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
    link.write_text(wrapper, encoding="utf-8", newline="")
    link.chmod(0o755)
    created(link)
    _say(f"Command created: {link}")
    if pf.IS_WINDOWS:
        from . import windows

        if windows.add_user_path(str(link.parent)):
            print("   Open a new terminal to use the uni-vpn command")
    elif str(link.parent) not in os.environ.get("PATH", "").split(os.pathsep):
        print(f"   Note: {link.parent} is not in PATH, open a new shell or add it to PATH")


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
    if cfg_path.exists():
        cfg = config.load(cfg_path)
        _say(f"Config found: {cfg_path} (university ID {cfg.user})")
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
        elif result == "replaced":
            _say("Proxy rule registered with the system; an existing proxy setting was replaced (backed up, uninstall restores it)")
        else:
            _say("Proxy rule registered with the system (Chrome, Edge and Firefox read it on their own)")

    install_launcher(cfg.http_port, dry, created)

    if pf.cisco_installed():
        print("   Note: Cisco Secure Client is installed. Do not connect both at once; uni-vpn pauses while Cisco is connected.")
        print("   Recommendation: in the Cisco client, turn off automatic connect on start ('Beim Start automatisch verbinden').")

    url = f"http://127.0.0.1:{cfg.http_port}/"
    if gui:
        # The service has started, but the daemon needs a moment until bind().
        wait_for_port(cfg.http_port, port_open=port_open, timeout=15)
        if getattr(args, "no_browser", False):
            # install.ps1 runs this elevated and opens the browser itself, unelevated.
            print(f"\nFinish in the browser ({url}).")
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
    _say("Proxy setting restored" if proxy_uninstall() else "Proxy setting was not set by uni-vpn")
    service_uninstall()
    _say("Service removed")
    for path in recorded():
        if path.is_dir() and not path.is_symlink():
            shutil.rmtree(path, ignore_errors=True)  # macOS app entry
            _say(f"deleted: {path}")
        elif path.is_symlink() or path.exists():
            path.unlink()
            _say(f"deleted: {path}")
    if pf.IS_WINDOWS:
        from . import windows

        windows.remove_user_path(str(command_path().parent))
    if _records_path().exists():
        _records_path().unlink()
    for name in ("daemon.lock",):
        stale = pf.config_dir() / name
        if stale.exists():
            stale.unlink()
    if user:
        answer = "y" if getattr(args, "yes", False) else input_fn(f"Delete the password and TOTP secret for {user} from the keyring? [y/N] ").strip().lower()
        if answer in ("j", "ja", "y", "yes"):
            _say("Password deleted" if delete(user) else "Password was not in the keyring")
            _say("TOTP secret deleted" if delete_totp(user) else "TOTP secret was not in the keyring")
    print(f"Left in place: packages (openconnect, ocproxy), the repo {pf.repo_root()} and the log in {pf.state_dir()}")
    return 0


ARCHIVE_URL = "https://codeload.github.com/DavidVinu/uni-vpn/zip/refs/heads/main"


def download_release(target: Path, url: str = ARCHIVE_URL, opener=None) -> None:
    """Replace the program files in target with the current main branch (installs without git)."""
    import io
    import urllib.request
    import zipfile

    opener = opener or urllib.request.urlopen
    with opener(url, timeout=60) as response:
        data = response.read()
    with zipfile.ZipFile(io.BytesIO(data)) as archive:
        names = archive.namelist()
        prefix = names[0].split("/", 1)[0] + "/"
        if not any(n == prefix + "uni_vpn/__init__.py" for n in names):
            raise ValueError("the download does not look like uni-vpn")
        for name in names:
            relative = name[len(prefix):]
            if not relative or name.endswith("/"):
                continue
            destination = (target / relative).resolve()
            if target.resolve() not in destination.parents:
                raise ValueError(f"unexpected path in the download: {name}")
            destination.parent.mkdir(parents=True, exist_ok=True)
            # Overwrite in place: on Windows the running service has this folder as its working
            # directory, so the folder itself cannot be swapped.
            destination.write_bytes(archive.read(name))
            if relative.startswith("bin/") or relative.endswith(".sh"):
                destination.chmod(0o755)


def update(args, run=subprocess.run, download=download_release) -> int:
    dry = bool(getattr(args, "dry_run", False))
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
    print("Service restarted.")
    return rc
