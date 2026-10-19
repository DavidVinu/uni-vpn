"""Domain list and PAC file (Proxy Auto-Config) for the browsers.

The browsers fetch the rule from the daemon (`/proxy.pac`): listed hosts and their
subdomains go via SOCKS5 127.0.0.1:<socks_port>, everything else direct.
"""

from __future__ import annotations

import json
import re
from pathlib import Path

from .i18n import t

DEFAULT_DOMAINS = [
    "sogo.uni-heidelberg.de",
    "elearning-med.uni-heidelberg.de",
    # elearning-med embeds matomo.js from there; the host is reachable only on the university
    # network, and otherwise the browser waits until the connection times out (Chrome: 136 s).
    "cip.dmed.uni-heidelberg.de",
    # heiCO login uses the UHDLOGINTERNAL realm, which answers 403 "Nur intern zugaenglich"
    # outside the university network.
    "heico.uni-heidelberg.de",
    # Moodle (and exam.moodle) open only on the university network or over the VPN since
    # July 2024 (URZ).
    "moodle.uni-heidelberg.de",
    # YoKI (the AI platform) and the Microsoft 365 license portal open only on the university
    # network or over the VPN (URZ).
    "yoki.urz.uni-heidelberg.de",
    "m365.urz.uni-heidelberg.de",
    # The people, institutions and rooms search and the phone book in the search panel of every
    # university page, and URZ's IT chatbot: only on the university network or over the VPN.
    "lsf.uni-heidelberg.de",
    "telefonbuch.uni-heidelberg.de",
    "heibot-it.urz.uni-heidelberg.de",
    # heiDOCS, the doctoral candidates' online file: behind the same campus-only proxy as LSF.
    "doktoranden.uni-heidelberg.de",
    # The web print portal (Ricoh myPrint): only on the university network.
    "myprint.uni-heidelberg.de",
]
_LABEL = r"[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?"
HOST_RE = re.compile(rf"^(?=.{{1,253}}$){_LABEL}(?:\.{_LABEL})+$")
HEADER = "# uni-vpn: domains that go through the university. One per line, also covers subdomains, # starts a comment.\n"


def normalize_host(value: str) -> str:
    return str(value or "").strip().lower().rstrip(".")


def parse_domain_list(text: str) -> tuple[list[str], list[str]]:
    """(domains, errors). Errors name the line so the status page can show them."""
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
            errors.append(t("domains.not_hostname", line=number, text=raw.strip()))
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


def build_pac(domains: list[str], port: int, or_direct: list[str] | tuple[str, ...] = ()) -> str:
    """or_direct: hosts that go via the port or, when it refuses, directly (as the Go core)."""
    # Twice: Safari (CFNetwork) drops SOCKS5 and speaks SOCKS5 behind SOCKS, as the Go core.
    address = f"127.0.0.1:{int(port)}"
    socks = f"SOCKS5 {address}; SOCKS {address}"

    def loop(name: str, result: str) -> list[str]:
        return [
            f"  for (var i = 0; i < {name}.length; i++) {{",
            f"    var d = {name}[i];",
            '    if (host === d || (host.length > d.length && host.slice(-(d.length + 1)) === "." + d)) {',
            f'      return "{result}";',
            "    }",
            "  }",
        ]

    return "\n".join([
        "function FindProxyForURL(url, host) {",
        *([f"  var orDirect = {json.dumps(list(or_direct))};"] if or_direct else []),
        f"  var domains = {json.dumps(list(domains))};",
        "  host = host.toLowerCase();",
        '  if (host.charAt(host.length - 1) === ".") host = host.slice(0, -1);',
        *(loop("orDirect", socks + "; DIRECT") if or_direct else []),
        *loop("domains", socks),
        '  return "DIRECT";',
        "}",
        "",
    ])


def domains_path() -> Path:
    from .platform import config_dir

    return config_dir() / "domains.txt"


def read_domains(path: Path, defaults: list[str] | None = None) -> list[str]:
    """If the file is missing, the university's defaults apply (without them Heidelberg's);
    invalid lines are skipped."""
    try:
        text = path.read_text(encoding="utf-8")
    except FileNotFoundError:
        return list(DEFAULT_DOMAINS if defaults is None else defaults)
    domains, _errors = parse_domain_list(text)
    return domains


def write_domains(path: Path, domains: list[str]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    path.write_text(HEADER + "".join(f"{d}\n" for d in domains), encoding="utf-8")
