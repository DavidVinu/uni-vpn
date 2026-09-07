"""Logging: Datei mit Rotation (0600), Log-Tail fuer die Statusseite, stderr fuer journal/launchd."""

from __future__ import annotations

import collections
import logging
import os
from logging.handlers import RotatingFileHandler
from pathlib import Path


class PrivateRotatingFileHandler(RotatingFileHandler):
    """Legt die Logdatei mit 0600 an, auch nach jeder Rotation und unabhaengig von der Umask."""

    def _open(self):
        fd = os.open(self.baseFilename, os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o600)
        return open(fd, self.mode, encoding=self.encoding, errors=self.errors)


class TailHandler(logging.Handler):
    def __init__(self, maxlen: int = 50):
        super().__init__()
        self.lines: collections.deque[str] = collections.deque(maxlen=maxlen)

    def emit(self, record: logging.LogRecord) -> None:
        self.lines.append(self.format(record))


def setup_logging(path: Path | None, level: int = logging.INFO) -> tuple[logging.Logger, collections.deque[str]]:
    logger = logging.getLogger("uni-vpn")
    logger.setLevel(level)
    logger.handlers.clear()
    logger.propagate = False
    fmt = logging.Formatter("%(asctime)s %(levelname)s %(message)s", "%Y-%m-%d %H:%M:%S")
    tail = TailHandler()
    tail.setFormatter(fmt)
    logger.addHandler(tail)
    if path:
        path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        handler = PrivateRotatingFileHandler(path, maxBytes=1_000_000, backupCount=3, encoding="utf-8")
        os.chmod(path, 0o600)  # bestehende Datei aus frueheren Versionen
        handler.setFormatter(fmt)
        logger.addHandler(handler)
    stream = logging.StreamHandler()
    stream.setFormatter(fmt)
    logger.addHandler(stream)
    return logger, tail.lines
