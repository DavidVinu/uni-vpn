"""Tunnel on Windows: openconnect with a Wintun adapter plus the built-in SOCKS server.

openconnect cannot pass the tunnel to a program on Windows (no --script-tun), so it creates
a Wintun adapter (administrator rights, hence the elevated scheduled task). Our
bin/uni-vpn-vpnc.js only gives the adapter its address and a default route with a metric so
high that no other program uses it, and writes address and DNS servers to a file. The SOCKS
server then connects from that address, which Windows sends through the adapter.
"""

from __future__ import annotations

import asyncio
import logging
from pathlib import Path

from . import platform as pf
from .config import Config
from .socks import SocksServer
from .tunnel import Tunnel

INTERFACE = "uni-vpn"
STATE_PREFIX = "tunnel-"


def parse_state(text: str) -> dict[str, str]:
    values = {}
    for line in text.splitlines():
        key, sep, value = line.partition("=")
        if sep:
            values[key.strip()] = value.strip()
    return values


def dns_servers(values: dict[str, str]) -> list[str]:
    return [server for server in values.get("INTERNAL_IP4_DNS", "").split() if server]


class WindowsTunnel(Tunnel):
    def __init__(self, cfg: Config, openconnect: str, script: str, log: logging.Logger,
                 token_dir: Path | None = None, python: str | None = None):
        super().__init__(cfg, openconnect, script, log, ocproxy=None, token_dir=token_dir)
        self.script = script
        self.python = python or pf.python_executable()
        self.state_file: Path | None = None
        self.socks: SocksServer | None = None
        self._server_task: asyncio.Task | None = None

    def command(self, port: int) -> list[str]:
        cmd = [
            self.openconnect,
            "--protocol=anyconnect",
            f"--useragent={self.cfg.useragent}",
            f"--user={self.cfg.user}",
            "--passwd-on-stdin",
            "--non-inter",
            "--no-dtls",
            "--force-dpd=30",
            "--reconnect-timeout=60",
            f"--interface={INTERFACE}",
            # openconnect runs it as: cscript.exe /e:JScript "<script>"
            f"--script={self.script}",
        ]
        if self.token_file:
            cmd += ["--token-mode=totp", f"--token-secret=@{self.token_file}"]
        return cmd + [self.cfg.host]

    def _env(self) -> dict[str, str]:
        env = super()._env()
        self.state_file = self.token_dir / f"{STATE_PREFIX}{self.port}.env"
        self.state_file.unlink(missing_ok=True)
        env["UNI_VPN_STATE"] = str(self.state_file)
        return env

    def _spawn_kwargs(self) -> dict:
        from . import windows

        # A console of its own (hidden) so Ctrl+C can reach openconnect for a clean logout.
        return {"creationflags": windows.CREATE_NEW_CONSOLE, "startupinfo": windows.hidden_console_startupinfo()}

    async def start(self, password: bytes, totp: str | None = None) -> None:
        self.token_dir.mkdir(parents=True, exist_ok=True)
        await super().start(password, totp)
        self._server_task = asyncio.create_task(self._serve_when_up())

    async def _serve_when_up(self) -> None:
        """Start the SOCKS server on self.port as soon as the script reports the address."""
        while not self.exited.is_set():
            values = self._read_state()
            if values.get("INTERNAL_IP4_ADDRESS"):
                self.socks = SocksServer(self.port, values["INTERNAL_IP4_ADDRESS"], dns_servers(values), self.log)
                try:
                    await self.socks.start()
                except OSError as exc:
                    self.log.error("SOCKS server on port %s failed: %s", self.port, exc)
                    self.socks = None
                    return
                self.log.info("Tunnel address %s, DNS %s", values["INTERNAL_IP4_ADDRESS"],
                              " ".join(dns_servers(values)) or "(none)")
                return
            await asyncio.sleep(0.2)

    def _read_state(self) -> dict[str, str]:
        if self.state_file is None:
            return {}
        try:
            return parse_state(self.state_file.read_text(encoding="utf-8", errors="replace"))
        except OSError:
            return {}

    async def stop(self, grace: float) -> None:
        self._remove_token_file()
        if self.proc is not None and not self.exited.is_set():
            self.stopped_by_us = True
            from . import windows

            loop = asyncio.get_running_loop()
            sent = await loop.run_in_executor(None, windows.send_ctrl_c, self.proc.pid, self.python)
            try:
                if not sent:
                    raise asyncio.TimeoutError
                await asyncio.wait_for(self.exited.wait(), grace)
            except asyncio.TimeoutError:
                self.log.warning("openconnect does not respond to Ctrl+C, terminating it")
                try:
                    self.proc.kill()
                except ProcessLookupError:
                    pass
                await self.exited.wait()
        await self._stop_server()

    async def _stop_server(self) -> None:
        if self._server_task and not self._server_task.done():
            self._server_task.cancel()
        if self.socks:
            await self.socks.stop()
            self.socks = None
        if self.state_file:
            self.state_file.unlink(missing_ok=True)

    async def _read_output(self) -> None:
        await super()._read_output()
        # openconnect is gone (logout, server, crash): the source address no longer exists.
        await self._stop_server()

    def _kill_wrapper(self) -> None:
        pass


def remove_stale_state_files(directory: Path) -> int:
    removed = 0
    for entry in directory.glob(f"{STATE_PREFIX}*.env") if directory.exists() else []:
        try:
            entry.unlink()
            removed += 1
        except OSError:
            pass
    return removed
