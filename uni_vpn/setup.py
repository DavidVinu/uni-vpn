"""Einrichten, Entfernen, Aktualisieren. Wird von install.sh und der CLI aufgerufen."""

from __future__ import annotations

import getpass
import os
import subprocess
import sys
import time
import xml.etree.ElementTree as ET
from pathlib import Path

from . import config, credentials, doctor, service
from . import platform as pf
from .tunnel import port_open as _port_open

INSTALLED_FILES = "installed-files.txt"
EXTENSION_HINT = """
Browser-Extension einrichten:
  Chrome:  chrome://extensions -> Entwicklermodus an -> "Entpackte Erweiterung laden" -> Ordner {ext}
  Firefox: about:debugging#/runtime/this-firefox -> "Temporaeres Add-on laden" -> {ext}/manifest.json
Statusseite: http://127.0.0.1:{port}/
"""


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
    """Eintrag wie apports mark_ignore(): <ignore program=... mtime=...>. Liefert den Pfad, wenn die Datei neu angelegt wurde."""
    path = apport_ignore_path()
    created = not path.exists()
    if dry_run:
        print(f"-> wuerde {executable} in {path} eintragen")
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
    """Wartet, bis der Daemon nach dem Dienststart den Port gebunden hat. True, sobald er erreichbar ist."""
    sleep = sleep or time.sleep
    clock = clock or time.monotonic
    deadline = clock() + timeout
    while True:
        if port_open(port):
            return True
        if clock() >= deadline:
            return False
        sleep(step)


def setup(args, *, input_fn=input, getpass_fn=getpass.getpass, service_install=service.install,
          store=credentials.store_password, keyring_probe=doctor.keyring_state, run_doctor=True,
          port_open=_port_open) -> int:
    dry = bool(getattr(args, "dry_run", False))

    def created(path: Path) -> None:
        # Sofort festhalten, damit ein Abbruch weiter unten nichts Unregistriertes hinterlaesst.
        record([path])

    if sys.version_info < (3, 11):
        print(f"Python {sys.version_info.major}.{sys.version_info.minor} ist zu alt, mindestens 3.11 noetig")
        return 1

    needed = ["openconnect", "ocproxy"] + ([] if pf.IS_MACOS else ["secret-tool"])
    missing = [name for name in needed if not pf.find_binary(name)]
    if missing:
        hint = "brew install openconnect ocproxy" if pf.IS_MACOS else "sudo apt install openconnect ocproxy libsecret-tools"
        if dry:
            _say(f"wuerde voraussetzen: {', '.join(missing)} ({hint})")
        else:
            print(f"Fehlt: {', '.join(missing)}. Installieren mit: {hint}")
            return 1
    else:
        _say("openconnect und ocproxy gefunden")

    cfg_path = config.default_path()
    if cfg_path.exists():
        cfg = config.load(cfg_path)
        _say(f"Konfiguration vorhanden: {cfg_path} (Uni-ID {cfg.user})")
    else:
        user = (getattr(args, "user", None) or "").strip()
        if not user:
            if dry and not sys.stdin.isatty():
                user = "beispiel"
            else:
                user = input_fn("Uni-ID (z.B. ab123): ").strip()
        if not user:
            print("Keine Uni-ID angegeben")
            return 1
        if dry:
            _say(f"wuerde {cfg_path} mit Uni-ID {user} anlegen")
            cfg = config.Config(user=user)
        else:
            config.write_initial(cfg_path, user=user)
            created(cfg_path)
            cfg = config.load(cfg_path)
            _say(f"Konfiguration angelegt: {cfg_path}")

    link = Path.home() / ".local" / "bin" / "uni-vpn"
    target = pf.bin_dir() / "uni-vpn"
    # Wrapper statt Symlink: so laeuft immer der Interpreter, mit dem eingerichtet wurde
    # (auf macOS sonst /usr/bin/python3 3.9, wenn Homebrew nicht vorne im PATH steht).
    wrapper = f'#!/bin/sh\nexec "{pf.python_executable()}" "{target}" "$@"\n'
    if dry:
        _say(f"wuerde {link} als Wrapper fuer {target} anlegen")
    else:
        link.parent.mkdir(parents=True, exist_ok=True)
        if link.is_symlink() or link.exists():
            link.unlink()
        link.write_text(wrapper, encoding="utf-8")
        link.chmod(0o755)
        created(link)
        _say(f"Kommando angelegt: {link}")
        if str(link.parent) not in os.environ.get("PATH", "").split(os.pathsep):
            print(f"   Hinweis: {link.parent} ist nicht im PATH, neue Shell oeffnen oder Pfad ergaenzen")

    if not pf.IS_MACOS:
        openconnect = pf.find_binary("openconnect", cfg.openconnect) or "/usr/sbin/openconnect"
        new_file = ensure_apport_ignore(openconnect, dry_run=dry)
        if new_file:
            created(new_file)
        if not dry:
            _say("Crash-Reports fuer openconnect ausgeschlossen (~/.apport-ignore.xml)")

    try:
        service_files = service_install(dry_run=dry)
    except service.ServiceError as exc:
        for path in exc.files:
            created(path)
        print(f"Dienst konnte nicht geladen werden: {exc}")
        return 1
    if not dry:
        for path in service_files:
            created(path)
        _say("Dienst eingerichtet und gestartet")

    if pf.cisco_installed():
        print("   Hinweis: Cisco Secure Client ist installiert. Nicht gleichzeitig verbinden; uni-vpn pausiert solange.")
        print("   Empfehlung: im Cisco-Client 'Beim Start automatisch verbinden' abschalten.")

    if dry:
        _say("wuerde nach dem Uni-Passwort fragen und es im Keyring ablegen")
    else:
        state = keyring_probe(cfg.user)
        if state == "present":
            _say("Passwort ist bereits im Keyring")
        else:
            password = getpass_fn(f"Uni-Passwort fuer {cfg.user} (landet nur im Keyring): ")
            if password:
                store(cfg.user, password)
                _say("Passwort im Keyring abgelegt")
            else:
                print("   Kein Passwort eingegeben, spaeter: uni-vpn password")

    if run_doctor and not dry:
        # Der Dienst ist gestartet, aber der Daemon braucht einen Moment bis zum bind().
        wait_for_port(cfg.http_port, port_open=port_open)
        print()
        print(doctor.format_checks(doctor.run_checks(cfg_path)))
    print(EXTENSION_HINT.format(ext=pf.repo_root() / "extension", port=cfg.http_port))
    return 0


