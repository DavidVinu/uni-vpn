"""Domainliste und PAC-Datei (Proxy Auto-Config) fuer die Browser.

Die Browser holen sich die Regel vom Daemon (`/proxy.pac`): gelistete Hosts und ihre
Subdomains gehen ueber SOCKS5 127.0.0.1:<socks_port>, alles andere direkt.
"""

from __future__ import annotations

import json
import re
from pathlib import Path

DEFAULT_DOMAINS = [
    "sogo.uni-heidelberg.de",
    "elearning-med.uni-heidelberg.de",
    # elearning-med bindet von dort matomo.js ein; der Host ist nur im Uni-Netz erreichbar
    # und der Browser wartet sonst bis zum Verbindungs-Timeout (Chrome: 136 s).
    "cip.dmed.uni-heidelberg.de",
]
_LABEL = r"[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?"
HOST_RE = re.compile(rf"^(?=.{{1,253}}$){_LABEL}(?:\.{_LABEL})+$")
HEADER = "# uni-vpn: Domains, die ueber die Uni laufen. Eine je Zeile, gilt auch fuer Subdomains, # leitet Kommentare ein.\n"


def normalize_host(value: str) -> str:
    return str(value or "").strip().lower().rstrip(".")


def parse_domain_list(text: str) -> tuple[list[str], list[str]]:
    """(Domains, Fehler). Fehler nennen die Zeile, damit die Statusseite sie anzeigen kann."""
    domains: list[str] = []
    errors: list[str] = []
    seen: set[str] = set()
    for number, raw in enumerate(str(text or "").splitlines(), start=1):
        line = normalize_host(raw.split("#", 1)[0])
        if not line:
            continue
        if line.startswith("*."):
            line = line[2:]
        if not HOST_RE.fullmatch(line):
            errors.append(f"Zeile {number}: '{raw.strip()}' ist kein Hostname")
            continue
        if line not in seen:
            seen.add(line)
            domains.append(line)
    return domains, errors


def matches(host: str, domains: list[str]) -> bool:
    h = normalize_host(host)
    if not h:
        return False
    return any(h == d or h.endswith("." + d) for d in domains)


def build_pac(domains: list[str], port: int) -> str:
    return "\n".join([
        "function FindProxyForURL(url, host) {",
        f"  var domains = {json.dumps(list(domains))};",
        "  host = host.toLowerCase();",
        '  if (host.charAt(host.length - 1) === ".") host = host.slice(0, -1);',
        "  for (var i = 0; i < domains.length; i++) {",
        "    var d = domains[i];",
        '    if (host === d || (host.length > d.length && host.slice(-(d.length + 1)) === "." + d)) {',
        f'      return "SOCKS5 127.0.0.1:{int(port)}";',
        "    }",
        "  }",
        '  return "DIRECT";',
        "}",
        "",
    ])


def domains_path() -> Path:
    from .platform import config_dir

    return config_dir() / "domains.txt"


def read_domains(path: Path) -> list[str]:
    """Fehlt die Datei, gilt die Vorbelegung; ungueltige Zeilen werden uebergangen."""
    try:
        text = path.read_text(encoding="utf-8")
    except FileNotFoundError:
        return list(DEFAULT_DOMAINS)
    domains, _errors = parse_domain_list(text)
    return domains


def write_domains(path: Path, domains: list[str]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    path.write_text(HEADER + "".join(f"{d}\n" for d in domains), encoding="utf-8")
