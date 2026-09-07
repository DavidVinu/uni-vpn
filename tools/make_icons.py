#!/usr/bin/env python3
"""Erzeugt die Extension-Icons (blauer Kreis) ohne Fremdpakete."""
import struct
import zlib
from pathlib import Path

COLOR = (31, 90, 168)


def png(size: int) -> bytes:
    cx = cy = (size - 1) / 2
    radius = size / 2 - 0.5
    rows = []
    for y in range(size):
        row = bytearray([0])
        for x in range(size):
            distance = ((x - cx) ** 2 + (y - cy) ** 2) ** 0.5
            alpha = max(0.0, min(1.0, radius - distance + 0.5))
            row += bytes([*COLOR, int(255 * alpha)])
        rows.append(bytes(row))

    def chunk(kind: bytes, data: bytes) -> bytes:
        return struct.pack(">I", len(data)) + kind + data + struct.pack(">I", zlib.crc32(kind + data) & 0xFFFFFFFF)

    header = struct.pack(">IIBBBBB", size, size, 8, 6, 0, 0, 0)
    return b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", header) + chunk(b"IDAT", zlib.compress(b"".join(rows))) + chunk(b"IEND", b"")


if __name__ == "__main__":
    out = Path(__file__).resolve().parent.parent / "extension" / "icons"
    out.mkdir(parents=True, exist_ok=True)
    for size in (16, 32, 48, 128):
        (out / f"{size}.png").write_bytes(png(size))
        print(out / f"{size}.png")