def uninstall(args, *, input_fn=input, service_uninstall=service.uninstall, delete=credentials.delete_password) -> int:
    dry = bool(getattr(args, "dry_run", False))
    user = None
    try:
        user = config.load(config.default_path()).user
    except config.ConfigError:
        pass
    if dry:
        _say("wuerde Dienst entfernen und diese Dateien loeschen:")
        for path in recorded():
            print(f"   {path}")
        return 0
    service_uninstall()
    _say("Dienst entfernt")
    for path in recorded():
        if path.is_symlink() or path.exists():
            path.unlink()
            _say(f"geloescht: {path}")
    if _records_path().exists():
        _records_path().unlink()
    for name in ("daemon.lock",):
        stale = pf.config_dir() / name
        if stale.exists():
            stale.unlink()
    if user:
        answer = "j" if getattr(args, "yes", False) else input_fn(f"Passwort fuer {user} aus dem Keyring loeschen? [j/N] ").strip().lower()
        if answer in ("j", "ja", "y", "yes"):
            _say("Keyring-Eintrag geloescht" if delete(user) else "Keyring-Eintrag war nicht vorhanden")
    print("Bleibt bestehen: Pakete (openconnect, ocproxy), die Extension im Browser (dort entfernen), "
          f"das Repo {pf.repo_root()} und das Log unter {pf.state_dir()}")
    return 0


def update(args, run=subprocess.run) -> int:
    dry = bool(getattr(args, "dry_run", False))
    repo = pf.repo_root()
    if dry:
        _say(f"wuerde git pull in {repo} ausfuehren und den Dienst neu starten")
        return 0
    result = run(["git", "-C", str(repo), "pull", "--ff-only"], text=True)
    if result.returncode != 0:
        print("git pull fehlgeschlagen")
        return result.returncode
    rc = service.control("restart", run=run)
    print("Dienst neu gestartet. Extension: in chrome://extensions auf Aktualisieren klicken, Firefox neu laden.")
    return rc
