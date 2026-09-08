#!/usr/bin/env python3
"""Ersatz fuer openconnect in Tests.

Umgebungsvariablen:
  FAKE_MODE           ok (Default) | auth_fail | input_required | totp_rejected | never_ready | ignore_sigterm | exit_after_ready
  FAKE_DELAY          Sekunden bis der Port lauscht (Default 0.2)
  FAKE_EXIT_AFTER     bei exit_after_ready: Sekunden nach Bereitschaft (Default 0.5)
  FAKE_PASSWORD_FILE  Datei, an die das per stdin gelesene Passwort angehaengt wird
  FAKE_TOKEN_FILE     Datei, an die der Inhalt der --token-secret=@Datei angehaengt wird
                      (gelesen kurz vor der Bereitschaft, wie openconnect beim Erzeugen des Codes)
"""
import os
import signal
import socket
import sys
import threading
import time


def log(text):
    sys.stderr.write(text + "\n")
    sys.stderr.flush()


def echo(conn):
    try:
        while True:
            data = conn.recv(65536)
            if not data:
                break
            conn.sendall(data)
    except OSError:
        pass
    finally:
        conn.close()


def main():
    port = None
    token_path = None
    for arg in sys.argv[1:]:
        if arg.startswith("--script="):
            port = int(arg.split()[-1])
        if arg.startswith("--token-secret=@"):
            token_path = arg[len("--token-secret=@"):]
    password = sys.stdin.readline()
    if os.environ.get("FAKE_PASSWORD_FILE"):
        with open(os.environ["FAKE_PASSWORD_FILE"], "a", encoding="utf-8") as handle:
            handle.write(password)
    log("POST https://fake.example/")
    mode = os.environ.get("FAKE_MODE", "ok")
    delay = float(os.environ.get("FAKE_DELAY", "0.2"))

    if mode == "auth_fail":
        time.sleep(delay)
        log("Failed to complete authentication")
        sys.exit(1)
    if mode == "input_required":
        log("User input required in non-interactive mode")
        log("Failed to complete authentication")
        sys.exit(1)
    if mode == "totp_rejected":
        log("Server is rejecting the soft token; switching to manual entry")
        log("Bitte zweiten Faktor eingeben (OTP):***")
        log("User input required in non-interactive mode")
        log("Failed to complete authentication")
        sys.exit(1)
    if mode == "never_ready":
        signal.signal(signal.SIGTERM, lambda s, f: os._exit(0))
        time.sleep(3600)

    def on_term(signum, frame):
        if mode == "ignore_sigterm":
            log("SIGTERM ignoriert")
            return
        log("User cancelled (SIGINT/SIGTERM); exiting.")
        os._exit(0)

    signal.signal(signal.SIGTERM, on_term)
    signal.signal(signal.SIGINT, on_term)
    signal.signal(signal.SIGUSR2, lambda s, f: log("SIGUSR2 empfangen"))

    time.sleep(delay)
    if token_path and os.environ.get("FAKE_TOKEN_FILE"):
        # openconnect liest die Datei erst beim Erzeugen des Codes, also nach dem Start.
        with open(token_path, encoding="utf-8") as handle, open(os.environ["FAKE_TOKEN_FILE"], "a", encoding="utf-8") as out:
            out.write(handle.read().rstrip("\n") + "\n")
    server = socket.socket()
    server.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    server.bind(("127.0.0.1", port))
    server.listen(16)
    server.settimeout(0.2)
    log("Connected as 10.0.0.2, using SSL")
    ready_at = time.monotonic()
    while True:
        if mode == "exit_after_ready" and time.monotonic() - ready_at > float(os.environ.get("FAKE_EXIT_AFTER", "0.5")):
            log("Session terminated by server")
            sys.exit(1)
        try:
            conn, _ = server.accept()
        except socket.timeout:
            continue
        threading.Thread(target=echo, args=(conn,), daemon=True).start()


if __name__ == "__main__":
    main()
