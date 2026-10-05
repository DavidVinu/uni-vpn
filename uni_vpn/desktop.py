"""The Uni VPN app: a native window around the page on 127.0.0.1:<http_port>, plus a menu bar
(macOS), notification area (Windows) or panel icon (Linux) that starts at login.

Each platform gets a small shell around its own web view, built or set up by "uni-vpn setup":
macOS Swift with WKWebView (app/macos), Windows C# with WebView2 (app/windows), Linux
PyGObject with WebKitGTK (app/linux). Where that is not possible (no Swift compiler, no
WebView2 SDK download, no WebKitGTK) the app entry opens the page in the browser as before.
"""

from __future__ import annotations

import hashlib
import io
import os
import plistlib
import shutil
import subprocess
import sys
import tempfile
import urllib.parse
import urllib.request
import zipfile
from pathlib import Path

from . import platform as pf

APP_NAME = "Uni VPN"
APP_ID = "de.davidvinu.UniVPN"  # Linux: application id, desktop file and icon name
BUNDLE_ID = "de.davidvinu.uni-vpn.app"  # macOS
LOGIN_LABEL = "de.davidvinu.uni-vpn.app"  # macOS LaunchAgent that starts the menu bar item
URL_SCHEME = "uni-vpn"
WEBVIEW2_VERSION = "1.0.4258.31"
WEBVIEW2_URL = (f"https://api.nuget.org/v3-flatcontainer/microsoft.web.webview2/{WEBVIEW2_VERSION}/"
                f"microsoft.web.webview2.{WEBVIEW2_VERSION}.nupkg")
WEBVIEW2_SHA256 = "56f7f4b8bf9aee4b8efefbbdd4f67d5f74ebd1b100ed0806da71bf76af481aa9"
WEBVIEW2_FILES = {"lib/net462/Microsoft.Web.WebView2.Core.dll": "Microsoft.Web.WebView2.Core.dll",
                  "lib/net462/Microsoft.Web.WebView2.WinForms.dll": "Microsoft.Web.WebView2.WinForms.dll",
                  "runtimes/win-x64/native/WebView2Loader.dll": "WebView2Loader.dll"}
GTK_CHECK = ("import gi; gi.require_version('Gtk', '3.0')\n"
             "for v in ('4.1', '4.0'):\n"
             "    try:\n"
             "        gi.require_version('WebKit2', v); break\n"
             "    except ValueError:\n"
             "        pass\n"
             "from gi.repository import Gtk, WebKit2")
INDICATOR_CHECK = ("import gi\n"
                   "for name in ('AyatanaAppIndicator3', 'AppIndicator3'):\n"
                   "    try:\n"
                   "        gi.require_version(name, '0.1'); __import__('gi.repository.' + name); break\n"
                   "    except (ValueError, ImportError):\n"
                   "        pass\n"
                   "else:\n"
                   "    raise SystemExit(1)")


def _say(text: str) -> None:
    print(f"-> {text}")


def _quiet(run, command: list[str], **kwargs) -> bool:
    """Runs a helper whose failure only costs a detail (icon, cache, an old process). True if it ran fine."""
    try:
        return run(command, capture_output=True, text=True, **kwargs).returncode == 0
    except (OSError, subprocess.SubprocessError):
        return False


def source_dir() -> Path:
    return pf.repo_root() / "app"


# --- where things go ---------------------------------------------------------

def app_path() -> Path:
    """What the start menu, Launchpad or app grid shows."""
    if pf.IS_WINDOWS:
        from .windows import start_menu_dir

        return Path(start_menu_dir()) / f"{APP_NAME}.lnk"
    if pf.IS_MACOS:
        return Path.home() / "Applications" / f"{APP_NAME}.app"
    return _data_home() / "applications" / f"{APP_ID}.desktop"


def legacy_paths() -> list[Path]:
    """App entries of earlier versions, which opened the browser."""
    if pf.IS_WINDOWS:
        from .windows import start_menu_dir

        return [Path(start_menu_dir()) / f"{APP_NAME}.url"]
    if pf.IS_MACOS:
        return []
    return [_data_home() / "applications" / "uni-vpn.desktop"]


def login_path() -> Path:
    """Starts the menu bar or tray icon at login, without a window."""
    if pf.IS_WINDOWS:
        from .windows import start_menu_dir

        return Path(start_menu_dir()) / "Startup" / f"{APP_NAME}.lnk"
    if pf.IS_MACOS:
        return Path.home() / "Library" / "LaunchAgents" / f"{LOGIN_LABEL}.plist"
    base = os.environ.get("XDG_CONFIG_HOME") or str(Path.home() / ".config")
    return Path(base) / "autostart" / f"{APP_ID}.desktop"


