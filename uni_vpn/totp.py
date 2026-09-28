"""TOTP-Schluessel pruefen und normalisieren; Kontrollcode berechnen (RFC 6238).

Die Erzeugung der Codes beim Login uebernimmt openconnect (--token-mode=totp). Hier wird nur
die Nutzereingabe in das Format gebracht, das openconnect versteht ("base32:..."), und ein
Kontrollcode berechnet, mit dem der Nutzer die Eingabe gegen seine App pruefen kann.
"""

from __future__ import annotations

import base64
import hashlib
import hmac
import re
import struct
import time
from urllib.parse import parse_qs, unquote, urlsplit

ALGORITHMS = {"SHA1": "sha1", "SHA256": "sha256", "SHA512": "sha512"}
BASE32 = re.compile(r"^[A-Z2-7]+$")
MIN_CHARS = 16  # 80 Bit, weniger liefert kein Portal


def normalize(text: str) -> str:
    """otpauth-URL oder Base32-Text -> openconnect-Token ("base32:..." oder "sha256:base32:...")."""
    text = text.strip()
    if "\n" in text or "\r" in text:
        raise ValueError("Schluessel darf keinen Zeilenumbruch enthalten")
    algorithm = "SHA1"
    if text.lower().startswith("otpauth:"):
        parts = urlsplit(text)
        if parts.netloc.lower() != "totp":
            raise ValueError("Nur zeitbasierte Token (otpauth://totp/...) werden unterstuetzt")
        query = {k: v[-1] for k, v in parse_qs(parts.query, keep_blank_values=True).items()}
        secret = unquote(query.get("secret", ""))
        algorithm = query.get("algorithm", "SHA1").upper()
        if algorithm not in ALGORITHMS:
            raise ValueError(f"Algorithmus {algorithm} wird nicht unterstuetzt")
        if query.get("digits", "6") != "6" or query.get("period", "30") != "30":
            raise ValueError("Nur 6 Ziffern und 30 Sekunden werden unterstuetzt")
    else:
        secret = text
    cleaned = re.sub(r"[\s\-]", "", secret).upper().rstrip("=")
    if not cleaned:
        raise ValueError("Kein Schluessel angegeben")
    if not BASE32.fullmatch(cleaned):
        raise ValueError("Schluessel muss aus Base32-Zeichen bestehen (A-Z, 2-7)")
    if len(cleaned) < MIN_CHARS:
        raise ValueError("Schluessel ist zu kurz")
    _decode(cleaned)
    token = f"base32:{cleaned}"
    return token if algorithm == "SHA1" else f"{algorithm.lower()}:{token}"


def _decode(cleaned: str) -> bytes:
    padded = cleaned + "=" * (-len(cleaned) % 8)
    try:
        return base64.b32decode(padded)
    except ValueError:
        raise ValueError("Schluessel ist kein gueltiges Base32") from None


def code(token: str, now: float | None = None) -> str:
    """Sechsstelliger Code fuer einen normalisierten Token, wie openconnect ihn erzeugt."""
    digest = "sha1"
    for name in ("sha512", "sha256", "sha1"):
        if token.startswith(f"{name}:"):
            digest = name
            token = token[len(name) + 1:]
            break
    if not token.startswith("base32:"):
        raise ValueError("Unbekanntes Token-Format")
    key = _decode(token[len("base32:"):])
    counter = int((time.time() if now is None else now) // 30)
    mac = hmac.new(key, struct.pack(">Q", counter), getattr(hashlib, digest)).digest()
    offset = mac[-1] & 0x0F
    number = struct.unpack(">I", mac[offset:offset + 4])[0] & 0x7FFFFFFF
    return f"{number % 1_000_000:06d}"
