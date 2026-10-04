"""Validate and normalize the TOTP secret; compute the check code (RFC 6238).

openconnect generates the codes at login (--token-mode=totp). This module only brings the
user's input into the format openconnect understands ("base32:...") and computes a check
code the user can compare against their app.
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
MIN_CHARS = 16  # 80 bits, no portal issues less


def normalize(text: str) -> str:
    """otpauth URL or Base32 text -> openconnect token ("base32:..." or "sha256:base32:...")."""
    text = text.strip()
    if "\n" in text or "\r" in text:
        raise ValueError("Secret must not contain a line break")
    algorithm = "SHA1"
    if text.lower().startswith("otpauth:"):
        parts = urlsplit(text)
        if parts.netloc.lower() != "totp":
            raise ValueError("Only time-based tokens (otpauth://totp/...) are supported")
        query = {k: v[-1] for k, v in parse_qs(parts.query, keep_blank_values=True).items()}
        secret = unquote(query.get("secret", ""))
        algorithm = query.get("algorithm", "SHA1").upper()
        if algorithm not in ALGORITHMS:
            raise ValueError(f"Algorithm {algorithm} is not supported")
        if query.get("digits", "6") != "6" or query.get("period", "30") != "30":
            raise ValueError("Only 6 digits and 30 seconds are supported")
    else:
        secret = text
    cleaned = re.sub(r"[\s\-]", "", secret).upper().rstrip("=")
    if not cleaned:
        raise ValueError("No secret given")
    if not BASE32.fullmatch(cleaned):
        raise ValueError("Secret must consist of Base32 characters (A-Z, 2-7)")
    if len(cleaned) < MIN_CHARS:
        raise ValueError("Secret is too short")
    _decode(cleaned)
    token = f"base32:{cleaned}"
    return token if algorithm == "SHA1" else f"{algorithm.lower()}:{token}"


def _decode(cleaned: str) -> bytes:
    padded = cleaned + "=" * (-len(cleaned) % 8)
    try:
        return base64.b32decode(padded)
    except ValueError:
        raise ValueError("Secret is not valid Base32") from None


def code(token: str, now: float | None = None) -> str:
    """Six-digit code for a normalized token, as openconnect generates it."""
    digest = "sha1"
    for name in ("sha512", "sha256", "sha1"):
        if token.startswith(f"{name}:"):
            digest = name
            token = token[len(name) + 1:]
            break
    if not token.startswith("base32:"):
        raise ValueError("Unknown token format")
    key = _decode(token[len("base32:"):])
    counter = int((time.time() if now is None else now) // 30)
    mac = hmac.new(key, struct.pack(">Q", counter), getattr(hashlib, digest)).digest()
    offset = mac[-1] & 0x0F
    number = struct.unpack(">I", mac[offset:offset + 4])[0] & 0x7FFFFFFF
    return f"{number % 1_000_000:06d}"
