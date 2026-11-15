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
