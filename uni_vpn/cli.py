"""Command line: uni-vpn <command>."""

from __future__ import annotations

import argparse
import asyncio
import ctypes
import getpass
import json
import os
import signal
import sys
import urllib.error
import urllib.request
from pathlib import Path

from . import __version__, config, credentials, totp
from . import platform as pf


class DaemonUnreachable(Exception):
    pass


def api_get(cfg: config.Config, path: str, timeout: float = 3) -> dict:
    try:
        with urllib.request.urlopen(f"http://127.0.0.1:{cfg.http_port}{path}", timeout=timeout) as response:
            return json.loads(response.read().decode("utf-8"))
    except (urllib.error.URLError, OSError, ValueError) as exc:
        raise DaemonUnreachable(str(exc)) from None


def api_post(cfg: config.Config, path: str, body: dict | None = None, timeout: float = 5) -> dict:
    data = json.dumps(body or {}).encode("utf-8")
    request = urllib.request.Request(
        f"http://127.0.0.1:{cfg.http_port}{path}", data=data, method="POST",
        headers={"X-Uni-VPN": "1", "Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            return json.loads(response.read().decode("utf-8"))
    except urllib.error.HTTPError as exc:
        raise DaemonUnreachable(f"HTTP {exc.code}: {exc.read().decode(errors='replace')}") from None
    except (urllib.error.URLError, OSError, ValueError) as exc:
        raise DaemonUnreachable(str(exc)) from None


def format_status(status: dict) -> str:
    return (f"{status['state']}: {status['message']} ({status['user']}@{status['host']}, "
            f"SOCKS 127.0.0.1:{status['socks_port']}, {status['active_connections']} connections)")


def harden() -> None:
    """No core dumps with the password in memory."""
    if sys.platform == "win32":
        return  # no core files; Windows Error Reporting is per process and off for pythonw children
    import resource

    try:
        resource.setrlimit(resource.RLIMIT_CORE, (0, 0))
    except (ValueError, OSError):
        pass
    if sys.platform.startswith("linux"):
        try:
            libc = ctypes.CDLL("libc.so.6", use_errno=True)
            libc.prctl(4, 0, 0, 0, 0)  # PR_SET_DUMPABLE = 4
        except (OSError, AttributeError):
            pass


UNREACHABLE_HINT = "Daemon not reachable. Start it with: uni-vpn service start, then uni-vpn doctor"


def _load(args) -> config.Config:
    return config.load(Path(args.config) if args.config else None)


def cmd_status(args) -> int:
    cfg = _load(args)
    try:
        status = api_get(cfg, "/status.json")
    except DaemonUnreachable:
        print(UNREACHABLE_HINT)
        return 1
    print(json.dumps(status, indent=2, ensure_ascii=False) if args.json else format_status(status))
    return 0


def cmd_connect(args) -> int:
    cfg = _load(args)
    try:
        api_post(cfg, "/api/connect")
    except DaemonUnreachable:
        print(UNREACHABLE_HINT)
        return 1
    print("Connecting, check with: uni-vpn status")
    return 0


def cmd_disconnect(args) -> int:
    cfg = _load(args)
    try:
        api_post(cfg, "/api/disconnect")
    except DaemonUnreachable:
        print(UNREACHABLE_HINT)
        return 1
    print("Disconnected")
    return 0


def cmd_password(args) -> int:
    cfg = _load(args)
    password = getpass.getpass(f"University password for {cfg.user}: ")
    if not password:
        print("No password entered")
        return 2
    if "\n" in password or "\r" in password:
        print("Password must not contain a line break")
        return 2
    credentials.store_password(cfg.user, password)
    print("Password stored in the keyring")
    try:
        api_post(cfg, "/api/connect")
    except DaemonUnreachable:
        pass
    return 0


def cmd_totp(args) -> int:
    cfg = _load(args)
    text = getpass.getpass(f"TOTP secret for {cfg.user} (otpauth URL or Base32, input stays hidden): ")
    if not text.strip():
        print("No secret entered")
        return 2
    try:
        token = totp.normalize(text)
    except ValueError as exc:
        print(str(exc))
        return 2
    credentials.store_totp(cfg.user, token)
    print(f"TOTP secret stored in the keyring. Check code now: {totp.code(token)} (must match the app)")
    try:
        api_post(cfg, "/api/connect")
    except DaemonUnreachable:
        pass
    return 0


def cmd_log(args) -> int:
    path = pf.log_file()
    if not path.exists():
        print(f"No log at {path}")
        return 1
    lines = path.read_text(encoding="utf-8", errors="replace").splitlines()
    print("\n".join(lines[-args.lines:]))
    return 0


def install_signal_handlers(loop: asyncio.AbstractEventLoop, daemon) -> None:
    for name in ("SIGTERM", "SIGINT", "SIGBREAK"):
        sig = getattr(signal, name, None)
        if sig is None:
            continue
        try:
            loop.add_signal_handler(sig, daemon.stop)
        except (NotImplementedError, RuntimeError, ValueError):
            # Windows: no loop signal handlers; a plain handler hands over to the loop.
            signal.signal(sig, lambda *_: loop.call_soon_threadsafe(daemon.stop))


def acquire_lock(path: Path):
    """Exclusive lock for the lifetime of the process; None if another daemon holds it."""
    handle = open(path, "a+")  # noqa: SIM115 - stays open until the end
    try:
        if sys.platform == "win32":
            import msvcrt

            handle.seek(0)
            msvcrt.locking(handle.fileno(), msvcrt.LK_NBLCK, 1)
        else:
            import fcntl

            fcntl.flock(handle, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except OSError:
        handle.close()
        return None
    return handle


def cmd_daemon(args) -> int:
    from .daemon import Daemon
    from .logsetup import setup_logging

    harden()
    os.umask(0o077)  # lock file, log and everything else readable only by the user
    lock_path = pf.lock_file()
    lock_path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    lock = acquire_lock(lock_path)
    if lock is None:
        print("uni-vpn daemon is already running", file=sys.stderr)
        return 0
    log, tail = setup_logging(pf.log_file())
    config_error = None
    cfg_path = Path(args.config) if args.config else config.default_path()
    try:
        cfg = _load(args)
    except config.ConfigError as exc:
        cfg = config.Config(**config.ports_from_broken(Path(args.config) if args.config else None))
        config_error = str(exc)
        log.error("%s", exc)
    log.info("uni-vpn %s starting (SOCKS %s, status %s)", __version__, cfg.socks_port, cfg.http_port)
    if pf.IS_WINDOWS:
        from . import windows

        windows.kill_children_with_us()
        windows.allow_ctrl_c_for_children()
        if not windows.is_admin():
            log.warning("Not running elevated: openconnect cannot create the Wintun adapter")

    async def run() -> None:
        daemon = Daemon(cfg, log, config_error=config_error, log_tail=tail, config_path=cfg_path,
                        needs_setup=not cfg_path.exists())
        install_signal_handlers(asyncio.get_running_loop(), daemon)
        await daemon.run()

    asyncio.run(run())
    log.info("uni-vpn stopped")
    return 0


def cmd_service(args) -> int:
    from . import service

    return service.control(args.action)


def cmd_doctor(args) -> int:
    from . import doctor

    checks = doctor.run_checks(Path(args.config) if args.config else None)
    print(doctor.format_checks(checks))
    return 1 if any(c.status == "fail" for c in checks) else 0


def cmd_setup(args) -> int:
    from . import setup

    return setup.setup(args)


def cmd_uninstall(args) -> int:
    from . import setup

    return setup.uninstall(args)


def cmd_update(args) -> int:
    from . import setup

    return setup.update(args)


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="uni-vpn", description="University VPN on demand as a local SOCKS5 proxy")
    parser.add_argument("--config", help=f"Path to config.toml (default: {pf.config_dir() / 'config.toml'})")
    parser.add_argument("--version", action="version", version=f"uni-vpn {__version__}")
    sub = parser.add_subparsers(dest="command", required=True)

    p = sub.add_parser("status", help="Show the state")
    p.add_argument("--json", action="store_true")
    p.set_defaults(func=cmd_status)
    sub.add_parser("connect", help="Bring the tunnel up").set_defaults(func=cmd_connect)
    sub.add_parser("disconnect", help="Tear the tunnel down").set_defaults(func=cmd_disconnect)
    sub.add_parser("password", help="Store the university password in the keyring").set_defaults(func=cmd_password)
    sub.add_parser("totp", help="Store the TOTP secret (second factor) in the keyring").set_defaults(func=cmd_totp)
    p = sub.add_parser("log", help="Show the last log lines")
    p.add_argument("-n", "--lines", type=int, default=200)
    p.set_defaults(func=cmd_log)
    sub.add_parser("doctor", help="Self-diagnosis").set_defaults(func=cmd_doctor)
    sub.add_parser("daemon", help="Run the service in the foreground (used by the background service)").set_defaults(func=cmd_daemon)
    p = sub.add_parser("service", help="Control the service")
    p.add_argument("action", choices=["start", "stop", "restart", "enable", "disable", "status"])
    p.set_defaults(func=cmd_service)
    p = sub.add_parser("setup", help="Set up (called by the installer)")
    p.add_argument("--dry-run", action="store_true")
    p.add_argument("--user", help="University ID")
    p.add_argument("--no-gui", action="store_true", help="Ask in the terminal instead of opening the setup assistant")
    p.add_argument("--no-browser", action="store_true", help=argparse.SUPPRESS)
    p.set_defaults(func=cmd_setup)
    p = sub.add_parser("uninstall", help="Remove the service and files")
    p.add_argument("--yes", action="store_true", help="Delete the keyring entries without asking")
    p.add_argument("--dry-run", action="store_true")
    p.set_defaults(func=cmd_uninstall)
    p = sub.add_parser("update", help="Fetch the latest version and restart the service")
    p.add_argument("--dry-run", action="store_true")
    p.set_defaults(func=cmd_update)
    return parser


def main(argv: list[str] | None = None) -> int:
    raw = sys.argv[1:] if argv is None else argv
    args = build_parser().parse_args([a for a in raw if a != ""])  # install.sh passes empty arguments under bash 3.2
    try:
        return args.func(args)
    except config.ConfigError as exc:
        print(str(exc))
        return 2
    except credentials.KeyringError as exc:
        print(str(exc))
        return 1
    except EOFError:
        print("\nNo terminal to answer questions: run the installer in a terminal")
        return 1
    except KeyboardInterrupt:
        return 130