def windows_build_dir() -> Path:
    # Next to the program in Program Files: what runs as the user must not be changeable by
    # other programs of the user either.
    return pf.repo_root() / "desktop"


def windows_exe() -> Path:
    return windows_build_dir() / f"{APP_NAME}.exe"


def linux_icon_path() -> Path:
    return _data_home() / "icons" / "hicolor" / "scalable" / "apps" / f"{APP_ID}.svg"


def _data_home() -> Path:
    return Path(os.environ.get("XDG_DATA_HOME") or str(Path.home() / ".local" / "share"))


# --- install -----------------------------------------------------------------

def install(port: int, dry: bool, created, run=subprocess.run) -> bool:
    """Builds or sets up the app and its login item. True if the native app is in place,
    False if the app entry only opens the browser."""
    port = int(port)
    if pf.IS_MACOS:
        return _install_macos(port, dry, created, run)
    if pf.IS_WINDOWS:
        return _install_windows(port, dry, created, run)
    return _install_linux(port, dry, created, run)


def _browser_entry(port: int, dry: bool, created, run) -> bool:
    """The fallback: an app entry that opens the page in the default browser."""
    url = f"http://127.0.0.1:{port}/"
    path = legacy_paths()[0] if pf.IS_WINDOWS or not pf.IS_MACOS else app_path()
    if dry:
        _say(f"would add {path} to open {url}")
        return False
    path.parent.mkdir(parents=True, exist_ok=True)
    if pf.IS_WINDOWS:
        path.write_text(f"[InternetShortcut]\r\nURL={url}\r\n", encoding="utf-8", newline="")
    elif pf.IS_MACOS:
        if path.exists():
            shutil.rmtree(path, ignore_errors=True)
        result = run(["osacompile", "-o", str(path), "-e", f'open location "{url}"'], capture_output=True, text=True)
        if result.returncode != 0:
            print(f"   App entry could not be created: {(result.stderr or '').strip()}")
            return False
    else:
        path.write_text("[Desktop Entry]\nType=Application\nName=Uni VPN\nComment=University VPN for selected websites\n"
                        f"Exec=xdg-open {url}\nIcon=network-vpn\nCategories=Network;\nTerminal=false\n", encoding="utf-8")
    created(path)
    _say(f"App entry created: {path}")
    return False


# macOS ----------------------------------------------------------------------

def info_plist(port: int) -> dict:
    return {
        "CFBundleName": APP_NAME, "CFBundleDisplayName": APP_NAME, "CFBundleIdentifier": BUNDLE_ID,
        "CFBundleExecutable": APP_NAME, "CFBundleIconFile": "AppIcon", "CFBundlePackageType": "APPL",
        "CFBundleShortVersionString": _version(), "CFBundleVersion": _version(),
        "LSMinimumSystemVersion": "11.0", "NSHighResolutionCapable": True, "LSApplicationCategoryType":
        "public.app-category.utilities", "UniVPNPort": str(port),
        "CFBundleURLTypes": [{"CFBundleURLName": BUNDLE_ID, "CFBundleURLSchemes": [URL_SCHEME]}],
        # The page is plain HTTP on the loopback address.
        "NSAppTransportSecurity": {"NSAllowsLocalNetworking": True, "NSExceptionDomains": {
            "127.0.0.1": {"NSExceptionAllowsInsecureHTTPLoads": True}}},
    }


def login_plist(executable: Path) -> dict:
    return {"Label": LOGIN_LABEL, "ProgramArguments": [str(executable), "--hidden"], "RunAtLoad": True,
            "LimitLoadToSessionType": "Aqua", "ProcessType": "Interactive"}


def _version() -> str:
    from . import __version__

    return __version__


def _swiftc(run) -> str | None:
    found = shutil.which("swiftc")
    if found:
        return found
    try:
        result = run(["xcrun", "--find", "swiftc"], capture_output=True, text=True, timeout=30)
    except (OSError, subprocess.SubprocessError):
        return None
    path = (result.stdout or "").strip()
    return path if result.returncode == 0 and path else None


