#!/usr/bin/env python3
"""Stand-in for openconnect in tests.

Environment variables:
  FAKE_MODE           ok (default) | auth_fail | input_required | totp_rejected | never_ready | ignore_sigterm | exit_after_ready
  FAKE_DELAY          seconds until the port listens (default 0.2)
  FAKE_EXIT_AFTER     with exit_after_ready: seconds after becoming ready (default 0.5)
  FAKE_PASSWORD_FILE  file the password read from stdin is appended to
  FAKE_TOKEN_FILE     file the contents of --token-secret=@file are appended to
                      (read shortly before becoming ready, like openconnect when generating the code)
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
        # Wrong password (measured 2026-09-08): "Login failed." comes before any OTP prompt,
        # then the server shows the form again and openconnect has no password left.
        time.sleep(delay)
        log("Bitte geben Sie ihren Benutzernamen und ihr Passwort ein.")
        log("Login failed.")
        log("Bitte geben Sie ihren Benutzernamen und ihr Passwort ein.")
        log("Password:")
        log("***")
        log("User input required in non-interactive mode")
        log("Failed to complete authentication")
        sys.exit(1)
    if mode == "input_required":
        log("User input required in non-interactive mode")
        log("Failed to complete authentication")
        sys.exit(1)
    if mode == "totp_rejected":
        # Wrong secret (measured 2026-09-08): the password was accepted, the code rejected;
        # the server sends no second OTP form but starts over.
        log("Bitte geben Sie ihren Benutzernamen und ihr Passwort ein.")
        log("Bitte zweiten Faktor eingeben (OTP) / Please enter second factor (OTP).")
        log("Generating OATH TOTP token code")
        log("Login failed.")
        log("Bitte geben Sie ihren Benutzernamen und ihr Passwort ein.")
        log("Password:")
        log("***")
        log("User input required in non-interactive mode")
        log("Failed to complete authentication")
        sys.exit(1)
    if mode == "never_ready":
        signal.signal(signal.SIGTERM, lambda s, f: os._exit(0))
        time.sleep(3600)

    def on_term(signum, frame):
        if mode == "ignore_sigterm":
            log("SIGTERM ignored")
            return
        log("User cancelled (SIGINT/SIGTERM); exiting.")
        os._exit(0)

    signal.signal(signal.SIGTERM, on_term)
    signal.signal(signal.SIGINT, on_term)
    signal.signal(signal.SIGUSR2, lambda s, f: log("SIGUSR2 received"))

    time.sleep(delay)
    if token_path:
        # Like the real openconnect when logging in with --token-mode=totp (measured 2026-09-08).
        log("Bitte zweiten Faktor eingeben (OTP) / Please enter second factor (OTP).")
        log("Generating OATH TOTP token code")
    if token_path and os.environ.get("FAKE_TOKEN_FILE"):
        # openconnect reads the file only when generating the code, i.e. after starting.
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
