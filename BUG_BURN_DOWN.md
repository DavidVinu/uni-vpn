# Bug burn-down

Each area keeps its own section. Severity: Critical, High, Medium, Low. Status: open, fixed,
left (with the reason). Only what an audit verified is listed; a missing entry is not a claim
that there is no bug.

## Go core

Scope: the Go core (cmd/uni-vpn, cmd/uni-vpn-ios, internal/*): VPN and tunnel, local API,
device sync and its relay, self-update, PAC and system proxy, setup, configuration and
credentials, the university list. Not here: internal/service (desktop thread), the native app
windows, installers, phone apps, website and CI.

| # | Bug | Severity | Status |
|---|---|---|---|
| GC-1 | Another account's request gets through the account check if it resets the connection first | High | fixed |
| GC-2 | Clients the API refuses may still make it read a 32 MiB body | Medium | fixed |
| GC-3 | Linux app entry does not start when its path has `"`, `` ` ``, `$` or `\` | Low | fixed |
| GC-4 | Windows Start Menu shortcut is not created for a path with a typographic apostrophe (D’Angelo) | Low | fixed |
| GC-5 | Linux Repair and restart take another account's panel icon for this account's | Low | fixed |
| GC-6 | Linux does not stop the panel icon when its path has `+`, `(` or `[` | Low | fixed |
| GC-7 | Any web page can restart the app window once per service start | Low | left: only once per start, only after an update installed the window; needs a marker only the shells send |
| GC-8 | An update that stops halfway leaves the new core next to old files for good | Medium | fixed |
| GC-9 | Updated files are not on disk before they are renamed into place (empty core after a power cut) | Low | fixed |
| GC-10 | Windows: the running core is missing if the update dies between its two renames | Low | left: a window of microseconds; Windows has no atomic swap for a running program |
| GC-11 | `.core-files` and `.commit` are written without fsync | Low | left: a crash only skips the clean-up of old files once; the core keeps working |
| GC-12 | `uni-vpn update` in a terminal and the service can install at the same time | Low | left: the install fails and is retried 5 hours later |
| GC-13 | Linux: TOTP secrets of 26, 52 or 103 characters never work (openconnect before 9.21 refuses them) | High | fixed |
| GC-14 | A config.toml that is not UTF-8 stops the core for good: no window, no setup, no update | Medium | fixed |
| GC-15 | A change made after taking over another device's value is undone when that device's clock is ahead | Medium | fixed |
| GC-16 | A join from a request code keeps writing after the core stopped | Medium | fixed |
| GC-17 | Two codes asked for at once (double click) leave the first one open, and Cancel cannot close it | Medium | fixed |
| GC-18 | Stopping the core waits up to 30 s behind a sync action from the app, which then writes after the stop | Medium | fixed |
| GC-19 | Stopping the core waits behind the background round after a join | Medium | fixed |
| GC-20 | A domains.txt that is not UTF-8 makes the status page answer 500 | Low | left: only by editing the file by hand in another encoding; the app itself writes UTF-8 |
| GC-21 | A stop waits for a slow keyring read (up to 20 s) before it ends the tunnel | Low | left: only with a locked or hanging keyring; the VPN ends with the stop anyway |
| GC-22 | Two scripted POST /api/repair at once can start two repairs | Low | left: the page disables the button; needs scripted requests |
| GC-23 | A password typed during a synced account switch is stored for the new account | Low | left: a window of one keyring write |
| GC-24 | A relay that serves an older group (compromised, or restored by the operator) brings older values back | Low | left: needs control of the relay; a fix needs a signed sequence inside the ciphertext (design change) |
| GC-25 | Entering a code can use it up on a failure (offline, not set up yet) | Low | left: a new code works |
| GC-26 | Small sync edges: Add a device offline turns syncing on; removing an unknown id moves the group; a device that switched groups stays listed in the old one; an answer exactly at cancel or expiry is dropped | Low | left: cosmetic or recovered by the next round or a new code |
| GC-27 | macOS keychain: a password with a tab, or one that looks like hex, reads back wrong | Low | left: how `security -w` prints; very unlikely for real passwords |
| GC-28 | Hidden tuning key `tick = 0` in config.toml makes the core spin | Low | left: undocumented key, nobody sets it |
| GC-29 | Removing sync keys counts a keyring time-out as done; they come back at the next start | Low | left: only with a locked keyring |
| GC-30 | A connection that breaks during sign-in is shown as a refused password, with no retry | Medium | fixed |
| GC-31 | Another account can take the proxy port during sign-in and be counted as the VPN | Medium | fixed |
| GC-32 | macOS: openconnect keeps the VPN session up after the service was killed | Medium | fixed |
| GC-33 | macOS uninstall loses a network service's own switched-off rule address | Low | fixed |
| GC-34 | Small SOCKS and DNS edges: a truncated DNS answer is not retried over TCP; a cancel can wait for the full DNS time-out; an unknown address type waits for 2 more bytes; a half-closed connection waits for the remote end; the "no acceptable method" reply can be lost on Windows | Low | left: each only delays or fails one request that the browser retries |
| GC-35 | Small tunnel edges: an empty `openconnect --help` (fork failure) adds `--no-external-auth` once; pipes leak if a second pipe cannot be made; a server banner with a marker word could be misread | Low | left: rare, and the next start corrects it |
| GC-36 | Proxy backup files are written without an atomic rename | Low | left: only a crash during the write; Restore falls back to the defaults |
| GC-37 | A UTF-8 BOM in domains.txt makes its first line invalid | Low | left: hand-edited files only |

### GC-1 Another account's request gets through the account check if it resets the connection first

- Severity: High (needs a second account on the same computer; it can then send requests such as
  /api/setup or /api/sync/ to this account's uni-vpn without seeing the answer).
- Root cause: `sockowner` finds the client's end of the connection in the system's TCP table and
  fails open when it cannot. A client that sends its request and resets the connection
  (`SO_LINGER` 0) before the lookup takes both ends out of the table, so the lookup returned
  "this connection is not ours in the system's table" and the request went through, while its
  bytes were still there to read. The existing rule (a client that closes first counts as
  another account's) only covered a normal close, where the server's end stays listed.
- Fix: when the lookup fails, `OtherAccount` asks the socket itself (`SO_ERROR`, Linux, macOS and
  Windows); a connection that already failed counts as another account's. Nobody could read the
  answer anyway. Every other failure still fails open as designed.
- Verification: `TestResetByAnotherAccountIsRefused` (a real other account, `nobody`, sends
  `POST /api/setup` and resets) and `TestResetClientCountsAsAnotherAccount` failed before the fix
  ("this connection is not ours in the system's table", request still readable) and pass after;
  `go test -race ./internal/sockowner/ ./internal/httpapi/` ok; `go vet` for linux, darwin,
  windows, android ok.

### GC-2 Clients the API refuses may still make it read a 32 MiB body

- Severity: Medium (memory use; any refused client could open many such connections).
- Root cause: `handle()` granted the 32 MiB screenshot limit from the `X-Uni-VPN` header, method
  and path alone. The Host, token, account and Origin checks run after the body is read, so a
  DNS-rebinding page, another account or another phone app got the big limit too.
- Fix: the big limit also needs `getsThrough()` (the same Host, Origin, token and account checks);
  a refused request keeps the normal limit and gets 413 from its Content-Length alone.
- Verification: `TestPictureLimitOnlyForRequestsThatGetThrough` (5 refused cases waited for the
  body before, get 413 now); `go test -race ./internal/httpapi/ ./internal/daemon/` ok.

### GC-3 Linux app entry does not start when its path has `"`, `` ` ``, `$` or `\`

- Root cause: `Exec=` is a desktop-entry string, whose `\\` escapes come off before the quoting
  rule; `DesktopQuote` wrote one backslash per quoting escape, which GLib refuses ("Desktop file
  didn't specify Exec field").
- Fix: each backslash the quoting needs is written as `\\`, as the specification says.
- Verification: `TestDesktopEntryQuotesAndNamesTheWindowClass` updated; `gio launch` on an entry
  from the new output runs with the exact arguments.

### GC-4 Windows Start Menu shortcut is not created for a path with a typographic apostrophe

- Root cause: PowerShell also ends a single-quoted string at ‘ ’ ‚ ‛; `PSQuote` doubled only `'`.
- Fix: all five are doubled.
- Verification: `TestPSQuoteDoublesEverySingleQuote` failed before, passes after (proven by the
  PowerShell grammar; no PowerShell here).

### GC-5 Linux Repair and restart take another account's panel icon for this account's

- Root cause: `desktop.Running` matched every process with the script path in `/proc/*/cmdline`,
  which every account can read. With a packaged install (same `/usr/...` path) another signed-in
  account's panel icon made Repair skip starting this account's, and a restart wait 5 s for a
  process it cannot stop.
- Fix: `Running` keeps only processes whose real uid (`/proc/<pid>/status`) is this account's.
- Verification: `TestRunningSkipsAnotherAccountsProcess` (a `nobody` process with the script path)
  failed before, passes after; `go test -race ./internal/desktop/` ok.

### GC-6 Linux does not stop the panel icon when its path has `+`, `(` or `[`

- Root cause: `pkill -f` takes a regular expression; the script path was passed as is.
- Fix: the path is passed through `regexp.QuoteMeta`.
- Verification: with real `pkill`, a process at `/tmp/a+b (c)/linux/uni-vpn-app.py` was not
  stopped with the raw path (exit 1) and was with the quoted one (exit 0); desktop tests updated.

### GC-8 An update that stops halfway leaves the new core next to old files for good

- Severity: Medium (Windows ends the update at shutdown, a power cut, a full disk: the user keeps
  old openconnect files, or on macOS a half-replaced app window whose signature no longer fits).
- Root cause: `install` renamed the files in zip order, so the core could be replaced first. After
  a stop halfway the next start ran the new core, whose update check compares only its own
  commit with the manifest: it found itself current and never installed the rest.
- Fix: the core program is renamed into place last. A stop before that leaves the old core, whose
  next check installs the rest (files already in place are skipped).
- Verification: `TestAnInstallThatStopsHalfwayIsCompletedLater` (second rename fails, then a
  restart) failed with `core "new core" with openconnect "old openconnect"` before, passes after;
  `go test -race ./internal/updater/` ok; `go vet` for linux, darwin, windows ok.

### GC-9 Updated files are not on disk before they are renamed into place

- Root cause: APFS, NTFS and XFS do not order a rename after the file's data; `unpack` wrote the
  new core without fsync, so a power cut right after an automatic update could leave an empty
  `bin/uni-vpn-core`, which the start script does not replace.
- Fix: `unpack` syncs each file before closing it (during staging, not during the install).
- Verification: `strace` showed write then `renameat` with no `fsync` before, `fsync` first after;
  `TestUnpackedFilesAreOnDiskBeforeTheInstall` failed before, passes after.

### GC-13 Linux: TOTP secrets of 26, 52 or 103 characters never work

- Severity: High (every Linux user whose university gives a 128-bit secret, or a SHA256 one,
  which is usually 256 bits: sign-in fails at every try, and typing `=` does not help because
  the secret is normalized).
- Root cause: tokens are stored as `base32:` plus the secret without padding. openconnect before
  9.21 (Debian 12: 9.01, Ubuntu 24.04: 9.12; the Linux packages use the distribution's) only
  decodes base32 whose length is a multiple of 8 (`decode_base32` in `oath.c`); Windows, macOS
  and the phones bring 9.21, which also takes it unpadded.
- Fix: `totp.ForOpenconnect` pads the base32 part to whole groups of eight, and the tunnel writes
  the token file with it. Stored tokens are unchanged, so existing installs and synced devices
  are covered without a migration.
- Verification: real openconnect 9.12 from Ubuntu 24.04: `base32:<26 chars>` gives "Invalid
  base32 token string", the padded token and `sha256:base32:<52 chars>====` sign in with the
  same code as `CodeAt`. `TestTokenFileIsPaddedForOlderOpenconnect` failed before, passes after;
  `TestForOpenconnectPadsTheBase32Part` checks the codes stay the same.

### GC-14 A config.toml that is not UTF-8 stops the core for good

- Severity: Medium (an install without any click-only way out, but only after the file was edited
  by hand in another encoding or damaged).
- Root cause: `readText` returned a plain error for invalid UTF-8 instead of a `*ConfigError`, so
  `coremain` exited with code 1 (restarted every 5 s, never a window) and setup refused too.
- Fix: invalid UTF-8 is a `*ConfigError` like any other problem in the file: the core starts, the
  window says the settings could not be read and offers setup, which moves the file aside.
- Verification: `TestFileThatIsNotUTF8IsAConfigError` failed before, passes after; a built core
  with a Latin-1 config.toml exited with 1 before and answers `settings_broken` / `setup` after.

### GC-15 A change made after taking over another device's value is undone when that device's clock is ahead

- Severity: Medium (lost updates: a removed website comes back, a new password is replaced by the
  old one on every device).
- Root cause: a local change competed only by its own clock. The "comes after" rule applied only
  to this device's own earlier entry, not to another device's entry this device already had in
  step (sync.json has its time and device), so a later change lost to a faster clock.
- Fix: the rule also applies when sync.json's time and device for the key equal the group's
  entry. Edits made at the same time on two devices (the group changed since the last round)
  still go by time.
- Verification: `TestChangeAfterTakingOverAValueFromAClockAheadStays` failed before (language,
  website and password all reverted), passes after; `TestRandomEditsOnClocksThatDisagree`
  (clocks +2 h, 0, -30 min) failed 36 of 40 seeds before, 0 after.

### GC-16 A join from a request code keeps writing after the core stopped

- Root cause: the request side's join ran on `context.WithoutCancel` and was not counted in the
  engine's background group, so a stop neither ended nor waited for it (pass 43 fixed only the
  offering side).
- Fix: it runs on the engine's background context and is counted, like the offering side.
- Verification: `TestJoinFromARequestCodeEndsWithTheCore` failed before ("sync.json was written
  after Run returned"), passes after.

### GC-17 Two codes asked for at once leave the first one open

- Root cause: `show()` cancelled the old code in one lock and put the new one on screen in a
  second; a code shown in between was overwritten without being closed, its waiter ran for ten
  more minutes and, for an offer, the group key stayed on the relay.
- Fix: the old code is swapped for the new one under one lock, then closed outside it.
- Verification: `TestCodesShownAtOnceAreAllWithdrawn` (8 requests at once, then cancel) failed 3
  of 3 before, passes after.

### GC-18 Stopping the core waits behind a sync action from the app

- Root cause: `/api/sync/*` ran with `context.Background()` while holding the engine's round lock,
  so a stop waited up to the 30 s relay time-out and a join wrote after it (on the iPhone the
  next core may already run then).
- Fix: `Daemon.Sync` uses a context that ends when the core stops.
- Verification: `TestJoinFromTheAppEndsWithTheCore` failed before ("the phone did not stop"),
  passes after.

### GC-19 Stopping the core waits behind the background round after a join

- Root cause: the background context was cancelled only in Run's own stop case, which Run could
  not reach while its round waited for the background round's lock.
- Fix: the stop watcher also cancels the background context at once.
- Verification: `TestStopDoesNotWaitBehindTheRoundAfterAJoin` took 30 s and failed before, passes
  at once after.

### GC-30 A connection that breaks during sign-in is shown as a refused password

- Severity: Medium (a Wi-Fi switch or a gateway hiccup during sign-in stops the VPN for good and
  tells the user to check the password, which was never checked).
- Root cause: openconnect prints "Error reading HTTP response" when the connection breaks while it
  waits for the gateway, then "Failed to complete authentication"; the classifier mapped the
  second line to a refused password whatever came before.
- Fix: the classifier remembers a failed request until the next request line; "Failed to complete
  authentication" right after one is a connect failure, retried with the usual back-off.
- Verification: a new case in `TestGatewayTroubleIsNotASignInFailure` gave `auth_failed` before
  and `error` / connect failed after; `TestARetriedRequestStillReportsTheSignIn` keeps a retried
  request followed by "Login failed." a refused password.

### GC-31 Another account can take the proxy port during sign-in

- Severity: Medium (needs a second account that races for the port: it then gets the browsers'
  connections, the core shows Connected before the sign-in, and the TOTP file is removed early).
- Root cause: `FreePort` picks a port and closes it; ocproxy binds it only after the sign-in, and
  the port is in openconnect's command line, which every account can read. `WaitReady` counted
  whoever listened there first as the tunnel.
- Fix: `WaitReady` counts the port only when the end that accepted its test connection belongs to
  this account (`sockowner`, a few 10 ms retries because Linux names the owner once the listener
  accepted). When the system cannot tell, it counts as before.
- Verification: `TestWaitReadyIgnoresAnotherAccountsListener` and, as root,
  `TestWaitReadyIgnoresARealOtherAccountsListener` (listener of `nobody`) failed before ("counted
  another account's listener as the tunnel"), pass after.

### GC-32 macOS: openconnect keeps the VPN session up after the service was killed

- Root cause: openconnect was started in a session of its own (`Setsid`). When the service dies
  (crash, SIGKILL, launchd's exit time-out), launchd ends only the job's process group, so
  openconnect and ocproxy stayed, holding a VPN session beside the one of the next start. Linux
  (systemd ends the cgroup) and Windows (job object) already clean up.
- Fix: no `Setsid` on macOS; openconnect stays in the service's process group.
- Verification: `TestOpenconnectEndsWithAKilledService` (macOS only, runs in CI): a killed
  "service" in a group of its own, then launchd's `kill(-pgid)`; red and green shown on Linux with
  the macOS branch forced on (5.3 s and still alive before, ended in 0.36 s after).

### GC-33 macOS uninstall loses a network service's own switched-off rule address

- Root cause: Restore only switched the rule off for a service whose own address was off, leaving
  uni-vpn's address in the field.
- Fix: Restore writes the service's own address back, then switches it off.
- Verification: `TestMacUninstallKeepsAnAddressThatWasOff` failed before, passes after.