def _icns(png: Path, target: Path, run) -> bool:
    """AppIcon.icns from the 1024 px PNG with the tools every Mac has."""
    with tempfile.TemporaryDirectory() as tmp:
        iconset = Path(tmp) / "AppIcon.iconset"
        iconset.mkdir()
        for size in (16, 32, 128, 256, 512):
            for scale in (1, 2):
                name = f"icon_{size}x{size}{'@2x' if scale == 2 else ''}.png"
                pixels = str(size * scale)
                if not _quiet(run, ["sips", "-z", pixels, pixels, str(png), "--out", str(iconset / name)]):
                    return False
        return _quiet(run, ["iconutil", "-c", "icns", str(iconset), "-o", str(target)])


def _install_macos(port: int, dry: bool, created, run) -> bool:
    app = app_path()
    swiftc = _swiftc(run) if not dry else shutil.which("swiftc") or "swiftc"
    if not swiftc:
        print("   Swift is missing (Xcode Command Line Tools), the app opens in the browser for now")
        return _browser_entry(port, dry, created, run)
    login = login_path()
    if dry:
        _say(f"would build {app} with {swiftc} and start it at login ({login})")
        return True
    with tempfile.TemporaryDirectory() as tmp:
        bundle = Path(tmp) / f"{APP_NAME}.app"
        (bundle / "Contents" / "MacOS").mkdir(parents=True)
        (bundle / "Contents" / "Resources").mkdir()
        binary = bundle / "Contents" / "MacOS" / APP_NAME
        try:
            result = run([swiftc, "-O", "-o", str(binary), str(source_dir() / "macos" / "main.swift")],
                         capture_output=True, text=True, timeout=600)
        except (OSError, subprocess.SubprocessError) as exc:
            result = subprocess.CompletedProcess([], 1, stdout="", stderr=str(exc))
        if result.returncode != 0:
            print(f"   Building the app failed, it opens in the browser for now: {(result.stderr or '').strip()[-400:]}")
            return _browser_entry(port, dry, created, run)
        with open(bundle / "Contents" / "Info.plist", "wb") as handle:
            plistlib.dump(info_plist(port), handle)
        if not _icns(source_dir() / "icon.png", bundle / "Contents" / "Resources" / "AppIcon.icns", run):
            print("   App icon could not be created")
        _quiet(run, ["codesign", "--force", "--sign", "-", str(bundle)])
        # The running copy goes first: the new one replaces it.
        _stop_running(run)
        if app.exists() or app.is_symlink():
            shutil.rmtree(app, ignore_errors=True)
        app.parent.mkdir(parents=True, exist_ok=True)
        shutil.move(str(bundle), str(app))
    created(app)
    lsregister = ("/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/"
                  "Support/lsregister")
    _quiet(run, [lsregister, "-f", str(app)])
    _say(f"App built: {app}")
    login.parent.mkdir(parents=True, exist_ok=True)
    with open(login, "wb") as handle:
        plistlib.dump(login_plist(app / "Contents" / "MacOS" / APP_NAME), handle)
    created(login)
    domain = f"gui/{os.getuid()}"
    _quiet(run, ["launchctl", "bootout", domain, str(login)])
    _quiet(run, ["launchctl", "bootstrap", domain, str(login)])
    _say("Menu bar item starts at login")
    return True


def _stop_running(run) -> None:
    if pf.IS_MACOS:
        _quiet(run, ["launchctl", "bootout", f"gui/{os.getuid()}", str(login_path())])
        _quiet(run, ["pkill", "-x", APP_NAME])
    elif pf.IS_WINDOWS:
        _quiet(run, ["taskkill", "/F", "/IM", f"{APP_NAME}.exe"], creationflags=0x08000000)
    else:
        _quiet(run, ["pkill", "-f", str(source_dir() / "linux" / "uni-vpn-app.py")])


# Windows --------------------------------------------------------------------

def csc_path() -> Path | None:
    windir = os.environ.get("WINDIR") or "C:\\Windows"
    for framework in ("Framework64", "Framework"):
        candidate = Path(windir) / "Microsoft.NET" / framework / "v4.0.30319" / "csc.exe"
        if candidate.is_file():
            return candidate
    return None


def extract_webview2(data: bytes, target: Path) -> None:
    """The three files of the WebView2 SDK the app needs, after checking the package's hash."""
    digest = hashlib.sha256(data).hexdigest()
    if digest != WEBVIEW2_SHA256:
        raise ValueError(f"WebView2 SDK has SHA-256 {digest}, expected {WEBVIEW2_SHA256}")
    target.mkdir(parents=True, exist_ok=True)
    with zipfile.ZipFile(io.BytesIO(data)) as archive:
        for member, name in WEBVIEW2_FILES.items():
            (target / name).write_bytes(archive.read(member))


def _download(url: str, timeout: float = 60) -> bytes:
    with urllib.request.urlopen(url, timeout=timeout) as response:  # noqa: S310 - fixed https URL
        return response.read()


