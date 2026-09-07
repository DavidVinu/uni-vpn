"""Kommandozeile: uni-vpn <kommando>."""

from __future__ import annotations

import argparse
import asyncio
import ctypes
import fcntl
import getpass
import json
import os
import re
import resource
import signal
import sys
import urllib.error
import urllib.request
from pathlib import Path

from . import __version__, config, credentials
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
            f"SOCKS 127.0.0.1:{status['socks_port']}, {status['active_connections']} Verbindungen)")


def harden() -> None:
    """Keine Coredumps mit Passwort im Speicher."""
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


UNREACHABLE_HINT = "Daemon nicht erreichbar. Starten mit: uni-vpn service start, dann uni-vpn doctor"


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
    print("Verbindungsaufbau angestossen, Status: uni-vpn status")
    return 0


def cmd_disconnect(args) -> int:
    cfg = _load(args)
    try:
        api_post(cfg, "/api/disconnect")
    except DaemonUnreachable:
        print(UNREACHABLE_HINT)
        return 1
    print("Getrennt")
    return 0


def cmd_password(args) -> int:
    cfg = _load(args)
    password = getpass.getpass(f"Uni-Passwort fuer {cfg.user}: ")
    if not password:
        print("Kein Passwort eingegeben")
        return 2
    if "\n" in password or "\r" in password:
        print("Passwort darf keinen Zeilenumbruch enthalten")
        return 2
    credentials.store_password(cfg.user, password)
    print("Passwort im Keyring abgelegt")
    try:
        api_post(cfg, "/api/connect")
    except DaemonUnreachable:
        pass
    return 0


def cmd_log(args) -> int:
    path = pf.log_file()
    if not path.exists():
        print(f"Kein Log unter {path}")
        return 1
    lines = path.read_text(encoding="utf-8", errors="replace").splitlines()
    print("\n".join(lines[-args.lines:]))
    return 0


_PORT_LINE = re.compile(r"^\s*(socks_port|http_port)\s*=\s*([0-9]{1,5})\s*(#.*)?$")


def _ports_from_broken_config(path: Path | None) -> dict[str, int]:
    """Ports bestmoeglich aus einer fehlerhaften config.toml lesen.

    Die Statusseite muss auch dann dort erreichbar sein, wo Browser und Extension sie
    erwarten, sonst sieht niemand die Fehlermeldung mit der Zeilennummer.
    """
    try:
        text = (path or config.default_path()).read_text(encoding="utf-8")
    except OSError:
        return {}
    ports: dict[str, int] = {}
    for line in text.splitlines():
        if line.strip().startswith("["):
            break  # ab hier Tabellen wie [timing], keine Top-Level-Schluessel mehr
        match = _PORT_LINE.match(line)
        if match and 1 <= int(match.group(2)) <= 65535:
            ports[match.group(1)] = int(match.group(2))
    if len(ports) == 2 and ports["socks_port"] == ports["http_port"]:
        return {}
    return ports


def install_signal_handlers(loop: asyncio.AbstractEventLoop, daemon) -> None:
    for sig in (signal.SIGTERM, signal.SIGINT):
        loop.add_signal_handler(sig, daemon.stop)


def cmd_daemon(args) -> int:
    from .daemon import Daemon
    from .logsetup import setup_logging

    harden()
    os.umask(0o077)  # Lock-Datei, Log und alles Weitere nur fuer den Nutzer lesbar
    lock_path = pf.lock_file()
    lock_path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    lock = open(lock_path, "w")  # noqa: SIM115 - bleibt bis zum Ende offen
    try:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except OSError:
        print("uni-vpn daemon laeuft bereits", file=sys.stderr)
        return 0
    log, tail = setup_logging(pf.log_file())
    config_error = None
    try:
        cfg = _load(args)
    except config.ConfigError as exc:
        cfg = config.Config(**_ports_from_broken_config(Path(args.config) if args.config else None))
        config_error = str(exc)
        log.error("%s", exc)
    log.info("uni-vpn %s startet (SOCKS %s, Status %s)", __version__, cfg.socks_port, cfg.http_port)

    async def run() -> None:
        daemon = Daemon(cfg, log, config_error=config_error, log_tail=tail)
        install_signal_handlers(asyncio.get_running_loop(), daemon)
        await daemon.run()

    asyncio.run(run())
    log.info("uni-vpn beendet")
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
    parser = argparse.ArgumentParser(prog="uni-vpn", description="Uni-VPN bei Bedarf als lokaler SOCKS5-Proxy")
    parser.add_argument("--config", help="Pfad zur config.toml (Default: ~/.config/uni-vpn/config.toml)")
    parser.add_argument("--version", action="version", version=f"uni-vpn {__version__}")
    sub = parser.add_subparsers(dest="command", required=True)

    p = sub.add_parser("status", help="Zustand anzeigen")
    p.add_argument("--json", action="store_true")
    p.set_defaults(func=cmd_status)
    sub.add_parser("connect", help="Tunnel aufbauen").set_defaults(func=cmd_connect)
    sub.add_parser("disconnect", help="Tunnel abbauen").set_defaults(func=cmd_disconnect)
    sub.add_parser("password", help="Uni-Passwort im Keyring ablegen").set_defaults(func=cmd_password)
    p = sub.add_parser("log", help="letzte Logzeilen")
    p.add_argument("-n", "--lines", type=int, default=200)
    p.set_defaults(func=cmd_log)
    sub.add_parser("doctor", help="Selbstdiagnose").set_defaults(func=cmd_doctor)
    sub.add_parser("daemon", help="Dienst im Vordergrund (fuer systemd/launchd)").set_defaults(func=cmd_daemon)
    p = sub.add_parser("service", help="Dienst steuern")
    p.add_argument("action", choices=["start", "stop", "restart", "enable", "disable", "status"])
    p.set_defaults(func=cmd_service)
    p = sub.add_parser("setup", help="Einrichten (wird von install.sh aufgerufen)")
    p.add_argument("--dry-run", action="store_true")
    p.add_argument("--user", help="Uni-ID")
    p.set_defaults(func=cmd_setup)
    p = sub.add_parser("uninstall", help="Dienst und Dateien entfernen")
    p.add_argument("--yes", action="store_true", help="Keyring-Eintrag ohne Rueckfrage loeschen")
    p.add_argument("--dry-run", action="store_true")
    p.set_defaults(func=cmd_uninstall)
    p = sub.add_parser("update", help="git pull und Dienst neu starten")
    p.add_argument("--dry-run", action="store_true")
    p.set_defaults(func=cmd_update)
    return parser


def main(argv: list[str] | None = None) -> int:
    raw = sys.argv[1:] if argv is None else argv
    args = build_parser().parse_args([a for a in raw if a != ""])  # install.sh liefert unter bash 3.2 leere Argumente
    try:
        return args.func(args)
    except config.ConfigError as exc:
        print(str(exc))
        return 2
    except credentials.KeyringError as exc:
        print(str(exc))
        return 1
    except KeyboardInterrupt:
        return 130
