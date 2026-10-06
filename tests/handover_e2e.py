"""End to end: an installation of the Python core moves over to the Go core the way users' copies
will (docs/go-switch.md). The Python updater finds a new version whose release says "handover",
installs it together with the Go core, and the service started the way the registered service
starts it ("python bin/uni-vpn daemon") is then the Go core.

Usage: python3 tests/handover_e2e.py PATH-TO-GO-CORE COMMIT
The Go core must have been built with COMMIT embedded. Runs on Linux, macOS and Windows.
"""

from __future__ import annotations

import hashlib
import io
import json
import os
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import urllib.request
import zipfile
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))

from uni_vpn import updater  # noqa: E402

OLD = "a" * 40
WINDOWS = sys.platform == "win32"


class Response(io.BytesIO):
    def __enter__(self):
        return self

    def __exit__(self, *exc):
        return False


def zipped(files: dict[str, bytes], comment: bytes = b"") -> bytes:
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w", zipfile.ZIP_DEFLATED) as zf:
        for name, data in files.items():
            zf.writestr(name, data)
        zf.comment = comment
    return buf.getvalue()


def source_files() -> dict[str, bytes]:
    files = {}
    for top in ("uni_vpn", "bin"):
        for path in (ROOT / top).rglob("*"):
            if path.is_file() and "__pycache__" not in path.parts and not path.name.startswith("uni-vpn-core"):
                files[path.relative_to(ROOT).as_posix()] = path.read_bytes()
    return files


def free_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def go_core_running(pid: int) -> bool:
    if WINDOWS:
        out = subprocess.run(["tasklist", "/FI", "IMAGENAME eq uni-vpn-core.exe", "/FO", "CSV", "/NH"],
                             capture_output=True, text=True).stdout
        return "uni-vpn-core.exe" in out
    # Same process id: bin/uni-vpn replaced itself with the Go core.
    out = subprocess.run(["ps", "-o", "comm=", "-p", str(pid)], capture_output=True, text=True).stdout
    return out.strip().endswith("uni-vpn-core")


def main() -> int:
    core, commit = Path(sys.argv[1]), sys.argv[2]
    tmp = Path(tempfile.mkdtemp(prefix="uni-vpn-handover-"))
    app = tmp / "app"
    files = source_files()
    for name, data in files.items():
        (app / name).parent.mkdir(parents=True, exist_ok=True)
        (app / name).write_bytes(data)
    (app / updater.COMMIT_FILE).write_text(OLD + "\n")

    binary = updater.core_binary()
    name = updater.core_name()
    core_zip = zipped({binary: core.read_bytes()})
    release = {
        updater.LATEST_URL: commit.encode(),
        updater.ARCHIVE_URL.format(commit=commit): zipped({f"uni-vpn-{commit}/{n}": d for n, d in files.items()},
                                                          commit.encode()),
        updater.CORE_MANIFEST_URL: json.dumps({"commit": commit, "handover": True, "files": {name: {
            "sha256": hashlib.sha256(core_zip).hexdigest(), "size": len(core_zip)}}}).encode(),
        updater.CORE_FILE_URL.format(name=name): core_zip,
    }

    def opener(request, timeout=None):
        return Response(release[getattr(request, "full_url", request)])

    u = updater.Updater(app, opener=opener)
    assert u.check() == commit, "no update found"
    u.apply()
    assert (app / binary).is_file(), "the Go core was not installed"
    assert updater.installed_commit(app) == commit
    print("installed", commit, "with", binary)

    http_port, socks_port = free_port(), free_port()
    env = dict(os.environ, XDG_CONFIG_HOME=str(tmp / "config"), XDG_STATE_HOME=str(tmp / "state"),
               XDG_DATA_HOME=str(tmp / "data"))
    env.pop("UNI_VPN_PYTHON", None)
    (tmp / "config" / "uni-vpn").mkdir(parents=True)
    (tmp / "config" / "uni-vpn" / "config.toml").write_text(
        f'university = "heidelberg"\nuser = "ab123"\nsocks_port = {socks_port}\nhttp_port = {http_port}\n'
        "auto_update = false\n", encoding="utf-8")
    # The command line the Python setup registered with the service manager.
    process = subprocess.Popen([sys.executable, "-I", str(app / "bin" / "uni-vpn"), "daemon"], env=env)
    try:
        status = None
        for _ in range(60):
            try:
                with urllib.request.urlopen(f"http://127.0.0.1:{http_port}/status.json", timeout=2) as response:
                    status = json.load(response)
                break
            except OSError:
                if process.poll() is not None:
                    break
                time.sleep(0.5)
        log = tmp / "state" / "uni-vpn" / "daemon.log"
        if status is None:
            print(log.read_text(errors="replace") if log.exists() else "no log")
            raise SystemExit("the service did not answer")
        print("status:", status["state"], status.get("commit"))
        assert status.get("commit") == commit, status.get("commit")
        assert go_core_running(process.pid), "the service is not the Go core"
        print("the service is the Go core")
    finally:
        process.terminate()
        try:
            process.wait(15)
        except subprocess.TimeoutExpired:
            process.kill()
        if WINDOWS:
            subprocess.run(["taskkill", "/F", "/IM", "uni-vpn-core.exe"], capture_output=True)
        shutil.rmtree(tmp, ignore_errors=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