def ps_quote(text: str) -> str:
    return "'" + str(text).replace("'", "''") + "'"


def shortcut_script(path: Path, target: Path, arguments: str, icon: Path) -> str:
    return (f"$s = (New-Object -ComObject WScript.Shell).CreateShortcut({ps_quote(path)}); "
            f"$s.TargetPath = {ps_quote(target)}; $s.Arguments = {ps_quote(arguments)}; "
            f"$s.WorkingDirectory = {ps_quote(target.parent)}; $s.IconLocation = {ps_quote(str(icon) + ',0')}; "
            f"$s.Description = {ps_quote(APP_NAME)}; $s.Save()")


def _install_windows(port: int, dry: bool, created, run, download=_download) -> bool:
    csc = csc_path()
    exe = windows_exe()
    if dry:
        _say(f"would build {exe} with {csc or 'csc.exe'} and the WebView2 SDK {WEBVIEW2_VERSION}, "
             f"add {app_path()} and start it at login ({login_path()})")
        return True
    if csc is None:
        print("   The .NET Framework compiler is missing, the app opens in the browser for now")
        return _browser_entry(port, dry, created, run)
    build = windows_build_dir()
    try:
        sdk = Path(tempfile.mkdtemp(prefix="uni-vpn-sdk-"))
        extract_webview2(download(WEBVIEW2_URL), sdk)
    except (OSError, ValueError, zipfile.BadZipFile) as exc:
        print(f"   WebView2 SDK not available ({exc}), the app opens in the browser for now")
        return _browser_entry(port, dry, created, run)
    _stop_running(run)
    shutil.rmtree(build, ignore_errors=True)
    build.mkdir(parents=True, exist_ok=True)
    for name in WEBVIEW2_FILES.values():
        shutil.copy2(sdk / name, build / name)
    shutil.rmtree(sdk, ignore_errors=True)
    source = source_dir() / "windows"
    command = [str(csc), "/nologo", "/target:winexe", "/optimize+", f"/win32icon:{source / 'uni-vpn.ico'}",
               f"/out:{exe}", f"/lib:{build}", "/r:Microsoft.Web.WebView2.Core.dll",
               "/r:Microsoft.Web.WebView2.WinForms.dll", "/r:System.Windows.Forms.dll", "/r:System.Drawing.dll",
               "/r:System.Web.Extensions.dll", str(source / "UniVpn.cs")]
    try:
        result = run(command, capture_output=True, text=True, timeout=600, creationflags=0x08000000)
    except (OSError, subprocess.SubprocessError) as exc:
        result = subprocess.CompletedProcess(command, 1, stdout=str(exc), stderr="")
    if result.returncode != 0:
        print(f"   Building the app failed, it opens in the browser for now: {(result.stdout or '').strip()[-400:]}")
        return _browser_entry(port, dry, created, run)
    _say(f"App built: {exe}")
    for legacy in legacy_paths():
        legacy.unlink(missing_ok=True)
    for path, arguments in ((app_path(), f"--port {port}"), (login_path(), f"--port {port} --hidden")):
        path.parent.mkdir(parents=True, exist_ok=True)
        script = shortcut_script(path, exe, arguments, exe)
        if not _quiet(run, ["powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script],
                      creationflags=0x08000000):
            print(f"   Shortcut {path} could not be created")
            continue
        created(path)
    _say("App entry in the start menu, icon in the notification area starts at login")
    if login_path().exists():
        # Back in the notification area right away, as the signed-in user (setup runs elevated).
        _spawn(["explorer.exe", str(login_path())])
    return True


# Linux ----------------------------------------------------------------------

def gtk_python(run=subprocess.run) -> str | None:
    """A Python with PyGObject, GTK 3 and WebKitGTK: the distribution's own, which is not
    necessarily the one uni-vpn runs with (Ubuntu 22.04 with python3.11, for example)."""
    candidates = ["/usr/bin/python3"] + [f"/usr/bin/python3.{minor}" for minor in range(20, 7, -1)]
    for candidate in dict.fromkeys([sys.executable, *candidates]):
        if not os.access(candidate, os.X_OK):
            continue
        try:
            if run([candidate, "-c", GTK_CHECK], capture_output=True, timeout=20).returncode == 0:
                return candidate
        except (OSError, subprocess.SubprocessError):
            continue
    return None


def has_indicator(python: str, run=subprocess.run) -> bool:
    try:
        return run([python, "-c", INDICATOR_CHECK], capture_output=True, timeout=20).returncode == 0
    except (OSError, subprocess.SubprocessError):
        return False


def desktop_quote(text: str) -> str:
    """An argument for Exec= in a desktop entry."""
    escaped = str(text).replace("\\", "\\\\").replace('"', '\\"').replace("`", "\\`").replace("$", "\\$")
    return '"' + escaped.replace("%", "%%") + '"'


def desktop_entry(python: str, port: int, hidden: bool = False) -> str:
    script = source_dir() / "linux" / "uni-vpn-app.py"
    command = f"{desktop_quote(python)} {desktop_quote(script)} --port {int(port)}" + (" --hidden" if hidden else "")
    lines = ["[Desktop Entry]", "Type=Application", f"Name={APP_NAME}", "Comment=University VPN for selected websites",
             f"Exec={command}", f"Icon={APP_ID}", f"StartupWMClass={APP_ID}", "Categories=Network;",
             "Terminal=false", "StartupNotify=true"]
    if hidden:
        lines += ["NoDisplay=true", "X-GNOME-Autostart-enabled=true"]
    return "\n".join(lines) + "\n"


def _install_linux(port: int, dry: bool, created, run) -> bool:
    python = gtk_python(run)
    if python is None:
        if not dry:
            print("   WebKitGTK is missing, the app opens in the browser for now")
        return _browser_entry(port, dry, created, run)
    if dry:
        _say(f"would add {app_path()} to open the app with {python}")
        return True
    icon = linux_icon_path()
    icon.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(source_dir() / "icon.svg", icon)
    created(icon)
    for legacy in legacy_paths():
        legacy.unlink(missing_ok=True)
    path = app_path()
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(desktop_entry(python, port), encoding="utf-8")
    created(path)
    _say(f"App entry created: {path}")
    _quiet(run, ["update-desktop-database", str(path.parent)])
    _stop_running(run)
    if has_indicator(python, run):
        login = login_path()
        login.parent.mkdir(parents=True, exist_ok=True)
        login.write_text(desktop_entry(python, port, hidden=True), encoding="utf-8")
        created(login)
        _spawn([python, str(source_dir() / "linux" / "uni-vpn-app.py"), "--port", str(port), "--hidden"])
        _say("Panel icon starts at login")
    return True


def _spawn(command: list[str]) -> bool:
    try:
        subprocess.Popen(command, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                         start_new_session=True)
        return True
    except OSError:
        return False


# --- open --------------------------------------------------------------------

def app_command(port: int, page: str = "", run=subprocess.run) -> list[str] | None:
    """How to show the app window with a page: "", "#settings", or "&user=ab123"."""
    if pf.IS_MACOS:
        if not (app_path() / "Contents" / "MacOS" / APP_NAME).exists():
            return None
        if page == "#settings":
            url = f"{URL_SCHEME}://settings"
        else:
            url = f"{URL_SCHEME}://open" + ("?" + page.lstrip("&") if page.startswith("&") else "")
        return ["open", url]
    if pf.IS_WINDOWS:
        exe = windows_exe()
        if not exe.exists():
            return None
        return [str(exe), "--port", str(int(port))] + (["--page", page] if page else [])
    if not app_path().exists():
        return None
    python = gtk_python(run)
    if python is None:
        return None
    command = [python, str(source_dir() / "linux" / "uni-vpn-app.py"), "--port", str(int(port))]
    return command + (["--page", page] if page else [])


def open_app(port: int, page: str = "", run=subprocess.run) -> bool:
    """Shows the app window. False when there is no app or no desktop; then the browser is the way."""
    if not pf.has_desktop():
        return False
    command = app_command(port, page, run)
    if command is None:
        return False
    if pf.IS_WINDOWS:
        from .windows import is_admin

        if is_admin():
            # The app must not run elevated: Explorer starts the shortcut as the signed-in user.
            # It cannot pass the page along, the assistant prefills less.
            if not app_path().exists():
                return False
            return _spawn(["explorer.exe", str(app_path())])
    return _spawn(command)


def page_for(query: dict) -> str:
    """The setup assistant's prefill (user, university) as a page for open_app."""
    text = urllib.parse.urlencode({key: value for key, value in query.items() if value})
    return "&" + text if text else ""


def uninstall(run=subprocess.run) -> None:
    """Stops the running app; the files themselves are in the installed-files list."""
    if pf.IS_MACOS:
        _stop_running(run)
    elif pf.IS_WINDOWS:
        _stop_running(run)
        shutil.rmtree(windows_build_dir(), ignore_errors=True)
    else:
        _stop_running(run)
