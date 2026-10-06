# Switching users from the Python core to the Go core

The Go core (`cmd/uni-vpn`, `internal/*`) serves the same local API as the Python service
(checked by `tests/contract`). This is how installed copies move over without anyone running a
command, and how the Go core updates itself afterwards.

## Files

The Go binary is `bin/uni-vpn-core` (`bin\uni-vpn-core.exe` on Windows), next to the helpers it
already looks for (`bin/uni-vpn-vpnc.js`, `bin/uni-vpn-ocproxy`). `bin/uni-vpn` stays the Python
entry point.

## Hand-over

`bin/uni-vpn` hands every command to `bin/uni-vpn-core` when that file exists next to it (and
`UNI_VPN_PYTHON` is not set): `os.execv` on Linux and macOS (same process id, so systemd and
launchd do not notice), a child process on Windows whose exit code it returns (the Python
supervisor in `cli.restart_daemon` keeps working: exit 75 still means "start me again").

So the service registrations written by Python setup (`python bin/uni-vpn daemon`) keep working
unchanged, and switching an installation over means putting one file in place. Removing it
switches back. Go setup registers the service with `bin/uni-vpn-core daemon` directly.

## Release files

CI builds (`packaging/build-core.sh`), on every push, one zip per platform holding the binary:

    uni-vpn-core-linux-amd64.zip     bin/uni-vpn-core
    uni-vpn-core-linux-arm64.zip     bin/uni-vpn-core
    uni-vpn-core-darwin-arm64.zip    bin/uni-vpn-core
    uni-vpn-core-darwin-amd64.zip    bin/uni-vpn-core
    uni-vpn-core-windows-amd64.zip   bin/uni-vpn-core.exe

The app windows and openconnect come with the installers and are not part of these updates.

Next to them, `core-manifest.json`:

    {"commit": "<40 hex>", "handover": false,
     "files": {"uni-vpn-core-linux-amd64.zip": {"sha256": "<64 hex>", "size": 123}, ...}}

The binary carries its commit:
`go build -ldflags "-X github.com/DavidVinu/uni-vpn/internal/daemon.Commit=<sha>" ./cmd/uni-vpn`.
`uni-vpn-core --version` then prints `uni-vpn <version> <commit>` (just `uni-vpn <version>`
without one), and the status reports it as `commit`. From main they are uploaded to the release "latest" next to the
installers, after the commit was promoted to "stable":
`https://github.com/DavidVinu/uni-vpn/releases/latest/download/core-manifest.json`.

`handover` is true when the tracked file `packaging/handover` exists. It is the switch.

## The Python updater

Unchanged while `handover` is false. When it finds a new stable commit and the manifest names the
same commit, says `handover: true` and has a zip for this platform, it downloads that zip as well,
checks size and SHA-256, runs `<staged>/bin/uni-vpn-core --version` (must print the commit), and
installs it together with the source files. The restart that follows starts the Go core through
the hand-over.

## The Go updater

`internal/updater`, run by the daemon (`internal/daemon/update.go`). Same schedule and the same
rules as the Python one (first check after 10 minutes, then every 5 hours plus up to 30 minutes,
switching `auto_update` on checks right away, applied only when idle, `auto_update` in
config.toml, `update_pending` in status). It reads the manifest, compares the commit with its
own, downloads the zip for its platform (size limit, then size and SHA-256 from the manifest),
unpacks it into `.uni-vpn-core-update-*` next to the install root (paths outside the root,
absolute paths, `..` and symlinks are refused), runs `<staged>/bin/uni-vpn-core --version`
(must print the manifest's commit) and replaces the files one by one. On Windows a file that is
in use (the running .exe) is renamed to `<name>.old` first (`<name>.1.old` and so on while an
older `.old` is still running) and removed on the next start.

The install root is the folder above `bin/` holding the running `uni-vpn-core`. The updater
records the files it installed in `<root>/.core-files` and the commit in `<root>/.commit`. It
only ever deletes files listed there, so the Python tree stays for the hand-over chain. A build
without a commit (a developer's `go build`), a binary outside `bin/` of an installation and a
root with `.git` never update themselves.

`uni-vpn-core update` checks right away, installs what it finds and restarts the service
(`--dry-run` only says what it would do).

Restart after an update: `syscall.Exec` of the new binary with the same arguments on Linux and
macOS; on Windows exit 75 when `UNI_VPN_SUPERVISED` is set, otherwise stay as a supervisor that
starts the new binary until it exits with something other than 75.

## Order

1. Ship hand-over code in Python, Go updater, Go setup, and the release files with
   `handover: false`. Users stay on Python and pick up the hand-over code.
2. CI tests the switch end to end on Linux, macOS and Windows runners: a Python install, the
   Python updater against the new manifest, then the status page answered by the Go core.
3. Add `packaging/handover`. Users move to Go with their next automatic update.
4. Installers ship the Go core without Python.
