# Bug burn-down

Each area keeps its own section. Severity: Critical, High, Medium, Low. Status: open, fixed,
left (with the reason). Only what an audit verified is listed; a missing entry is not a claim
that there is no bug.

## Website and CI

Scope: the download site (site/, live at https://vpn.davidvinu.de), the CI workflows, the release
and the "stable" branch, the build scripts (packaging/, get.sh, install.sh), the translations
(uni_vpn/locales).

| # | Bug | Severity | Status |
|---|---|---|---|
| WC-1 | Updates never replace the helper files the installers put next to the core | Medium | fixed |
| WC-2 | Imprint and privacy page show "[email protected]" instead of the email address without JavaScript | Medium | fixed |
| WC-3 | Privacy page leaves out the Android app the site offers | Medium | fixed |
| WC-4 | Download page shows no download at all without JavaScript | Low | fixed |
| WC-5 | README's install table has no Android download | Low | fixed |
| WC-6 | iPad visitors read "Not on iPhone yet" | Low | fixed |
| WC-7 | French tips break a line inside « », "Processeur ARM" capitalised after a comma (also Spanish) | Low | fixed |
| WC-8 | FreeBSD and other non-Linux X11 systems are offered the Linux packages | Low | fixed |
| WC-9 | CI's shell syntax check skips the Android, iPhone, Windows and test scripts | Low | fixed |
| WC-10 | Actions still target Node.js 20 (GitHub runs them on Node.js 24 with a warning) | Low | left: they work on Node.js 24; upgrading every action is a separate change |
| WC-11 | The sideloaded Android APK never updates itself | Medium | handed to the mobile burn-down (app side and publish-android job) |
| WC-12 | French HKU steps name a "Changer" button, Microsoft's French page says "Modifier" | Low | fixed |
| WC-13 | Wording slips in French, German and Spanish messages | Low | fixed |
| WC-14 | Chinese texts mix terms for the same thing (for example two words for "sign out") | Low | left: each term is a correct, common one; unifying them is style |
| WC-15 | A failed release upload deletes the download (gh's --clobber deletes first) | Medium | fixed |
| WC-16 | README's helper commands and two button names are from the Python version | Low | fixed |
| WC-17 | Download page gives Linux users no hint to open the app after installing | Low | fixed |
| WC-18 | Download page does not say the Mac package needs macOS 14 | Low | fixed |
| WC-19 | 32-bit ARM Linux is offered the 64-bit ARM packages | Low | partly: no button for armv7l any more; the list's ARM files are 64-bit only, and a browser that reports aarch64 on a 32-bit system still gets them (rare) |
| WC-20 | An older run's queued publish can replace a newer one's, so the newest green commit waits for the next push | Low | left: GitHub's concurrency queue keeps one waiting job; nothing is lost, the next green push publishes |
| WC-21 | CI removes the Linux package only after uninstalling, so the "removed in the app store" path runs only in unit tests | Low | left: covered by removal_test.go; an end-to-end run waits two 5-minute checks |
| WC-22 | Windows in Chinese (Hong Kong, Macau) probably gets the installer's few texts in Simplified Chinese | Low | left: unverified, the installer shows almost no text and the app itself picks Traditional |
| WC-23 | Privacy page says the Android app asks GitHub "at most once a day"; after a failed check it asks again 10 minutes later | Medium | fixed |
| WC-24 | Privacy page points to "number 4" for syncing, which is number 5 since the Android item | Low | fixed |
| WC-25 | One GitHub error answer makes release-put.sh fail instead of trying again | Low | fixed |
| WC-26 | README says the core is not on the PATH, but setup creates a `uni-vpn` command | Low | fixed |
| WC-27 | Download page does not say the Android app needs Android 10 | Low | fixed |
| WC-28 | CI test TestOnlyTOTPFieldWritesATokenFile failed once ("duo_push not ready") on go (macos-latest) | Low | left: not reproduced (80 runs with -race, half under full CPU load); the test now reports how the fake ended |

### WC-1 Updates never replace the helper files the installers put next to the core

- Root cause: packaging/build-core.sh staged each update zip with the core, its licences and the
  app windows only, while the installers (packaging/stage-app.sh --go) also ship
  bin/uni-vpn-ocproxy, bin/uni-vpn-vpnc.js, app/icon.svg, LICENSE and README.md. The updater only
  replaces files a zip carries, so a change to openconnect's helper script (macOS, Linux) or the
  Windows adapter script would reach new installs only, never the installed copies. Seen in the
  live release: uni-vpn-core-linux-amd64.zip held bin/uni-vpn-core and app/linux/uni-vpn-app.py,
  the .deb also the two helpers and app/icon.svg.
- Fix: stage-app.sh --support is the one list of what goes next to the core (helpers, the app
  windows' sources and icons, LICENSE, README.md; no longer the phone apps' sources, which no
  computer uses). The installers and build-core.sh both use it. packaging/check-core-zips.sh
  checks every zip against that list and the manifest; the core-files job runs it.
- Verification: check-core-zips.sh failed on zips built by the old script (13 missing files per
  zip) and passes on the new ones; the real updater applied the new Linux zip in a throwaway test
  (helpers replaced, executable); installer staging differs only by the phone sources. The
  released uni-vpn-core-linux-amd64.zip of 3c8cb5e has both helpers, app/icon.svg, LICENSE and
  README.md.

### WC-2 Contact email hidden on the legal pages

- Root cause: Cloudflare's email obfuscation (in front of vpn.davidvinu.de) rewrote every address
  on impressum.html and privacy.html into "[email protected]" plus a decoding script. Without
  JavaScript the imprint had no email address, which § 5 DDG requires.
- Fix: both pages wrap their text in Cloudflare's email_off markers.
- Verification: curl of the live pages showed the rewritten addresses before (2 on the imprint, 4
  on the privacy page); 6 minutes after the merge (#75) none, and both pages show
  info@davidvinu.de in their HTML.

### WC-3 Privacy page leaves out the Android app

- Root cause: the policy (19 October) predates the public APK (8 November) and spoke of
  computers only: password stores of Windows, macOS and Linux, uninstalling, updates from GitHub.
- Fix: both languages say what the Android app stores and where (encrypted file, key in the
  Android Keystore, not in backups or phone transfers), that uninstalling deletes it, that its
  VPN only hands the listed websites to the app, and that the GitHub update check is the
  computers'. Date updated. Claims checked against AndroidManifest.xml, data_extraction_rules.xml,
  Core.kt and internal/credentials.
- Verification: the page renders; text compared line by line in both languages.

### WC-4 to WC-9

- WC-4: a noscript list of every file (the list's first row keeps no top border with and
  without JavaScript, first-of-type), and the empty button area is hidden. Checked in headless
  Chromium with JavaScript on and off.
- WC-5: README row for uni-vpn.apk and the Android install hint.
- WC-6: the phone text names iPhone and iPad in all six languages.
- WC-7: non-breaking spaces inside the French « » and before the colon; "processeur ARM",
  "procesador ARM". Checked in a 375 px French screenshot.
- WC-8: only Linux and ChromeOS (not any X11) get the Linux buttons; FreeBSD now sees the full
  list. Checked with a FreeBSD user agent next to Windows, Mac, iPad, iPhone, Android, Linux,
  Fedora, Linux on ARM and ChromeOS.
- WC-9: the check runs over every tracked .sh file plus the shebang-only scripts, each with the
  shell its first line names.

### WC-12 and WC-13 Translations

- Root cause: the translations were written per key without a second read against the English and
  the pages they describe. A review of all six languages against en.json (meaning, placeholders,
  names of buttons on other sites) found these.
- WC-12: the HKU sign-in page is Microsoft's, which shows its French button as "Modifier". The four
  HKU keys (computer and phone, staff and students) now say « Modifier ».
- WC-13, French: msg.server_untrusted ("n’a pas passé un contrôle" is a calque, now "a échoué à
  un contrôle"), phone.msg.not_running ("ne fonctionne pas" read as broken, now "n’est pas
  lancé"), and 23 keys used a straight apostrophe next to keys with ’ (now ’ everywhere).
  German: phone.vpn_refused and phone.vpn_not_allowed said the phone "did not switch on" Uni VPN,
  but the phone refused to let it (now "nicht einschalten lassen"). Spanish:
  phone.msg.hostscan_required said the university connects ("antes de conectarse", now "antes de
  que se conecte"), totp.qr_not_key now says "códigos de acceso", the
  app's own name for them ("Código de acceso").
- The phone apps carry their own copy of phone.msg.port_in_use (shown when the core cannot
  start): Android's port_in_use and iOS's Strings.portTaken now have the ’ too. The iOS CI test
  caught the mismatch, but only on a Mac and not for Android, so internal/i18n's
  TestPhoneAppsCarryTheCoresPortText now compares both copies with the catalog in every language
  (failed before the copies were updated, passes after).
- Verification: every file still loads, keeps its keys, has the placeholders of en.json, and keeps
  its formatting (json.dumps round trip byte for byte); the Python test suite (592 tests) passes.

### WC-15 A failed release upload deletes the download

- Found by: the re-audit after #75.
- Root cause: the publish job replaced each file of release "latest" with `gh release upload
  --clobber`, which deletes the old file before it uploads the new one (gh's own help: "If the
  upload fails, the original assets will be lost"). While a file uploads its
  releases/latest/download/ link answers 404, and if the upload fails (a GitHub 5xx or timeout)
  the step stops and the file stays gone until the next green push: the download page's button
  for that system, or the update zip or manifest, is missing meanwhile.
- Fix: packaging/release-put.sh uploads every file under a temporary name first (retried), then
  in the given order deletes the old file and renames the new one (GitHub's "update a release
  asset"), so a link is missing only between those two calls and a failed upload leaves the old
  file. The publish job uses it in the order it had (zips, manifest, installers), and
  publish-android for the APK and then its uni-vpn-apk.json.
- Verification: with a stand-in for gh that applies --clobber like gh, the old step left the
  release without uni-vpn.pkg after one failed upload. tests/test_release_put.py runs the script
  against the same stand-in: all uploads before any delete, each file missing only between its
  delete and rename, swaps in the given order, a failed upload retried, uploads that keep failing
  leave every old file, a lost answer to a delete, a file left over from a failed run. Live:
  the first publish with it (#79, 3c8cb5e) left all 16 files on release "latest" and no
  "next-" file; core-manifest.json names 3c8cb5e and matches uni-vpn-core-linux-amd64.zip, and
  uni-vpn-apk.json gives the size of the uploaded uni-vpn.apk.

### WC-16 to WC-19

- WC-16: README's "Command line" listed the Python version's `uni-vpn status`, `doctor`, `log`,
  `install.sh --repair` and `install.ps1 -Uninstall` (the installers ship none of them), and the
  table of messages named an "Update second factor" button and a "second factor is not set up"
  message that do not exist. It now lists what `uni-vpn-core` has (`--version`, `app`, `update`,
  `setup --no-browser --repair` as the Repair button runs it, `uninstall`), where it is, and
  where the log is; "Update sign-in code" and "No setup key saved yet" as en.json has them.
  Checked every quoted text of that table against en.json and every command against
  cmd/uni-vpn.
- WC-17: the .deb and .rpm have no setup step of their own; nothing happens until "Uni VPN" is
  opened once. Linux visitors now get the tip "When the installation is done, open Uni VPN from
  your apps" in all six languages.
- WC-18: distribution.xml allows macOS 14 and newer, and Installer only says it cannot install.
  The Mac button now has "macOS 14 or newer" under it (browsers do not tell the macOS version).
- WC-19: "armv7l" matched /arm/ and got the arm64 packages, which do not install there. 32-bit
  ARM now sees the full list, like an unknown system.
- Verification for WC-17 to WC-19: headless Chromium with the user agents of WC-8 plus Linux
  armv7l, in English, German and Traditional Chinese; screenshots at 375 px with the tip closed
  and open (French Mac, German Linux); no horizontal scroll.

### WC-23 to WC-27

- Found by: the final re-audit after #79.
- WC-23: privacy.html (both languages) said "at most once a day". ApkUpdate.check sets the next
  check 10 minutes ahead before each request and keeps it there when the request fails (no
  network, 404, unreadable JSON), and a download that does not match its JSON is tried again
  after 10, 20, 40 minutes up to a day. The page now says once a day, and after a failed check
  every 10 minutes until it works (the re-audit of #80: "again after 10 minutes" read as one
  more try); docs/android.md said "at most once a day" too and now says the same. Checked
  against ApkUpdate.kt (RETRY = 10 minutes, DAY).
- WC-24: "(unten, Nummer 4)" / "(below, number 4)" now say 5; headless Chromium numbers the list
  the same way.
- WC-25: on an HTTP error `gh api` prints GitHub's error body to stdout, and retry() kept the
  output of every try, so the release id became "{...error...}7" and every later call failed.
  retry() now prints only the successful try's output. tests/test_release_put.py's stand-in
  for gh prints such a body now; the new test failed with the old script and passes.
- WC-26: setup always writes a `uni-vpn` wrapper (internal/setup CommandPath, Wrapper). README
  names it, where it goes, and the core's full path for when it is not on the PATH.
- WC-27: minSdk is 29; older phones only hear "App not installed". The Android button now has
  "Android 10 or newer" under it, in all six languages (checked in headless Chromium).

### WC-28 A tunnel test failed once on macOS

- Found by: CI of #80's first commit (go (macos-latest), `go test -race ./...`), a change with
  no Go code; the same test passed on Linux and Windows in that run.
- What is known: the test took 0.52 s, so WaitReady returned because the fake openconnect had
  exited (its 3 s limit was not reached), about the time the fake listens on its port. The
  failure printed nothing about why.
- Reproduction: 40 runs with -race on Linux, and 40 more with every CPU busy, all passed.
- Change: the test now prints the exit code and the fake's last output lines when it is not
  ready, so a next failure shows the cause (checked by forcing the fake to fail once).
- Left as Low: no user-facing path is known to fail; a release only waits for a rerun.

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
| GC-38 | The ports are not read from a config.toml that is not UTF-8, so the window cannot reach the core with custom ports (found by the re-audit, rest of GC-14) | Low | fixed |
| GC-39 | Phones: the ready file outlives the status port at a stop, so the app could send its token to another app on that port | Low | fixed |
| GC-40 | Windows: `taskkill /IM "Uni VPN.exe"` after an update, setup or uninstall also closes other signed-in accounts' tray apps | Low | left: multi-account Windows (see the per-user task name item); a session filter cannot be tested here |
| GC-41 | iPhone in-process restart: an enrollment started before a stop can write its result after it; a late log line reopens the log file | Low | left: only the iPhone's in-process restart, a tiny leak |
| GC-42 | Uninstall keeps the password and TOTP secret when config.toml is broken (the user name comes from it) | Low | left: terminal-only uninstall of a broken install |
| GC-43 | macOS proxy edges: a dead rule address comes back if the proxy backup was deleted; a covered service keeps our address in its switched-off field | Low | left: browsers go direct when the rule cannot be loaded |
| GC-44 | A gateway that breaks the connection after every password would now be retried with back-off, each try a new Duo push (rest of GC-30) | Low | left: no such gateway known (ASA and ocserv answer "Login failed" or 401) |
| GC-45 | Sync edges seen by the re-audit: a code shown in the moment before a stop polls the relay after it (writes nothing); busy rounds logged as failures; a device linked again right after removal is listed only once its clock passes the removal; a retried Remove leaves an unused group on the relay | Low | left: no wrong value anywhere; each recovers by itself |

### Final re-audit (2026-11-15)

After the fixes above, four fresh reviewers re-audited the whole Go core on this branch (core and
local API; tunnel and proxy; sync and relay; update, setup and data). Each first reviewed every
fix in its area for regressions (several by removing the fix and watching its test fail again),
then read its area anew. Result: no Critical, High or Medium bug found; two Lows fixed (GC-38,
GC-39), the rest recorded above as left. Evidence beyond the unit tests: Ubuntu 24.04's
openconnect 9.12 for GC-13, a 3-device property test with clocks +2 h, 0 and -30 min and a lossy
relay for GC-15 (200 seeds pass, 14 failures with the old rule), and the devsync suite against the
real relay worker. Windows and macOS code was checked by reading, cross-compiling and CI only.

Full suite on the final branch: `go test -race ./...` (35 packages), `go vet` for linux, darwin,
windows and android, the contract tests against the Go core, `node --test relay/worker.test.mjs`
and the devsync tests against `relay/serve.mjs`, `go test -tags iosapi ./cmd/uni-vpn-ios/`; plus
every CI job on the pull request.

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

### GC-38 The ports are not read from a config.toml that is not UTF-8

- Root cause: `PortsFromBroken` read the file with `readText`, which refuses invalid UTF-8, so the
  core that GC-14 now starts with such a file fell back to 1080/1081 while the window and the
  openers used the configured port.
- Fix: it reads the raw bytes and replaces invalid sequences (the port lines are ASCII).
- Verification: a Latin-1 case in `TestPortsFromBroken` returned no ports before; a built core with
  `http_port = 21081` answered on 1081 before and on 21081 (`settings_broken`) after.

### GC-39 Phones: the ready file outlives the status port at a stop

- Root cause: Run's stop closed both ports, waited up to 5 s for requests and 5 s for the loop, and
  removed the ready file only at the end (Unbind too, after unbinding); the iPhone app sends its
  token wherever the file says the core is ready.
- Fix: `dropReady()` runs before the ports are let go, in Run's stop and first in Unbind.
- Verification: `TestReadyFileGoesBeforeTheStatusPort` (another app takes the port the moment it
  is free) failed before, passes after (`-race -count=20`).

## Mobile apps

Scope: `app/android`, `app/ios`, `packaging/android`, `packaging/ios`, `cmd/uni-vpn-ios` and
`internal/libtunnel`. `internal/netstack` and `internal/octunnel` belong to the Go core section.

### M1. The Android app from the download page never updates

- **Severity:** High
- **Status:** Fixed
- **Bug:** `uni-vpn.apk` from the release page is the only way to get the Android app (there is
  no Play listing yet), but nothing ever updates it: the core's updater is off on Android
  (`internal/updater` `Disabled()`: "the app store updates the Android app") and the app had no
  update check. Every phone stays on the version it was installed with, security fixes included.
- **Root cause:** the app was designed for a store that does not exist yet; the download build
  had no updater of its own.
- **Fix:** role model Signal's website build. Two flavors: `website` (the release APK, updates
  itself) and `play` (the future Play bundle, no self-update, no `REQUEST_INSTALL_PACKAGES`). CI
  writes `uni-vpn-apk.json` (version code read from the APK, size, SHA-256) and uploads it after
  the APK. `ApkUpdate` checks it at most once a day while the VPN is on or the app is open (not at
  all with Settings, Automatic updates off), downloads on Wi-Fi (on mobile data only on a tap),
  checks size, SHA-256, package and signing key, and Home plus the VPN's notification offer
  "Update Uni VPN"; a tap hands the file to `PackageInstaller`. A release file that is not this
  copy (a build signed with another key) is not offered or downloaded again until the next
  release; debug builds do not check. Copies installed before this have no update check and need
  the new APK once by hand.
- **Verification:** `ApkReleaseTest` (what CI writes is read, anything malformed refused, the
  digest) passes; lint clean; both flavors compile. `ApkUpdateTest` (the signing check accepts
  this app's own APK and refuses another package) runs on the CI emulator (Android 14). Android 10
  reads an archive's signatures only with `GET_SIGNATURES` (AOSP android-10.0.0_r1
  `getPackageArchiveInfo`; 11 also with `GET_SIGNING_CERTIFICATES`), so both flags are passed;
  without that, Android 10 would never be offered an update.

### M2. Android: when the core ends, the proxy rule stays up for up to a minute

- **Severity:** Medium
- **Status:** Fixed
- **Bug:** the VPN hands Android `http://127.0.0.1:1081/proxy.pac`, which only our own core may
  answer (`api-ready`, docs/android.md). When the core ended while the app lived (a crash,
  Android ending the child process, Repair), the service noticed only at its next 30 s round,
  and withdrew the rule only once a new core had failed with "ports taken" a round later. For up
  to 60 s another app could hold port 1081 and answer the browsers' rule requests, and the
  websites did not work until the next round restarted the core.
- **Root cause:** `UniVpnService` trusted the rule until it had proof that another app held the
  port, instead of only while its own core held it; and it looked only every 30 s.
- **Fix:** the service checks `Core.ready()` (our process alive and `api-ready` present) every
  second; when it goes false the VPN's rule is withdrawn at once, the core restarted, and the rule
  handed back once the new core holds both ports.
- **Verification:** traced the poll loop and the existing emulator test path (`PhoneTest`
  `c_theVpnCarriesTheProxyRule` still starts with the core ready); lint and unit tests pass.

### M3. Android: a Keystore that keeps failing leaves the app on a spinner forever

- **Severity:** Medium
- **Status:** Fixed
- **Bug:** `Core.secretsKey()` makes a new key only for `KeyPermanentlyInvalidatedException` and
  `AEADBadTagException`; every other failure is treated as "the Keystore is not ready yet" and
  thrown, so the core is never started. Where the key itself is broken (seen on some phones after
  system updates: `UnrecoverableKeyException`, "Keystore operation failed"), every start fails
  the same way forever: the app shows an endless spinner, the tile and notification say on, and
  only clearing the app's storage helps.
- **Root cause:** a lasting failure was handled like a passing one, and the screen had no state
  for "the core does not answer".
- **Fix:** after a minute of failing starts (about one a second, `LastingFailure`) the Keystore
  key is deleted and a new one made; the core then asks for the password again. After 20 s
  without an answer the app shows the core's "Uni VPN is not running" text (shipped in the app's
  resources, as no core texts exist then) instead of the spinner.
- **Verification:** `LastingFailureTest` (a minute of failures counts, failures far apart or
  after a success start afresh); `test_android_texts_without_the_core_are_the_cores` checks the
  app's texts against the core's catalogs in all six languages (and fails when one differs).

### M4. iPhone: removing a duplicate VPN configuration could remove the live one

- **Severity:** Low
- **Status:** Fixed
- **Bug:** `VPN.load()` removed every configuration that was not `manager`, a property, across
  `await`s. A removal posts a configuration change, whose own `load()` sets `manager` to its copy
  of the same live configuration; the first load then saw its copy as "not manager" and removed
  it, so the VPN vanished from Settings and iOS asked again.
- **Root cause:** comparing with shared state that a concurrent load changes.
- **Fix:** compare with this load's own choice (`keep`).
- **Verification:** traced; CI builds and tests the app in the simulator (no VPN configurations
  there, so not reproducible in CI).

### M5. iPhone: Home said "Connected" while the tunnel was still starting

- **Severity:** Low
- **Status:** Fixed
- **Bug:** turning the VPN on stops the app's own core for the handover, but Home kept showing
  that core's last status (often "Connected") for the up to 20 s the extension needs to start its
  core, and longer when the start failed.
- **Root cause:** the status of a stopped core was shown as current.
- **Fix:** while iOS reports the VPN as connecting, Home shows "Connecting" and no stale hint or
  fix button.
- **Verification:** traced; CI builds the app.

### M6. iPhone: a core that never answers leaves an endless spinner

- **Severity:** Low
- **Status:** Fixed
- **Bug and root cause:** as M3 on Android: `unreachable` was tracked but never shown.
- **Fix:** after 20 s without an answer the app shows "Uni VPN is not running" from
  `Strings.notRunning` (bundled, like `Strings.portTaken`).
- **Verification:** the simulator test compares `Strings.notRunning` with the core's
  `phone.msg.not_running` in all six languages.

### M7. Android: every Remove button on Websites read only "Remove"

- **Severity:** Low
- **Status:** Fixed
- **Fix:** the button's label names the website for screen readers.

### M8. Android: a release whose core does not start strands the phone

- **Severity:** Medium
- **Status:** Fixed
- **Bug:** found by the re-audit. When a release's core does not start on a phone, the next
  release is the way out, and `ApkUpdate` finds it, but nothing offered it: Home's "Update Uni
  VPN" needs the core's status, the app showed only "not running", and the service checked for
  updates only while its core was ready.
- **Root cause:** the update offer hung off the core's screens and loop, while it is what
  repairs a broken core.
- **Fix:** the "not running" page offers the update (text shipped in the app's resources) and
  the switch; the service checks every 30 s also while it waits for a core.
- **Verification:** traced the screen's branches and both service loops; lint clean, unit tests
  pass.

### M9. Android: leaving the app during a download left Update busy

- **Severity:** Medium
- **Status:** Fixed
- **Bug:** found by the re-audit. A tap on Update on mobile data downloads first. Swiping the
  app away (or Back on Android 10 and 11) cancelled the screen's scope; the download went on,
  but `withContext` then threw and the busy flag was never reset. While the VPN kept the process
  alive, Home's row stayed disabled and the notification's Update did nothing.
- **Root cause:** work that must outlive the screen ran in the screen's `viewModelScope`.
- **Fix:** `ApkUpdate.start` runs the tap in its own scope and resets the flag unless Android's
  installer took the file.
- **Verification:** traced; lint clean, both flavors compile.

### M10. Fixed Lows from the re-audit

- **Android: "close the app and open it again" kept M3's key reset from coming.** The minute of
  failures counted only within one run of the app, so with the VPN off, following the text
  started it afresh. The count is now kept across runs; a pause up to 10 minutes keeps it, a
  restart of the phone or a success clears it (`LastingFailureTest`).
- **Android 12 and later asked before every update.** `USER_ACTION_NOT_REQUIRED` needs
  `UPDATE_PACKAGES_WITHOUT_USER_ACTION`, now declared (website build only).
- **Android: any app could start the update download** through the exported activity's
  `ACTION_UPDATE`. The notification's intent now carries a key known only to this run of the
  app; reopening from Recents is not a new tap.
- **Android: a release whose `uni-vpn-apk.json` upload failed** had phones download the APK
  every 10 minutes on Wi-Fi. Mismatches now wait 10, 20, 40 minutes and so on, up to a day.
- **Android: a rule refresh still establishing when the core ended** could bring the rule back
  after it was withdrawn (M2). The withdrawal now takes the refresh's lock.
- **Both apps: back on screen, "not running" could show at once,** counted from before the app
  left. The 20 s now count on screen.
- **iPhone: `api-ready` left by a core that iOS ended** let requests with the token go, for
  about a second, to another app that took port 1081. `CoreHost.ready` now also needs a core to
  hold the lock (flock), as Android checks its own process. Two narrower windows remain in the
  core, see below.
- **Both apps: the "not running" page had no switch;** it now has the one the port-taken page
  has, so the VPN can be turned off there.
- **Second re-audit, Android:** a tap on an update whose file did not match downloaded it again
  at every tap (now the offer waits for the next check); turning the phone on the "not running"
  page brought back 20 s of spinner (the count now restarts only after the app was away for
  5 s); a restart of the phone was missed by M10's count once the new uptime passed the old
  one (the count now keeps Android's boot count). `test_android_texts_without_the_core_are_the_cores`
  now also checks "Update Uni VPN".

### Left as they are (Low)

- **Fixed ports 1080 and 1081.** A local proxy app on the same port (Shadowsocks' default local
  port is 1080) keeps Uni VPN's core from starting; the app says another app is in the way and
  tries again every 30 s, so it recovers once that app stops. Choosing free ports needs the core's
  config and the PAC address to follow; not worth it for a clash this rare.
- **Texts without the core follow the phone's language** on Android (`port_in_use`,
  `not_running`, the notification), not a language chosen in the app's Settings.
- **Home can show the last status after the core stops answering** (both apps): a core that
  answered once, then ended and cannot start again (the Keystore failing, say), leaves Home on
  its last state until the core is back; actions do say that Uni VPN is not running. Needs a
  lasting start failure after a working start; clearing the status after 20 s would instead
  hide iPhone's "Connecting" while a slow tunnel starts.
- **The core's ready file around a start and a stop** (`internal/daemon`): a starting core holds
  the lock a few milliseconds before it removes a file left by a killed one, and a stopping core
  frees port 1081 up to 5 s before it removes its file. Another app would have to bind 1081 in
  exactly that window to get the app's token. Reported to the Go core section with fixes.
- **ocproxy's port is chosen, closed, then bound later** (`internal/tunnel` `FreePort`): an app
  holding every other free port could take it in between. Needs tens of thousands of sockets;
  reported to the Go core section.

## Desktop apps

Scope: the native shells (app/macos, app/windows, app/linux), the page they show
(uni_vpn/ui/index.html), the installers and packaging, and self-update and hand-over on the
desktop.

### D1. Installing again over a running app can fail with "file in use" (Windows)

- Severity: Medium
- Status: fixed
- Root cause: the installer's StopRunning ended the scheduled task first and only then stopped
  the programs that run from Program Files, with a PowerShell command that takes one to several
  seconds to start. The tray app starts a stopped service again after two missed polls (3 to
  6 s). When PowerShell was slow, the tray app could start the core again after PowerShell had
  listed the running programs, and the new core then held bin\uni-vpn-core.exe, so Inno Setup
  could not replace it ("DeleteFile failed; code 5").
- Fix: packaging/windows/uni-vpn.iss stops "Uni VPN.exe" with taskkill before it ends the task,
  so nothing is left that starts the service again.
- Verification: test-install.ps1 (CI windows-installer and windows-arm-install) now ends the
  service with the tray app running and records how fast the tray app starts it again (10 s
  in CI), measures the installer's process search, and installs again over the running app,
  then checks that the core of this commit answers. Its first run failed on the test itself:
  its WebView2 probe had loaded the DLLs from Program Files, now it runs in a process of its own.

### D2. Repair can fail because the tray or menu bar app starts the service in the middle of it (Windows, macOS)

- Severity: Medium
- Status: fixed
- Root cause: the shells start a stopped service after two missed polls, without knowing that
  Repair stops and starts it on purpose. On Windows, Repair's setup ends the task, waits until
  Task Scheduler no longer reports it running, then starts it; a tray app that starts it in
  between makes setup fail ("The running service did not stop") before the proxy rule and the
  app are set up again. On macOS a menu bar app that loads the service between Repair's bootout
  and bootstrap makes all five bootstrap attempts fail.
- Fix, in two places:
  - Setup itself (internal/service), for Repair, a reinstall and setup alike. On Windows, while
    it waits for the old service to end, it ends a task that runs again once more (a shell
    starts it at most once a minute). On macOS, a bootstrap that fails while another job than
    the old one runs counts as done: that job was loaded from the file setup had just written.
  - The shells: the status already says `repairing`. All three remember when they last saw it
    and do not start the service within 60 s of that. The re-audit showed that a poll every 3 s
    often misses it (an idle service ends within a second of the Repair), so this alone was not
    enough.
- Verification:
  - Reproduced with the real Linux shell under Xvfb against a fake core that says `repairing`
    and then goes away: before the fix the shell ran `systemctl --user start` 3.1 s later.
    After it: nothing within 20 s, a start 62.6 s after the core went away, and a service that
    stops outside Repair is still started again within 6 s.
  - Go tests: setup ends a task that was started again and then starts it; a job loaded by the
    menu bar app counts, the old one going away does not.
  - CI runs Repair's setup while the service is started again at the worst moment: on Windows
    as soon as Task Scheduler no longer counts it as running (windows-installer,
    windows-arm-install: started again 1.3 s in, setup succeeded and no old service was left),
    on macOS by a loop that loads it as soon as it can (macos-pkg). On the final commit
    setup's own load came first on macOS, so the Mac race rests on the Go tests; CI warns
    when it was not tested.
  - The Windows shell compiles with mcs as C# 5 (as csc in CI), the macOS shell in CI.

### D3. The Mac service runs throttled as a background job (macOS)

- Severity: Medium
- Status: fixed
- Root cause: the LaunchAgent had ProcessType Background. macOS runs such a job with the
  lowest CPU priority and marks its sockets as background traffic that gives way to all other,
  while all of the browser's traffic to the university goes through the service.
- Fix: ProcessType Interactive (launchd.plist(5): "the same resource limitations as apps, that
  is to say, none") in internal/service/render.go and launchd/de.davidvinu.uni-vpn.plist.in.
  An update replaces only the program, so on macOS the service rewrites the LaunchAgent of an
  earlier version once (UpgradeProcessType, written in one step); launchd reads it at the next
  login.
- Verification: CI macos-pkg checks the priority of the installed service with `ps -o pri`.
  On the commit with only that check it failed: priority 4, `TASK_APPTYPE_DAEMON_BACKGROUND`.
  With the fix: priority 31, `TASK_APPTYPE_DAEMON_INTERACTIVE`, I/O tier 0.
  Go tests: the rendered plist is interactive; an older plist is rewritten once, keeps its
  mode, and only on macOS.

### D4. The window loses the page it was asked for when the service is still starting (Linux, macOS)

- Severity: Low
- Status: fixed
- Root cause: while the service starts (login, update) the page does not load and the window
  retries it. A second click on the app in that time ("as it is") replaced the page asked for
  (Settings, or setup's prefilled university ID) with the main page. The Windows shell already
  kept it.
- Fix: with nothing shown, "as it is" loads the page asked for last (app/linux load,
  app/macos load).
- Verification: real Linux shell under Xvfb: show "&user=ab123" with the service down, then
  show "", then start the service: before the fix the page was `?app=1`, after it
  `?app=1&user=ab123`.

### D5. The window stays blank after the page's process ends (Linux, Windows)

- Severity: Low
- Status: fixed
- Root cause: WebKitGTK and WebView2 do not reload a page whose process ended (crash, out of
  memory, ended by hand); the shells did not handle web-process-terminated or ProcessFailed.
  Without its browser process a WebView2 can show nothing again. WKWebView reloads by itself.
- Fix: Linux reloads the page (at most once in 10 s; opening the window loads it then).
  Windows reloads after RenderProcessExited (the same limit) and replaces the window after
  BrowserProcessExited, open again if it was open.
- Verification: Linux under Xvfb: after killing WebKitWebProcess the page was gone (JavaScript
  errors) before the fix, loaded again after it; a second kill within 10 s stays blank until the
  window is opened again, which loads it. Windows: test-install.ps1 kills the window's page
  processes and its browser process and checks that a new window comes up and that the page
  itself loaded again (the window notes it in app.log; a new page process alone proves
  nothing, WebView2 starts one for its error page). Its waits for the window's processes
  always ran out their time: in PowerShell 5.1 a function's single result comes back as the
  process object itself, which has no Count, so `(NewBrowser).Count -gt 0` was never true for
  one process (the checks after the waits, `-eq 0`, held). The test now counts as
  `@(...).Count` everywhere.

### D6. Linux app menu entry missing when the home folder's path has $, ", ` or \ (Linux)

- Severity: Low
- Status: fixed by the Go core burn-down (PR #77, the same DesktopQuote change), not twice here
- Root cause: DesktopQuote escaped these for the Exec quoting only, but a desktop entry's string
  escapes come off first, and GLib refuses an entry with `\$` or `\"` in Exec.
- Verification here: before the fix Gio.DesktopAppInfo refused the entry; with the doubled
  backslashes it loads the entry the Go code writes for a script in `a$b"c`d\e 100%` and
  launches it with exactly that path.

### D7. The Windows app window keeps an otpauth link's key on disk (Windows)

- Severity: Low (a secret on disk, readable by the user's account only)
- Status: fixed
- Root cause: an otpauth:// link opens the page with the key in its address (#otpauth=), and
  WebView2 keeps visited addresses in its history in %LOCALAPPDATA%\uni-vpn\WebView2, which
  uninstall leaves in place. The browser fallback already leaves the key out for this reason.
- Fix: the window's WebView2 runs InPrivate, and the profile earlier versions kept is deleted.
- Verification: test-install.ps1 opens an otpauth link and looks for its key in every file of
  the WebView2 folder, 20 s later and again after the browser process ended. The first version
  of that check could never fail: in PowerShell the comma binds tighter than `-bor`, so opening
  each file threw and the error was swallowed. Fixed; it now also says how many files it read.

### D8. The Windows service runs below normal priority (Windows)

- Severity: Medium (the Windows twin of D3)
- Status: fixed
- Root cause: the scheduled task had `<Priority>7</Priority>`, Task Scheduler's default for
  maintenance tasks: below normal CPU priority, low I/O and memory priority. Programs it starts
  get the same class, openconnect included. Under load (a video call, a virus scan) the proxy
  that carries every university website and the tunnel wait behind every normal program.
- Fix: priority 4 (normal) in internal/winsys. An update replaces only the program, so the
  service also sets its own priority class to normal when it starts (cmd/uni-vpn), before it
  starts openconnect. That fixes the CPU priority only: an updated install keeps low I/O and
  memory priority until the next setup or Repair writes the task again (the service reads
  and writes little on disk).
- Verification: Go test for the task priority. CI (windows-installer, windows-arm-install)
  checks the installed task's priority and that the running service has base priority 8
  (normal; below normal is 6), then gives the task an earlier version's priority 7, starts
  the service again and checks it once more.

### D9. The Linux and Mac app windows keep an otpauth link's key on disk (Linux, macOS)

- Severity: Low (a secret on disk, readable by the user's account only; the Linux twin of D7)
- Status: fixed
- Root cause: the window's web view kept its data on disk. When an otpauth link opens the
  window, WebKit's network cache records the full address, key included
  (`~/.cache/de.davidvinu.UniVPN/WebKitCache/.../SubResources/...`, mode 0644), and uninstall
  leaves it there. WKWebView's default data store is the same WebKit cache.
- Fix: the Linux window's web view is ephemeral, the Mac window's data store is not persistent,
  and both delete what earlier versions kept (Linux `~/.cache` and `~/.local/share`
  `de.davidvinu.UniVPN`; macOS `~/Library/Caches/<bundle id>/WebKit` and
  `~/Library/WebKit/<bundle id>`). The page stores nothing.
- Verification: real Linux shell under Xvfb with WebKitGTK 2.52: before the fix `grep -r` found
  the key in that SubResources file 20 s after the link opened the window; after it, nothing,
  the earlier files were gone, and the page still put the key into the sign-in code step. CI
  macos-pkg opens an otpauth link in the installed app and looks for the key in the app's
  WebKit, cache and saved state folders, before and after the app quits.

### D10. After the page's process ends, an old otpauth link is offered again (Linux, Windows, macOS)

- Severity: Low
- Status: fixed
- Root cause: the shells keep the page asked for last (D4) to load it again (D5, a failed load),
  and that included `#otpauth=<key>`. A page process that ended much later put the user back
  into "Change second factor" with the old key, during a first setup possibly over a newer one.
- Fix: once the page has loaded (Linux and macOS: load committed; Windows: navigation
  succeeded), the shells drop the otpauth part from the page they load again. On macOS WebKit
  reloads an ended page by itself, and a reload that fails while the service restarts was
  tried again with the key.
- Verification: Linux under Xvfb: open with an otpauth link, then Main, then kill
  WebKitWebProcess: the page came back in the change with the old key before the fix, on the
  main screen after it. Windows: same code path, compiled with mcs. macOS: same code path,
  compiled in CI.

### D11. The Windows window loses the page asked for while WebView2 starts (Windows)

- Severity: Low (the Windows twin of D4)
- Status: fixed
- Root cause: until WebView2 is ready (1 to 3 s), a click on the icon ("as it is") replaced the
  page asked for, an otpauth link or Settings, with the main page.
- Fix: before WebView2 is ready, "as it is" keeps the page asked for.
- Verification: traced in the code; compiled with mcs as C# 5.

### D12. The Windows window reopens over and over when its browser process keeps ending (Windows)

- Severity: Low
- Status: fixed
- Root cause: D5's new window after a browser process exit had no limit; a browser process
  that ended at once each time (security software) made a window take the focus each time.
- Fix: a new window at most once in 10 s, as for the page process. Only a window that comes
  back counts: one that ended while hidden does not keep the next one from coming back.
- Verification: traced in the code; compiled with mcs as C# 5.

### D13. Other programs could run JavaScript in the Mac app window (macOS)

- Severity: Low (only gains something for a program that cannot reach the local API itself,
  such as a sandboxed app without network access)
- Status: fixed
- Root cause: the page name of the `de.davidvinu.uni-vpn.app.show` notification, which any
  program of the user can post, went into `evaluateJavaScript("location.hash = '<page>'")`
  unescaped, so `#'; fetch(...); '` ran in the page with the whole local API.
- Fix: the page name goes into the script as a JSON string; a name that is no valid address is
  ignored instead of being force-unwrapped.
- Verification: the re-audit ran the old script text with such a name in node and the injected
  code ran; the JSON string cannot end early. The macOS shell compiles in CI.

### D14. Ending the Windows service's task left the service running (Windows)

- Severity: Medium (Repair did not restart the service; uninstall left it running until the
  user signed out)
- Status: fixed
- Root cause: the task runs `conhost.exe --headless uni-vpn-core.exe daemon`, so its program is
  conhost, which starts the service. `schtasks /End` ends only the task's own program
  (Microsoft's schtasks documentation: "Stops only the instances of a program started by a
  scheduled task"). Setup, Repair, uninstall and a restart from a terminal relied on it: the old
  service went on running, the new one could not take over, and after an uninstall it ran on
  (the uninstall moves the running program aside, so its file was gone anyway).
- Fix: after `/End`, setup ends the service too: every copy of this program of the signed-in
  account except setup itself (`taskkill` by image name, process id and user name), openconnect
  with it through the service's job object.
- Verification: reproduced in CI (windows-installer): after `schtasks /End` said "SUCCESS" and
  the task was "Ready", the conhost was gone but `uni-vpn-core.exe daemon`, its child, still
  ran and answered. Go test: ending the task also ends the service, before it is started
  again. CI: after Repair no old service is left running, and after the uninstall no
  uni-vpn-core.exe runs. openconnect does not run in CI (no connection); that it ends with the
  service rests on the job object, unchanged here.

### D15. The Repair button could end its own setup halfway (Windows)

- Severity: Low as far as CI shows, High where it happens (Repair would do nothing but a
  restart a minute later)
- Status: fixed
- Root cause: the service starts Repair's setup outside its kill-on-close job. Where Windows
  does not allow that (an outer job without breakaway), it starts setup inside the job
  instead. Since D14 setup ends the service, and with the service's job Windows ended setup
  too, before it started the new service.
- Fix: in that case the job ends nothing with the service until setup is done
  (winsys.ChildrenEndWithUs); if the service still runs then, its job kills its children
  again. Repair is offered only where no tunnel runs (a missing program, a failed start), so
  openconnect is not running meanwhile as a rule (corrected in the tenth re-audit).
- Verification: Windows Go test with a real job: a child stays in the job and ends with its
  parent, and outlives it with the limit lifted. CI (windows-installer, windows-arm-install)
  now presses Repair through the local API as the app does: the new service of this commit
  must answer within 40 s (the tray app would only start it after a minute), and Repair must
  write an earlier version's task (priority 7) again. It answered after 1.5 s (x64) and
  1.8 s (ARM), with the task at priority 4. Both ways of starting setup now succeed, so CI
  does not show which one Windows took.

### Left as they are (Low)

- macOS: the packaged app reads the service's port from its Info.plist (1081); a port changed
  by hand in config.toml is not followed by the app window. Only a hand edit changes the port.
- Linux and macOS: the app registers for otpauth:// links, so another authenticator app that
  also handles them may lose the default to it. Wanted for the MFA portal's link.
- Windows: taskkill and the installer stop the tray apps of every account on the computer;
  they run from the same files that the installer replaces.
- Windows: the tray app's status poll has no timeout of its own; WebClient fails at once when
  nothing listens, and the service answers in milliseconds.
- macOS: the login item is registered without the retries the service gets; a failed
  registration is repaired by the next setup or Repair.
- macOS: when the updated menu bar app cannot be started, the running one goes on as it is
  and tries again only after its next start.
- Windows: the elevated setup (installer, Repair, which a program of the user can start through
  the local API) reads things the user can write: PowerShell finds Get-ScheduledTask through
  the user's module path, and the task's XML is written to and read from %LOCALAPPDATA%. A
  program of the user could so get its code run with administrator rights. The re-audit rated
  it High; left as Low because uni-vpn on Windows is set up only from an administrator account,
  where programs of the user already reach administrator rights in other ways (the user's own
  environment variables reach every elevated program the user starts), and Microsoft does not
  treat UAC as a security boundary. No other account's data is reachable this way.
- Linux: when an otpauth link starts the shell (it was not running), the key is in that
  process's command line, which other accounts can read with ps for as long as it runs. The
  fix would be D-Bus activation instead of the command line.
- macOS: a second end of the page's process within about 30 s leaves the window blank until
  the app restarts (WebKit reloads only once).
- macOS: when two copies start at the same moment, the hand-over posts an otpauth link's key
  to the user's other programs that listen for that notification.
- Windows: after its browser process ended, the new window shows the main page, not the page
  shown before.
- macOS: setup counts a job that runs after its own bootstrap failed as the one the menu bar
  app loaded. If no job ran when setup looked (the service in launchd's wait between crashes)
  and launchd starts the old job in the milliseconds before setup unloads it, setup reports
  success with nothing loaded. The menu bar app loads it again within a minute.
- CI: the checks that no otpauth key stays on disk find nothing, but do not show that the page
  got the key (the Linux check was shown to fail before the fix under Xvfb). The macOS Repair
  race is tested only when the test's bootstrap comes before setup's; CI says when not.
- Windows: setup counts the old service as ended once Task Scheduler no longer runs the task
  and taskkill has run; it does not check that the old process is gone. taskkill matches the
  account the same way the task's own principal does (winsys.CurrentUser), shown in CI.
- macOS: the one rewrite of an earlier version's LaunchAgent (D3) may make macOS show its
  "Background Items Added" note once after the update.
- Windows: the window's InPrivate mode needs a WebView2 runtime from 2022 or later (101);
  with an older one the page opens in the browser. The runtime updates itself.
- Linux: after a self-restart the old WebKit processes stay as zombies (two per restart);
  `pkill -f` takes the script path as a pattern (a home path with `+`, `(` or `[` misses);
  a restart after an update can drop a half-done change while the window is hidden.

## Final re-audit

Scope: the whole repository at 78c79e6 (after #74 to #80), read by five fresh reviewers, one per
area: the core's sign-in path and API, the core's system parts (proxy, service, setup, updates,
sync, relay), the desktop apps and installers, the phone apps, and the website, CI, release,
translations and docs. The full test suite on that commit passed first: `go test -race ./...`,
599 Python tests, the relay's tests, shellcheck, and all 23 CI jobs.

| # | Bug | Severity | Status |
|---|---|---|---|
| FA-1 | Android tablets (Chrome's "Desktop site", on by default there) are offered the Linux packages | Medium | fixed |
| FA-2 | A phone with 32-bit Android hears only "App not installed"; the page does not say the app needs 64-bit Android | Medium | fixed (the page says so; a 32-bit build is not made) |
| FA-3 | Windows 11 with Smart App Control on blocks the unsigned installer outright, and the page's tip does not cover it | Medium | partly fixed: the page says so; only code signing (needs David's certificate) lets it run |
| FA-4 | The privacy page does not say that the Android app's QR scanner (Google Play services) sends data to Google | Medium | fixed |
| FA-5 | The privacy page says only the terminal scripts fetch from the Linux package sources; the normal .deb and .rpm do too | Low | fixed |
| FA-6 | Linux with no password store at all (no gnome-keyring, KDE before 5.97): "Your saved passwords are locked. Sign out..." and no way through | Medium | fixed |
| FA-7 | Windows PC with two accounts: the second account's install takes over the scheduled task, and the first account's Uni VPN never starts again | Medium | fixed |
| FA-8 | Another local account that holds 127.0.0.1:1081 first answers this account's proxy rule and app window | High | fixed |
| FA-9 | Another local account can use this account's VPN session through 127.0.0.1:1080 and start its sign-in | Medium | fixed |
| FA-10 | Chrome, Edge and Brave on Xfce and LXQt never read the proxy rule, while setup reports success | Medium | fixed |
| FA-11 | The sync relay can be filled for 400 days with a few thousand anonymous requests | Medium | fixed in the code; the live relay does not have it yet (SR-3) |

### FA-1 Android tablets offered the Linux packages

- Found by: the website reviewer. Chrome on Android with "Desktop site" sends "X11; Linux
  x86_64" and platform "Linux" (Chromium's SetDesktopUserAgentOverride); Google turns it on by
  default on large tablets. The page then offered uni-vpn.deb and .rpm as the main buttons.
- Root cause: Android was recognized only by "Android" in the user agent.
- Fix: on Linux (not ChromeOS), a touch screen that is the only pointer (`pointer: coarse`, no
  `any-pointer: fine`, maxTouchPoints > 0) counts as Android.
- Verification: headless Chromium with the desktop-site user agent and client hints, touch
  emulated: the APK button and the Android note; a Linux desktop, a Linux touch laptop with a
  touchpad (maxTouchPoints 10, fine pointer) and a Chromebook in tablet mode still get the
  Linux files; Windows, Android phones unchanged; no script errors, no horizontal scroll.

### FA-2 32-bit Android not explained

- Found by: the phone and website reviewers. The APK holds arm64-v8a and x86_64 code only
  (build.gradle.kts abiFilters, build-apk.sh); Android Go phones and some older models run
  32-bit Android 10 or newer and refuse it with "App not installed".
- Fix: the Android note says "Android 10 or newer (64-bit)" in all six languages; where the
  browser still tells it (navigator.platform armv7l, armv8l or i686), the page says instead
  that Uni VPN needs 64-bit Android and this phone has 32-bit Android, and keeps the APK in the
  list. Chrome's frozen value "Linux armv81" does not match, so such phones keep the button.
- Verification: headless Chromium with "Linux armv8l": no button, the message in en, de, fr,
  the APK in the list; "Linux aarch64" and "Linux armv81": the button and the 64-bit note.
- Left: building a 32-bit APK (openconnect, ocproxy and the core for armeabi-v7a).

### FA-3 Smart App Control

- Found by: the website reviewer. The release's uni-vpn-setup.exe is unsigned (empty security
  directory). With Smart App Control on, Windows blocks it with no "Run anyway", and the core
  and openconnect too (ci.yml's own comment).
- Fix: the Windows tip says that if Windows reports Smart App Control blocked the file, Uni VPN
  cannot run on this computer yet (six languages, Windows' own name for the feature).
- Left: code signing of every program in the setup; needs a certificate (David).

### FA-4 and FA-5 Privacy page

- FA-4: app/android uses play-services-code-scanner (Scanner.kt, with auto zoom, manifest
  `com.google.mlkit.vision.DEPENDENCIES` barcode_ui). Google's ML Kit data disclosure
  (developers.google.com/ml-kit/android-data-disclosure, read 2026-11-15): device information,
  package name and version, a device identifier for these features, performance metrics, API
  configuration, event types and error codes, and with auto zoom a session id, zoom levels and
  the code's bounding box. A new item 6 in "Who the app connects to" says this, in German and
  English; scanning is only started by "Scan QR code" and "Scan code", typing the code does not
  use it.
- FA-5: the last item now says the Linux package's dependencies come from the distribution's
  package sources, and only the terminal scripts fetch from GitHub, python.org, Homebrew or
  nuget.org.

### FA-6 Linux without a password store

- Found by: the core reviewer, reproduced with the real secret-tool 0.21.4 under
  dbus-run-session with no keyring: "secret-tool: The name org.freedesktop.secrets was not
  provided by any .service files", exit 1, for lookup, store and clear. Setup and Settings
  then said "Your saved passwords are locked. Sign out of the computer and back in", which
  changes nothing.
- Root cause: every failed `secret-tool store` became a KeyringError (keyring_locked), and a
  failed lookup "No password saved".
- Fix: credentials.NoStoreError for that answer (dbus-daemon's and dbus-broker's "The name is
  not activatable" wording); new message keyring_missing in all six languages: install
  "Passwords and Keys" from Discover or the Software app, then Try again. Ubuntu's seahorse
  depends on gnome-keyring, which brings D-Bus activation of org.freedesktop.secrets, so no
  sign-out is needed. NoStoreError counts as no secret saved for sync.
- Verification: TestNoSecretServiceIsNoStore (both wordings; a locked collection stays a
  KeyringError; never on macOS), TestNoPasswordStore (status and Settings); the real core with
  the real secret-tool and no keyring now answers /api/setup and /api/password with
  msg.keyring_missing.

### FA-7 Windows PC with two accounts

- Found by: the desktop reviewer, by reading (no second Windows account here). Task names
  hold for the whole computer. Setup registered "uni-vpn" with `schtasks /Create /F` and this
  account as the logon trigger and principal, so the second account's install replaced the
  first account's task. The first account's Uni VPN then never started again: not at login,
  and the tray app's `schtasks /Run /TN uni-vpn` ran the other account's task (or failed).
  Its install again took the task back.
- Root cause: one fixed task name, winsys.TaskName, for a per-account service.
- Fix: every account has its own task, "uni-vpn-" and the account's SID (as OneDrive names
  its per-user tasks): winsys.TaskName(sid), winsys.CurrentSID, service.Manager.SID. Setup and
  uninstall end and delete the task of earlier versions only when it is this account's
  (Task Scheduler's principal, a name or a SID, compared as a SID), never another account's.
  An update replaces only the program, so the service, started from the earlier version's
  task, registers its own task from the service file it wrote at install
  (AddAccountTask); until setup runs again, both start it at login and the second copy ends
  at once (the lock). The tray app starts this account's task, falling back to the earlier
  one; the installer stops every "uni-vpn*" task before it replaces the files.
- Verification: TestWindowsEveryAccountHasItsOwnTask (no earlier task, another account's,
  this account's: only this account's is ended and deleted, after the service ended and
  before its own task starts), TestWindowsServiceAddsItsOwnTaskAfterAnUpdate,
  TestTaskName; on Windows CI against the real Task Scheduler:
  TestTaskOwnerScriptAgainstTaskScheduler, and test-install.ps1 now installs (own task, no
  "uni-vpn"), turns the install into an earlier version's (only "uni-vpn", priority 7),
  checks that its service registers its own task, and that the Repair button deletes
  "uni-vpn" again; test-uninstall.ps1 checks that no "uni-vpn*" task is left. Failing first:
  the new Go tests fail with the old fixed name. The first Windows CI run of the task owner
  test failed: without the task, `powershell -Command` ends with exit code 1 (the error that
  `-ErrorAction SilentlyContinue` silences still counts). Setup took that as "not ours", which
  is right there, but the script now ends with `exit 0`, and the test shows what PowerShell
  wrote.
- Left (Low): a computer where a second install already took the task before this version:
  the account without a task needs the installer once more (its "Restart the computer" does
  not bring a task back); after that neither takes the other's again. Uninstalling from one
  account leaves the other accounts' tasks, which then start nothing.

### FA-8 Another account on the status port

- Found by: the system reviewer, by reading. A port on 127.0.0.1 is the computer's, not an
  account's: whatever another account runs on 1081 first (its uni-vpn, which starts first
  after a restart, or any program of it) answers this account too. On Linux and Windows this
  account's proxy rule (GNOME, KDE, the Windows setting) points at
  http://127.0.0.1:1081/proxy.pac, so the browsers took their rule from the other account's
  program, which could send them through any proxy; and the app windows loaded whatever page
  answered there, for example a sign-in form that asks for the university password.
- Root cause: the rule and the app windows trusted the fixed port; nothing checked whose
  program listens there.
- Fix:
  - Core: when the status port is taken by a program of another account
    (sockowner.OtherAccountListens), the rule is switched off (sysproxy.Withdraw) and a marker
    puts it back once the port is this account's, also after a restart. On macOS the rule is
    the computer's: the uni-vpn that holds the port switches it on again (Cover), at once when
    the other one asks for the PAC file after switching it off (Knock, OtherAccountSeen).
  - App windows (Linux, macOS, Windows): the core writes an instance file that only this
    account can read: a random id, and while another account holds the port the page that
    says so (OtherAccountWindow, in the language setting or the system's). status.json says
    the id. The windows load the status port's page only once status.json says that id; they
    show the core's note while another account holds the port, nothing while something else
    answers, and keep the page from reloading itself meanwhile. The menu then says
    "Not running" and takes no texts from that program. Without the file (an earlier core)
    they work as before.
  - The uni-vpn of another account answers this account only with a PAC file that sends
    everything directly, and refuses everything else (as before).
- Verification: Go tests, failing first without the change:
  TestRuleIsOffWhileAnotherAccountHoldsTheStatusPort, TestRuleIsOnAgainOnceTheStatusPortIsFree,
  TestWithdrawSwitchesOffOnlyOurRule, TestMacCoverSwitchesOnWhatAnotherAccountSwitchedOff,
  TestAnotherAccountGetsOnlyThePAC, TestInstanceFileTellsTheAppWindowsWhatIsOurs,
  TestInstanceFileHasNoPageForThisAccountsProgram,
  TestAnotherAccountsRequestBringsTheRulesLookForward, TestOtherAccountWindow. End to end on
  Linux with the real core and a second account (nobody): while it held 1081 the GNOME rule
  went to "none" and the log said why; once the port was free, "auto" again with a new `?v=`.
  The real Linux app window (WebKitGTK under Xvfb) against a fake uni-vpn of the other account
  (its own sign-in page, a status.json without the id): the note while this account's uni-vpn
  ran, this account's page once the port was free, nothing while only the fake answered, the
  note again after a restart, and never the fake page. The note's language in Chromium: de,
  zh-TW, zh-CN and es as the system's, ja falls back to English, the setting fr wins. The macOS
  and Windows windows have the same logic; CI builds them (not run with a second account).
- Left (Low): on macOS the uni-vpn that holds the port also switches the rule back on after a
  user switched it off by hand. If this account's uni-vpn stops while its page is open and a
  program of another account takes the port and answers the app's own checks with a reset,
  the open page talks to it for the few seconds until the app starts this account's uni-vpn
  again, which then puts the note in place. A failing owner lookup counts as this account.
  Windows without a WebView2 runtime opens the page in the browser, without the check.

### FA-9 Another account on the SOCKS port

- Found by: the system reviewer, by reading. The SOCKS port 1080 took every local connection:
  a browser of another account (the rule is the computer's on macOS) used this account's VPN
  session, and its connection started this account's sign-in. Whatever held 1080 instead got
  this account's browser connections.
- Root cause: the forwarder checked no owner, and the rule pointed at the SOCKS port whoever
  held it.
- Fix: the forwarder refuses connections of other accounts after the SOCKS greeting, before a
  target is asked (sockowner.OtherAccount; a failing lookup lets it through). Other accounts get
  a PAC file that sends everything directly (pac.Direct). While another program holds the SOCKS
  port, the rule sends everything directly too, and the browsers are told to load it again
  when that changes.
- Verification: TestRefusesOtherAccounts (forwarder), TestSocksPortRefusesOtherAccounts and
  TestRuleIsDirectWhileTheSocksPortIsTaken (daemon). End to end on Linux: the other account's
  SOCKS connection was reset and its PAC file was DIRECT, this account's got `05 00` and a PAC
  file with SOCKS; while nobody held 1080 this account's PAC file was DIRECT, and the rule's
  address changed when 1080 was taken and again when it was free.

### FA-10 Chrome on Xfce and LXQt

- Found by: the system reviewer, with the real Chromium. Chromium picks where it reads the proxy
  setting by desktop (base::nix::GetDesktopEnvironment, ProxyConfigServiceLinux): GNOME's on
  GNOME, Cinnamon, Pantheon, Budgie, Unity, Deepin, UKUI and MATE (Chromium counts
  DESKTOP_SESSION=mate as GNOME), KDE's on KDE, and on Xfce, LXQt and any other desktop only
  the environment variable `auto_proxy`. Setup wrote only GNOME's and KDE's setting and said
  "Chrome, Edge and Firefox read it on their own"; README said to enter the address in the
  browser, which Chrome on Linux has no setting for.
- Reproduced: Chromium 1243 under dbus-run-session, GNOME setting "auto" with a PAC server that
  logs fetches. XDG_CURRENT_DESKTOP=GNOME: 2 fetches; XFCE, LXQt, MATE without DESKTOP_SESSION:
  0; MATE with DESKTOP_SESSION=mate (as a real MATE session has it): 2, so MATE is not
  affected; XFCE with `auto_proxy` set: 2; GNOME with `auto_proxy` set: the GNOME setting's
  address, not the variable's.
- Root cause: the rule reached only the settings Chromium reads on GNOME-like desktops and KDE.
- Fix:
  - sysproxy (Linux): Install also puts three lines into `~/.profile` and `~/.xprofile`
    (`export auto_proxy=http://127.0.0.1:1081/proxy.pac` between marker lines), which a session
    reads at login: LightDM's session wrapper reads both, GDM both, SDDM `~/.profile` through
    a login shell and `~/.xprofile` for X11 (read in Ubuntu 24.04's lightdm, gdm3 and sddm
    packages). Lines there before are replaced where they are; a linked file (dotfiles) stays
    a link; lines whose end marker is gone are left alone. Uninstall takes them out (a file
    left empty is removed). Withdraw (FA-8) takes them out too, so a session that starts while
    another account holds the status port does not take its rule from there; Install puts them
    back. Cover adds them while the rule is ours, for installs from before.
  - Setup: in a session whose Chrome reads only the variable, and does not have it yet, the
    assistant's last step says "Sign out of the computer and back in once" instead of
    "Close and reopen your browser once" (`setup.all_set_signout`, in all six languages), and
    so does the terminal.
  - README says how the rule reaches Chrome there.
- Verification: TestChromeReadsSettings (Chromium's choice, table above),
  TestLinuxInstallWritesTheSessionFilesAndUninstallTakesThemOut,
  TestLinuxUninstallWithoutBackupTakesTheSessionLinesOut,
  TestSessionLinesAreReplacedWhereTheyAre, TestSessionFileThatIsALinkStaysALink,
  TestSessionLinesWithoutTheirEndAreLeftAlone, TestWithdrawTakesTheSessionLinesOut,
  TestLinuxCoverAddsTheSessionLinesWhileTheRuleIsOurs, TestNoSessionFilesOnMacAndWindows,
  TestSetupOnXfceSaysToSignOutOnce; all but the first fail without the change. End to end
  with the real Chromium, sysproxy on Ubuntu's own `.profile`, and the login steps of LightDM
  and SDDM as their scripts do them: Xfce 0 fetches before, 2 after setup (LightDM), LXQt 2
  (SDDM), GNOME still the GNOME setting's address; after Withdraw the next Xfce login 0, after
  setup again 2; after uninstall `.profile` byte for byte as before, `.xprofile` gone, 0; an
  earlier version's install got the lines from Cover. The assistant's last step in Chromium
  with `signout=1`: the new text in English, German and Chinese (Taiwan); without it the old.
- Left (Low): the lines take effect from the next login (the assistant says so on a new
  install, also without a website list since SR-1; an install from before gets them at the
  next login without a word). Chrome there
  is not told when the rule changes (no setting to watch): an edited website list reaches it
  when it restarts or loads the rule again on its own (the app says so since SR-2, for installs
  from before it since TR-4).
  A program of another account that holds the
  status port before this account signs in gives Chrome there its rule for that one session
  (the app window shows the note, and the next login has no lines until the port is ours).
  Sessions that read neither file (zsh on SDDM with Wayland, startx) do not get it.

### FA-11 The sync relay filled by anyone

- Found by: the system reviewer. Anyone can make groups at random ids with a token of their
  own (256 KiB each) and read each once, which makes it live 400 days (a read moves `updated`
  once a day). The database's limit (500 MB on the free plan) is reached with about 2,000
  groups, about 4,000 requests; then no pairing slot or group can be written, for anyone,
  until the operator clears it. Nothing limited a client.
- Reproduced: the reviewer's test against the real worker.js and d1.mjs: 50 groups of 256 KiB,
  each made and read once, all still there after the cleanup on day 399 (13,107,200 bytes).
- Root cause: the relay had a size limit per blob, but none per client and none for the whole
  database.
- Fix (relay/worker.js, schema.sql):
  - A network (CF-Connecting-IP: an IPv4 address, an IPv6 /48) may add 4 MiB a day: pairing
    slots, new groups, and what a group grows by. Above it: 429. Only a SHA-256 of the network
    with a secret of the day is kept (tables `quota`, `meta`); counts and secret are replaced
    on the first request of the next day, and the hourly cron deletes old counts. Without the
    header (the local test server) nothing is counted.
  - The size of all blobs is kept in table `usage` by triggers. New groups and pairing slots
    stop at 400 MB (507); groups in use may still be written up to 450 MB, and smaller always.
    The answers that were there (409, 412, 403, 404, 410) stay.
  - docs/sync.md says so; the privacy page (German and English) says that a hash of the
    address is kept for a day, and on what legal basis.
- Verification: `node --test relay/worker.test.mjs`, new: one network cannot fill the
  database (16 groups of 256 KiB, then 429; a pairing slot too; the same /48 counts as one, the
  next day it may add again, no header no count), a group that grows counts like new data, a
  full database takes no new groups or slots while groups in use still change, the database
  knows its size (usage equals the blobs after writes, reads, deletes and the cleanup), only a
  hash of the network is kept for one day, and `network` for IPv4 and IPv6 forms. All but the
  size test fail with the old worker.js. The reviewer's test, with a network address, now gets
  429 at the 17th group. The Go client's tests against `relay/serve.mjs` pass.
- Left (Low): many networks together (a botnet, cloud machines) can still fill it, at 4 MiB
  per network a day; then new pairings fail until the junk expires, while existing groups keep
  syncing. Requests that add nothing (reads) are not limited: they can use up the plan's
  requests for a day. Campus networks behind one address share the 4 MiB: about 2,000
  pairings a day (in the Go tests a new group is about 1 KB, a pairing slot about 100 bytes). The Go client shows "Can't reach the sync service" for 429 and 507.

### CI on #84

- go (windows-latest): the new session test checked that `.profile` keeps mode 0644; Windows
  reports 0666 for every writable file. The check now runs on macOS and Linux only, as the
  daemon's mode checks do.
- go (macos-latest): `TestInstanceFileTellsTheAppWindowsWhatIsOurs` (from #83) failed once. Root
  cause: once the status port was free again, `bindHTTP` set `d.http` before it took the
  "another account" page out of the instance file, so for a moment the port was ours while the
  file still said it was not (Low: an app window could show the page until its next look).
  Reproduced on Linux: 7 of 40 runs with `-race` failed. Fix: the file is written first. Then
  100 of 100 runs passed.

## Second re-audit

Scope: the whole repository at 0c7bf0d (FA-1 to FA-11 in), read by five fresh reviewers in the
same five areas. Their tests passed on that commit: `go test` and `go vet` for linux, darwin and
windows, the relay's tests, the Go client against the real worker, test_i18n, and the page in
headless Chromium with 16 browsers and platforms.

| # | Bug | Severity | Status |
|---|---|---|---|
| SR-1 | Xfce and LXQt: "sign out once" was missing for 45 of 46 universities (no website list yet) | Medium | fixed |
| SR-2 | Xfce and LXQt: a changed website list reaches Chrome only when it starts again, and the app said only "Saved" | Medium | fixed |
| SR-3 | The live sync relay still runs the code of #18: FA-11 and the relay fixes of #28 and #29 never reached it | Medium | open: the deploy needs David (two repository secrets, or his go for a deploy from here) |
| SR-4 | Chromebooks were offered the Linux packages, which cannot reach ChromeOS's own browser | Medium | fixed |
| SR-5 | Windows: the browser's "isn't commonly downloaded" comes before the blue box, and the page did not say how to keep the file | Medium | fixed |
| SR-6 | Another account of the computer can use this account's connected VPN through the tunnel's own SOCKS port | Low (reviewer: Medium) | left, see below |
| SR-7 | Phones: any app can use the SOCKS port 1080, which also starts the sign-in | Low (reviewer: Medium) | left, see below |

### SR-1 No "sign out" without a website list

- Found by: the desktop reviewer. The assistant's last step showed `setup.all_set_signout`
  only when the university has websites; 45 of the 46 listed (and "Not listed") have none, so
  it said only "Add the websites ... under Settings, Websites", and Chrome on Xfce still never
  read the rule in that session.
- Reproduced: the real core, the page in Chromium with `?app=1&signout=1` and no websites:
  the old text in all six languages (my FA-10 check had set a website first).
- Root cause: the sign-out text was a variant of the "websites set" text only.
- Fix: `setup.all_set_no_domains_signout` (six languages): add the websites, then sign out
  once.
- Verification: the same run: the new text in en, de, fr, es, zh-Hans, zh-Hant with
  `signout=1` and no websites; the old texts without `signout`, and with websites.

### SR-2 Xfce and LXQt: "Saved" after a change of the websites

- Found by: the system reviewer, with the real Chromium (XFCE, `auto_proxy` to a PAC server
  that logs fetches): after a change of the PAC file 3 fetches in 3 minutes, all at start, no
  new one; after a restart of the browser the new rule. The app said "Saved"; it has "Saved.
  Restart the browser" (`app.saved_restart`) for `pac_refresh: "manual"`, which the core never
  reported.
- Root cause: `pac_refresh` was always "auto"; the core cannot tell the desktop (its service
  may start before the session's variables reach systemd).
- Fix: setup, which runs in the session, records `rule-at-browser-start` in the state folder
  where Chrome takes the rule only from the session files (written on Xfce, LXQt and others,
  removed on GNOME and KDE, left as it is without a desktop, as in a repair the service
  started); the core reports `pac_refresh: "manual"` while it is there.
- Verification: TestSetupRecordsThatChromeTakesTheRuleAtItsStart and
  TestStatusSaysToRestartTheBrowserWhereChromeTakesTheRuleAtItsStart fail before, pass after.
  The real core: the page reads `pac_refresh` "auto", with the file "manual".

### SR-3 The live relay is behind main

- Found by: the website reviewer, read only: the Worker `uni-vpn-sync` (the `SyncRelay` every
  released core uses) runs worker.js of #18, and its D1 has only `pairs` and `groups`. Every
  run of relay.yml said "No credentials": the repository has no Cloudflare secrets, and the
  relay was once deployed by hand. So FA-11 is not live, nor #28's limits (a new group at
  most 64 KiB and gone after 7 days unless used, the hourly cleanup); the live relay can still
  be filled.
- Root cause: deploying needs secrets that are not set, and a run without them passed.
- Fix: relay.yml now fails with an error when `relay/` changed and nothing could be deployed,
  so a relay behind main shows. The deploy itself (schema.sql first, then the Worker; the
  schema only adds tables and triggers and counts the existing rows) needs David: either the
  two secrets `CLOUDFLARE_API_TOKEN` and `CLOUDFLARE_ACCOUNT_ID` (then re-run relay.yml), or
  his go to deploy from this thread. Not done yet.

### SR-4 Chromebooks

- Found by: the website reviewer: ChromeOS (`CrOS`) counted as Linux and got the .deb, which
  goes only into the Linux container; its proxy rule never reaches ChromeOS's own Chrome, and
  the container has no password store by default.
- Fix: the page says "Not on Chromebooks yet. Open this page on another computer or on an
  Android phone." (six languages) and keeps the full list below; README says so.
- Verification: the page in Chromium with ChromeOS x86, ARM and armv8l: the note, no button;
  Windows, Mac, Linux (x86, ARM, 32-bit ARM), Fedora, Android and iPad as before.

### SR-5 Windows download warning

- Found by: the website reviewer, by reading: the unsigned installer gets a new hash with every
  release, so Edge says it "isn't commonly downloaded" and keeps it only through "…", "Keep",
  "Show more", "Keep anyway" (Microsoft Learn, "Troubleshoot download failures"). The page
  and README named only the blue box after it.
- Fix: the Windows tip and README start with that path. English and German name Edge's
  buttons (both from Microsoft's pages); French, Spanish and Chinese say what to do in words,
  as Microsoft's pages there are machine translations. Code signing (needs David) ends it.
- Verification: the page in Chromium with a Windows browser in all six languages.

### SR-6 and SR-7, rated Low

- SR-6 (core reviewer): FA-9 refuses other accounts on 1080, but the connected tunnel's own
  SOCKS port (ocproxy on Linux and macOS, the core's SOCKS server on Windows, a port from
  20000-31999 that `ps` shows) answers anyone on the computer. Another account that looks for
  it uses this account's VPN while it is connected; on Windows any program can also send
  through the VPN's network adapter, as with every VPN there. Rated Low: it takes someone of
  another account who does it on purpose; no browser does it by itself (other accounts get a
  DIRECT rule and are refused on 1080 since FA-9), and it cannot start the sign-in. A VPN
  that installs its own routes (Cisco's, openconnect with a tun device) gives every account
  the same. Closing it on Linux and macOS means replacing ocproxy.
- SR-7 (phone reviewer): on Android and iPhone every app can use 1080, and its connection
  starts the sign-in, as a browser's does. Rated Low: that is how the VPN starts by itself for
  any app that uses the phone's proxy setting (a university app, a web view); other VPN apps
  give every app the VPN too. docs/android.md and docs/ios.md describe it ("Known gap: the
  proxy port") and the way to close it (`getConnectionOwnerUid`).

### Left as they are (Low), second re-audit

- On a computer, an own listener that has not accepted yet can count as another account's in
  `sockowner.OtherAccountListens` (7 of 300 runs), only when another program of the same
  account holds 1081; and a service of another user there (root, `nobody`) gets the
  "another user account" note, which does not fit.
- `bindHTTP` does not announce a rule that an update changed while the port was held.
- Linux app window: a load that fails just before the window notices the core is gone stays
  blank until a click on the app; during a core restart, Settings from the menu or an
  otpauth link do nothing.
- Android: Off can end the service before its disconnect request is sent (the core then ends
  the tunnel when idle; nothing goes through it); the setup page's error can say "tap Repair"
  where only Home has the button; the release APK's R8 build is not run by the emulator tests.
- Session files: a dangling `~/.profile` link becomes a file, a file of only spaces is removed
  on uninstall, the newline we added stays, and an uninstall during a Cover tick can leave
  the lines.
- Texts: README and docs/windows-test.md name the task "uni-vpn" (it is "uni-vpn-<SID>"),
  README has no row for `msg.keyring_missing`, the privacy page names the GNOME keyring where
  KDE uses KWallet and "an IP address" where IPv6 counts a /48, and says computer where the
  phone's name is used.
- CI: `promote` cannot push after a force-push to main.

## Third re-audit

Scope: the whole repository at 236ead1 (#84 in), read by five fresh reviewers in the same five
areas. Their tests passed on that commit: `go test` (also `-race` for the daemon, API,
forwarder, sockowner and tunnel), `go vet` for windows and darwin, the relay's tests and the Go
sync client against the real worker in workerd, test_i18n, the page in headless Chromium, the
real Linux app window under Xvfb, and the real setup with its own D-Bus and dconf.

| # | Bug | Severity | Status |
|---|---|---|---|
| TR-1 | Setup switches the proxy rule on after the core withdrew it, while another account holds the status port (FA-8 again, for the install path) | High | fixed |
| TR-2 | Android tablets that can take a pen (Samsung's, Pixel Tablet) are still offered the Linux packages (FA-1 incomplete) | Medium | fixed |
| TR-3 | The Windows tip names Edge's buttons; Chrome, the largest browser on Windows, has none of them | Medium | fixed |
| TR-4 | Xfce and LXQt installs from before SR-2 still say only "Saved" after a change of the websites | Medium | fixed |
| TR-5 | Linux with automatic updates off: installing a newer .deb or .rpm never updates the program | Medium | fixed |

### TR-1 Setup switches the rule on while another account holds the port

- Found by: the system reviewer. FA-8's core withdraws the rule once, when it starts and finds
  the status port held. Setup starts (or restarts) the core and only then installs the rule,
  so the rule ended up on, pointing at the other account's program, until the next login. In
  some orders the `rule-withdrawn` marker was never written either (on a first install there
  was nothing to withdraw yet), so the rule did not come back once the port was free.
- Reproduced: the real `uni-vpn-core setup --no-gui` in a fresh home with its own D-Bus and
  dconf, a fake systemctl that starts the real daemon, and a listener of the account `nobody`
  on the status port: 6 of 6 first setups ended with GNOME's mode 'auto' and our address (the
  reviewer: 8 of 10, and Chromium then fetched the PAC from `nobody`'s listener).
- Root cause: setup installs the rule without looking who holds the port.
- Fix: setup asks `sockowner.OtherAccountListens` first. While another account's program holds
  the port it leaves the rule off and writes `rule-withdrawn` (now `sysproxy.WithdrawnName`),
  so the core switches the rule on once the port is this account's, however the two runs
  overlap. The record for Chrome on Xfce (SR-2) is still written.
- Verification: TestSetupLeavesTheRuleOffWhileAnotherAccountHoldsThePort fails before, passes
  after. The same real-setup run: 8 of 8 left mode 'none', no session lines, the marker
  written; with the listener stopped, the core took the port after 27 s and switched the rule
  on (mode 'auto', our address, `.profile` lines), the marker gone.

### TR-2 Android tablets that can take a pen

- Found by: the website reviewer. FA-1 counted a "Desktop site" Linux browser with a touch
  screen as a tablet only without `any-pointer: fine`, but Chromium on Android reports a fine
  pointer for any device that can take a pen (Samsung's touch screens, Pixel Tablet), with no
  pen there (ui/android TouchDevice.java).
- Reproduced: the page in headless Chromium with a desktop Linux user agent, 10 touch points,
  `pointer: coarse` and `any-pointer: fine`: the .deb and .rpm buttons.
- Root cause: `any-pointer` counts what a device can take, not what it is used with.
- Fix: a Linux browser whose main pointer is coarse and that has touch points is a tablet
  (`pointer: coarse`). A Linux computer with a mouse or touchpad has a fine main pointer.
- Verification: tablet with and without pen support: uni-vpn.apk; Linux touch laptop and
  desktop: .deb and .rpm. The other 15 browsers and platforms unchanged.

### TR-3 Windows tip for Chrome

- Found by: the website reviewer: Chrome says "Suspicious download blocked" and offers only
  "Delete" and "Download suspicious file"; the tip named Edge's "…", "Keep", "Show more",
  "Keep anyway".
- Fix: the tip follows the browser: Edge's steps in Edge, Chrome's ("Download suspicious
  file", German „Verdächtige Datei herunterladen“) in Chrome, and "open its downloads and
  allow the file" in Firefox, Opera and others; the blue box and Smart App Control parts stay
  for all. README names both browsers.
- Verification: the page in Chromium with Edge, Chrome, Firefox and Opera on Windows in all six
  languages: the matching text each time.

### TR-4 Xfce and LXQt installs from before SR-2

- Found by: the desktop reviewer. Only setup recorded `rule-at-browser-start`, and an update
  does not run setup, so every install from before SR-2 kept `pac_refresh: "auto"` ("Saved")
  while Cover gave Chrome the rule from the session files.
- Reproduced: the real core in a home without the record, gsettings with our address: after
  the start `.profile` had our lines, status.json said "auto".
- Root cause: only setup, which runs in the session, knows the desktop; the service may not.
- Fix: the Linux app window, which runs in the session, names its desktop in its status polls
  (XDG_CURRENT_DESKTOP, DESKTOP_SESSION, GNOME_DESKTOP_SESSION_ID, KDE_FULL_SESSION), and the
  core writes or removes the record as setup does. A poll that names no desktop (the copy of
  the window the Repair button starts as a service) leaves it.
- Verification: TestTheLinuxAppNamesItsDesktop fails before, passes after. The real core with
  the real window under Xvfb: on XFCE `pac_refresh` "manual" within a poll, on GNOME "auto"
  again, without a desktop unchanged.

### TR-5 Linux: a newer package with automatic updates off

- Found by: the desktop reviewer. uni-vpn-open copies the package only on the first open, and
  the app entry setup writes starts the user's copy directly; with automatic updates off
  (also synced from another device) a newer .deb or .rpm never reached the program. On Mac
  and Windows installing a newer version does update.
- Root cause: on Linux nothing brought a package's version into the user's copy after the
  first copy, except the automatic updates.
- Fix: with automatic updates off, the core takes the version of its package's app folder when
  it is newer (`updater.FromPackage`: the package's `.commit`, its program's version
  `0.1.<run>` compared with this one's, copied next to the copy, started once as a test, then
  installed when the tunnel is idle like an update, and restarted). The package watcher looks
  again as soon as the package's `.commit` changes. An older package (the copy updated itself
  past it) is never installed.
- Verification: TestANewerPackageIsInstalled, TestAnOlderPackageIsNotInstalled,
  TestAPackageThatIsNotUniVPNIsRefused, TestNewer, TestANewerPackageIsInstalledWithAutomaticUpdatesOff
  and TestAChangedPackageIsLookedAtAtOnce fail before, pass after;
  TestAutomaticUpdatesComeFromTheRelease shows the release stays the source with automatic
  updates on. End to end: a user copy with a core 0.1.50 and automatic updates
  off, its package folder replaced by 0.1.60 while the core ran: at the watcher's next look
  (5 minutes) the core installed it and restarted; status.json said 0.1.60, the copy had the
  new program, window and `.commit`.

### Left as they are (Low), third re-audit

- macOS: polls from another account's window bring the next Cover forward about every 30 s
  (extra networksetup calls, nothing else).
- An enrollment that starts while the SOCKS port is held waits for its timeout if another
  message replaces "port in use" before the port is free.
- Windows: two accounts' services share Program Files and can race on an update's staging;
  each recovers by itself (as GC-12).
- App windows: on Windows the instance file can look missing for a moment while it is
  swapped; on Mac and Windows an XDG_STATE_HOME set by hand (the core follows it, the windows
  do not) turns their instance check off. Quitting the window and opening it again before the
  assistant ends loses its "sign out" step.
- Phones: the APK can lag the desktop files by one release when an android job fails after
  main moved on (each half consistent); on mobile data an update comes down and goes in only
  on a tap (corrected in the fourth re-audit: before FR-1 Wi-Fi did not install it either); a
  stopped tunnel can say "ready" for a moment; on
  iPhone the instance file, which nothing there reads, can be overwritten in a handover.
- Setup while nothing listens on the port, when another account's program takes it in the
  moment before the core starts: the core withdraws the rule as before, but can do so while
  setup's install still runs.
- A web page can make the core write or remove the "restart the browser" record by asking for
  status.json as the Linux window does (a hint text, nothing else).

## Fourth re-audit

Scope: the whole repository at a960754 (#85 in), read by five fresh reviewers in the same five
areas. Their tests passed on that commit: `go test` (also `-race` for the daemon, API,
forwarder, sockowner and tunnel), `go vet` for windows, darwin, android and ios, the relay's
tests, test_i18n, test_release_put and test_messages, go-licenses, the page in headless
Chromium with about 25 browser identities, the real Linux window under Xvfb, TR-5 end to end
with two staged package folders, and the real setup with its own D-Bus and dconf.

| # | Bug | Severity | Status |
|---|---|---|---|
| FR-1 | Android (download page build): an update never goes in without a tap, and the tap offer can be invisible | Medium | fixed |
| FR-2 | While Cisco Secure Client is connected, every listed website fails in the browser | Medium | fixed |
| FR-3 | Setup can take this account's own new core for another account's program and leave the rule off (a regression from TR-1) | Low | fixed |
| FR-4 | Site: a space between the joined Windows tip sentences in Chinese; the English parts of the privacy page and legal notice marked as German | Low | fixed |

### FR-1 Android: updates only after a tap

- Found by: the mobile reviewer. `ApkUpdate.check` downloads a new version on Wi-Fi, but only
  `ApkUpdate.start` (Home's row, the "not running" page, the notification's tap) hands it to
  Android's installer. The service's own comment says most users never open the app again;
  for them the only offer is the text of the VPN's low-importance ongoing notification, which
  Android 13 and later do not show at all when the notification prompt was declined. Such
  phones kept their version for good, security fixes included (M1's problem for this group).
  The third re-audit's Lows list left it as "the update offer on mobile data is only a
  notification (Wi-Fi installs it anyway)", which was wrong: no code installed on Wi-Fi.
- Reproduced: by reading only (no Android toolchain here): every caller of `install` traced;
  no WorkManager, JobScheduler or alarm path.
- Root cause: installing hung off a tap on an offer whose visibility the app does not control.
- Fix: role model Signal's download build. On Android 12 and later, a version that came down
  goes to Android's installer without a tap (`ApkUpdate.installUnattended`) while the VPN is
  on, the app is not on screen (MainActivity onStart/onStop) and the core's tunnel is not in
  use (state not connecting, connected or disconnecting), as the computers install once the
  tunnel is idle. The app updates itself, so with `UPDATE_PACKAGES_WITHOUT_USER_ACTION` and
  `USER_ACTION_NOT_REQUIRED` Android puts it in without asking, and BootReceiver turns the
  VPN back on (MY_PACKAGE_REPLACED). Should Android want to ask after all, the session is
  abandoned and nothing opens (UpdateReceiver, `EXTRA_UNATTENDED`): the offer stays, and the
  next try for that version comes a day later. Mobile data still downloads only on a tap;
  Android 10 and 11 always ask, so there the tap stays the way. Debug builds never update.
  The privacy page (German and English, dated 16 November) and docs/android.md say so.
- Verification: `ApkReleaseTest` (the rule: Android 12+, not on screen, no tunnel in use, once
  a day per version) runs in CI's `testWebsiteDebugUnitTest`; compile and lint in CI's android
  job. The installer's conditions were checked against AOSP's PackageInstallerSession (a
  self-update with the permission needs no user action, at most one every 30 s); not run on a
  device here, and the CI emulator runs a debug build.

### FR-2 Cisco Secure Client connected

- Found by: the core reviewer (sign-in and API). The README suggests the Cisco client for large
  downloads, and users have it for drives or SSH. While it is connected the core is
  `blocked`, but the rule still sends every listed website to the SOCKS port, which refused
  them (SOCKS reply 2): Chrome showed ERR_SOCKS_CONNECTION_FAILED although Cisco carries the
  university's network. Disconnect did not help (the next page load found Cisco again); the
  app said only "Paused while Cisco Secure Client is connected".
- Reproduced: a daemon test with the Cisco check on and one listed website: `Direct()` was
  only the security page, and a SOCKS CONNECT to the website got `05 02`.
- Root cause: `directHosts` let the listed websites through directly only while the gateway
  cannot be reached (Offline, or Idle while away), not while Cisco is connected.
- Fix: also while `blocked`: the core connects the listed websites itself, through Cisco's
  routes, as it does for Offline. Once Cisco is disconnected they go through the tunnel again
  (and the direct connections end once it is up). README and docs/e2e.md say so.
- Verification: TestListedWebsitesGoThroughCiscoWhileItIsConnected (blocked, after
  Disconnect and the next page load, then back through the tunnel) fails before, passes after;
  the other Cisco and direct tests pass.

### FR-3 Setup and its own new core

- Found by: the core reviewer (system parts). Since TR-1 setup asks `OtherAccountListens`
  right after it started the core. On Linux an accepted connection the program has not taken
  yet has no owner in /proc/net/tcp, which `OtherAccount` counts as another account's (right
  for the forwarder's clients), so a core that had bound but not yet accepted looked like
  another account's: setup left the rule off, and the core, holding the port, never switched it
  on until the next start or a Repair.
- Reproduced: the real setup with a fake systemctl that returns 0.2 s after starting the real
  daemon, under 12 busy loops on 4 cores: 8 of 15 setups said another account held the port.
  TestAListenerOfThisAccountIsOursAlsoBeforeItAccepts (a listener that has not accepted) fails.
- Fix: `OtherAccountListens` counts only an end whose account the system names, asking for
  half a second; no name is "cannot tell" (false), as the function says. (Incomplete: it let
  another account's program that does not take connections pass, see FV-1.)
- Verification: the same run 0 of 15; the test passes; TR-1's run with a listener of `nobody`
  still leaves the rule off (mode 'none', marker written, "Another account ... holds").

### FR-4 Site texts

- The TR-3 tip joins two sentences with a space; Chinese has none between sentences. The
  English halves of privacy.html and impressum.html now carry `lang="en"`, so screen readers
  read them as English. Checked in Chromium: zh-Hans and zh-Hant tips without the space, the
  German one unchanged, each heading's language right.

### Left as they are (Low), fourth re-audit

- Linux: a package watcher look in the second while dpkg or rpm replaces the app folder can find the
  new `.commit` with the old program (then the next regular check installs it) or copy a mix
  of old and new files (each half works with the frozen API).
- Mac: a second account with automatic updates off keeps its copy of the core after a newer
  .pkg (postinstall runs setup for the console user only); unchanged from before TR-5.
- The window's old WebKit processes after it restarts itself (left before).
- Android: openconnect runs in its own session, so if only the core child is killed it can
  outlive it until the app process ends (not reproduced). iOS: the TR-4 hook is not excluded,
  but nothing there sends `shell=`.
- Site and docs: README's paragraph on other programs using port 1080 predates FA-9; the zip
  table in docs/go-switch.md; an Android device in desktop mode with a touchpad gets the Linux
  files; the version scheme relies on the run number never resetting; the Linux window's TR-4
  poll goes to whatever holds 1081 (desktop names only); Cloudflare's default NEL reporting is
  not named on the privacy page.
- Core: PortInUse can replace a SettingsBroken message; another program of the same account on
  1081 shows a blank window; one NtQuerySystemInformation per connection on Windows.
- Setup on Xfce while the port is held says "reopen the browser" where the session lines come
  later; reinstateRule removes the marker also when ProxyInstall failed; uninstall can race a
  reinstate.

## Fifth re-audit

Scope: the whole repository at c68ffe4 (#86's first push), five fresh reviewers in the same
five areas.

| # | Bug | Severity | Status |
|---|---|---|---|
| FV-1 | FR-3 let another account's program on the status port pass when it does not take the connection at once: the rule went on, pointing at it (FA-8 again) | High | fixed |
| FV-2 | Android (download page build): FR-1's update without a tap never happens, as Android installs without asking only for an app allowed to install apps | Medium | fixed |
| FV-3 | Mac: before this account is set up, the window shows and trusts another account's uni-vpn, and setup never starts | Medium | fixed |
| FV-4 | Privacy page: says listed websites always go through the university VPN, though they go direct while it cannot be reached or Cisco is connected | Medium | fixed |
| FV-5 | CI never starts the Android APK that goes out (minified by R8), which phones now take without a tap | Medium | fixed |
| FV-6 | Privacy page: does not name the device name and system the VPN server is told | Low | fixed |

### FV-1 Another account's program that does not take connections at once

- Found by: the core reviewer (sign-in and API). Since FR-3 an end the system never names was
  "cannot tell". A program of another account that holds the status port but takes
  connections late or never (a deferred accept is enough) left its end without owner for the
  whole half second, so `portHeld` neither withdrew the rule nor noted the other account, and
  setup switched the rule on: the browsers took their rule from that program.
- Reproduced: TestAListenerThatNeverAcceptsCountsAsAnotherAccounts (a listener that never takes
  the connection) fails on c68ffe4; the real setup with a listener of `nobody` that never
  accepts registered the rule.
- Root cause: FR-3 turned "no owner" from another account's into "cannot tell" for the
  listener's end, to spare this account's own core in the moment before it takes the
  connection.
- Fix: an end that stays without owner counts as another account's again, as everywhere else.
  The moment before this account's own core takes the connection is covered twice: the check
  asks for up to 2 s (the core takes it within milliseconds), and while the status port is the
  core's own, a rule setup left off goes back on at the next tick (no other account can hold
  the port then). Uninstall removes the marker first, so that the rule does not come back while
  it is removed (this also settles the third re-audit's Low "uninstall can race a reinstate").
- Verification: TestAListenerThatNeverAcceptsCountsAsAnotherAccounts,
  TestARuleSetupLeftOffGoesBackOnWhileThePortIsOurs and
  TestUninstallRemovesTheRuleLeftOffMarkerFirst fail before, pass after;
  TestAListenerOfThisAccountIsOursOnceItAccepts passes. The real setup with a listener of
  `nobody` that never accepts: before mode 'auto' with our address, after mode 'none', the
  marker written, "Another account ... holds". FR-3's run (systemctl returning 0.2 s after
  starting the core, 12 busy loops on 4 cores): still 0 of 15 setups say another account.

### FV-2 Android: an update without a tap needs installs from Uni VPN allowed

- Found by: the mobile reviewer. FR-1 hands a downloaded version to Android's installer with
  `setRequireUserAction(USER_ACTION_NOT_REQUIRED)`. Android (AOSP `PackageInstallerSession`,
  `isInstallDisabledForPackage` before the silent branch) installs without asking only when the
  app may install apps at all ("Install unknown apps", `canRequestPackageInstalls`). A phone
  that put the APK in from the browser gave that to the browser, not to Uni VPN, so every
  unattended try came back "pending user action" and was dropped (UpdateReceiver), once a day,
  for good: FR-1's group still kept its version.
- Reproduced: by reading Android 12 to 15's `PackageInstallerSession` (the check for
  installs from unknown sources comes before the one that skips the prompt). Not run on a
  phone: that needs a published newer version signed with the upload key.
- Root cause: FR-1 relied on an Android permission the app is never given without the user's
  tap, and nothing asks for it.
- Fix: until installs are allowed, Home shows "Let Uni VPN update itself" (Android 12 and newer,
  website build, automatic updates on), which opens Android's switch for Uni VPN; the first tap
  on Update allows it too. `installUnattended` does not try before (`mayInstall`, which also
  holds in the Play build, where Android throws). The idle check now also waits for browser
  connections, a sign-in under way and a device being added (`active_connections`, `busy`,
  `sync.pairing`), not only the state, and without an answer from the core it waits a minute
  after the VPN started. docs/android.md and the privacy page say so; strings in all six
  languages.
- Verification: ApkReleaseTest (the rule, and idle with each of the four in use) in CI's unit
  tests; test_i18n and test_messages pass with the new keys; the CI emulator run starts the
  release build (FV-5).

### FV-3 Mac: the window before this account is set up

- Found by: the desktop reviewer. The window trusts what answers on 127.0.0.1:1081 once its
  instance file says so, and without the file (as before the file existed) always. A second
  account on a Mac (fast user switching) that opens Uni VPN before its own setup has no file,
  so the window showed the first account's uni-vpn: its websites and state, with buttons that
  act on it. And since that one answered, the shell's "not running" counter never rose, so
  `setUpIfMissing` (setup for this account) never ran.
- Reproduced: by reading `check` and `startServiceIfDown` on c68ffe4 (no Mac here): no file
  means `guardWindow(true, nil)`, `status` is the other core's, `misses` stays 0.
- Root cause: "no instance file" meant "an install from before the file", which is also what a
  fresh, not yet set up account looks like.
- Fix: without the file the window trusts the port only once this account is set up (its
  LaunchAgent and core exist, the same test `setUpIfMissing` uses); until then it shows nothing
  of it and the menu says "Not running", so setup starts after two polls as on a Mac with one
  account.
- Verification: CI's macos jobs compile the shell (no swiftc here); the paths are the ones
  setup writes (`internal/setup` darwin) and `setUpIfMissing` already checked.

### FV-4 and FV-6 Privacy page: direct connections and the device name

- Found by: the web reviewer. Since FR-2 (and before it, while the VPN server cannot be reached
  or the user is away from the university's network), the app connects the listed websites
  directly from the device, so those websites see the user's own address; the page said they
  go through the university VPN. It also did not name what the Cisco client sends too: the
  device's name and the kind of system (openconnect's `--os`, the user agent).
- Root cause: the page was written before those paths.
- Fix: German and English items for the VPN server now say both.
- Verification: read in Chromium, both languages; the dates say 16 November.

### FV-5 CI starts the release APK

- Found by: the web/CI reviewer. The emulator test installs the debug APK. The APK that goes
  out is minified by R8 and signed, and since FR-1 Android 12 and later put it in without a tap;
  a release that does not start (an R8 rule missing) could not update itself out of that.
- Fix: `packaging/android/emulator-test.sh` then installs `out/uni-vpn.apk` (signed in CI), opens
  it, and fails unless the app and its core run and nothing crashed. A fork without the upload
  key has no signed APK and skips it.
- Verification: `bash -n`; CI's android job on this branch.

### Left as they are (Low), fifth re-audit

- The core reviewer for sign-in and the local API was stopped by Anthropic's safeguards
  partway; FV-1 is from its part. That area gets a fresh reviewer in the sixth re-audit.
- Android: the idle check does not see a website the core serves directly while away from the
  university's network (it is not a tunnel); a tap on Update while an unattended try runs does
  nothing that once; an update can go in while the user scans a code in the system scanner (the
  app is not on screen then).
- Site: no CI job loads `site/index.html` in a browser; README's Cisco row speaks of the
  university's Cisco only; `msg.offline` on a campus whose gateway is not reachable from it;
  docs/e2e.md wording.
- Core: an ESTABLISHED end with inode 0 could count as known on Linux (the core reviewer's
  other way to fix FV-1); not taken, as it trusts a connection nobody has taken yet.

## Sixth re-audit

Scope: the whole repository at 41baeba (#86's third push), five fresh reviewers in the same
five areas; this time the reviewer for the core's connect path and local API finished.

| # | Bug | Severity | Status |
|---|---|---|---|
| SX-1 | Another account's program on the status port can hide from the owner check by keeping its queue of connections full: the rule goes on, pointing at it (FA-8 by another way) | High | fixed |
| SX-2 | Mac with a standard account: setup never sets the rule (macOS refuses it), and the window still says "All set" | Medium | fixed |

### SX-1 Another account's program with a full queue on the status port

- Found by: the core reviewer (system parts). `OtherAccountListens` told the listener's account
  by connecting to it. A program that keeps its accept queue full (`listen(0)` and one
  connection of its own) makes the kernel drop new connections, so the 250 ms connect failed
  and counted as "nothing listens". Setup registered the rule, a core starting at login left
  it on, and once that program served it handed this account's browsers its own rule. It
  could time it: the check ran only at setup and when the core started.
- Reproduced: TestAnotherAccountsListenerThatNeverAccepts (a listener of `nobody` with a full
  queue) fails on 41baeba. The real setup with the reviewer's harness (own D-Bus and dconf,
  `nobody` holding the port with a full queue for 20 s): "Proxy rule registered", mode 'auto',
  and a fetch of the rule's address got the other program's PAC. The login path the same,
  with Chromium fetching that PAC twice.
- Root cause: a listener's account was read from a connection to it, which its program can
  refuse; the system's table names the listening socket's account without one.
- Fix: `OtherAccountListens` first reads the listener of 127.0.0.1:port (or of every address)
  from the system's table: /proc/net/tcp and tcp6 on Linux, `net.inet.tcp.pcblist_n` on macOS,
  `GetExtendedTcpTable` with `TCP_TABLE_OWNER_PID_LISTENER` and the sign-in sessions on
  Windows. The connection is asked only where the table shows no listener or cannot be read.
  This also settles FR-3 and FV-1's timing: this account's own core is named at once, whether
  it took the connection or not. And the core now asks again at each try to take the port,
  until the answer is another account (TestRuleGoesOffWhenAnotherAccountTakesOverTheStatusPort,
  failed before).
- Verification: the new tests fail before and pass after; `go test` and `-race` for sockowner
  and the daemon. The real setup with the full queue: after the fix "Another account ...
  holds", mode 'none', the marker and the other-account page written; the login path the
  same, nothing fetched from the other program. FR-3's run under load (12 busy loops, systemctl
  returning 0.2 s after starting the core): 0 of 15 setups say another account.

### SX-2 Mac: a standard account never gets the rule

- Found by: the desktop reviewer (by reading). Setup runs as the user (the .pkg's postinstall
  starts it for the person signed in). On macOS it sets the rule with `networksetup`, which a
  standard account may not use; setup then logs "Registering the proxy rule with the system
  failed" in setup.log, which nobody reads, and the window says "All set". Repair runs the same
  step. A Mac whose only Uni VPN user has a standard account (a family Mac, a student account
  set up by someone else) never sent a website through the VPN, with no way out by clicks.
- Reproduced: on the CI Mac (macOS 15, run 608 of #86, a step added for it and removed again):
  a standard account `unistd` (not in admin) running `networksetup -setautoproxyurl` gets
  "** Error: Command requires admin privileges." and exit status 14 at once, no dialog; the
  runner, an administrator, sets it with exit status 0. The system rule for it is
  `authenticate-admin-nonshared`.
- Root cause: the rule's commands assumed an administrator's account, which every Mac's first
  account is, but not every Uni VPN user's.
- Fix: when networksetup refuses the account, setup, repair and uninstall (what the user
  started) hand the remaining commands to macOS's own password dialog in one go
  (`osascript ... do shell script ... with administrator privileges`, each word quoted), with
  the reason in the user's language (`setup.admin_network`, six languages): "Uni VPN needs the
  name and password of an administrator of this Mac to change its network settings." Where an
  administrator's account set the rule up already (it is the computer's), a standard account's
  setup keeps it instead of asking for a new address. The core's own looks at the rule (Refresh,
  Cover, Withdraw, putting it back) never ask. README says so.
- Verification: TestMacStandardAccountAsksForAnAdministratorsPassword (also: nobody to ask and a
  cancelled dialog stay "failed"), TestMacStandardAccountKeepsTheRuleThatIsOursAlready,
  TestMacStandardAccountUninstallAsksForAnAdministratorsPassword, TestAdminScriptKeepsEveryWord
  (the script through /bin/sh with a fake networksetup: a service called "Marie's iPhone USB"
  and one with quotes and `$HOME` arrive as they are), TestAskAdminHandsMacOSTheScriptAndTheReason,
  TestAdminPromptInTheMacsLanguage. The dialog itself needs a Mac with someone at it: not run.

### Left as they are (Low), sixth re-audit

- Core: listed websites are refused while Cisco is connected and the core is in a final state
  (password not accepted, passwords locked); its message and button lead on, and then Blocked
  serves them. After Cisco disconnects, Blocked lasts up to 30 s. A stale Cisco pause can show
  "Paused" for up to 30 s after a Disconnect. The back-off is not reset when the loop starts on
  demand. The poll that names the desktop answers with the old `pac_refresh` (the next one is
  right).
- Android: a finished or expired code no longer holds back the update (fixed: only a code that
  is waiting counts), and one failed status request no longer counts as a core that does not
  answer (fixed: a minute since its last answer). The release start check in CI now also fails
  on a core that keeps restarting. Left: it does not run the update path of the release build;
  a crash of any process in that minute fails it; the privacy page names the device name,
  which Android's openconnect sends as "localhost".
- Windows: uninstall deletes the stored passwords of the account that runs it only.
- CI: the `promote` job's comment predates the Go core.
- Mac, standard account: when the websites change, the core cannot give the rule a new address
  (it does not ask for a password on its own), so Safari takes the new list after the next
  sign-in; a cancelled password dialog leaves setup's "failed" in setup.log only, as before.
- Sockowner: on macOS and Linux another account's listener on `::` with IPv6 only also counts
  (the core heals that rule once the port is its own); a ProxyInstall already running while
  uninstall removes the rule can put it back (the marker exists for seconds at most).

## Seventh re-audit

Scope: the whole repository at fd1bf31 (#86's fourth push), five fresh reviewers in the same
five areas (core connect path and local API, core system parts, desktop, phones, site with CI
and texts); all five finished.

| # | Bug | Severity | Status |
|---|---|---|---|
| SV-1 | Mac with a standard account: the core cannot switch the computer's rule off while another account's program holds the status port, so the browsers keep taking their rule from it (FA-8 again, for the accounts SX-2 gave the rule) | High | fixed (the Mac's guard of the proxy rule, chosen by David) |
| SV-2 | Mac with a standard account: a network service added since setup (a dock's Ethernet, an iPhone over USB) never gets the rule, also not from Repair | Medium | fixed |
| SV-3 | Download page: a tablet in desktop mode with 32-bit Android gets the APK button instead of the 32-bit note | Low | fixed |

### SV-1 Mac, standard account: the rule cannot be switched off

- Found by: the desktop, core (system parts) and site reviewers, each on its own. The proxy
  rule on a Mac is the computer's and points at 127.0.0.1:1081, a port any account may take.
  FA-8's answer is that the core switches the rule off while another account holds that port
  (`portHeld`, `sysproxy.Withdraw`). macOS lets only an administrator change the rule (SX-2),
  and the core's own changes never ask for a password, so on a standard account Withdraw is
  refused, `portHeld` stops there and the rule stays on, pointing at the other program. The
  same cause: the core cannot put a rule back that it or setup left off (`reinstateRule`
  removes the marker even so), and where an administrator's Uni VPN switched the rule off for
  another account's, a standard account's Cover cannot switch it on again.
- Reproduced: the reviewer's TestAudit8StandardAccountCannotWithdraw (SX-2's fakes: networksetup
  answers exit 14, "Command requires admin privileges"): Withdraw returns false after
  `-setautoproxystate Wi-Fi off` and the rule stays on with our address. Not run on a Mac.
- Root cause: the design gives every account's browsers one computer-wide rule pointing at a
  port that any account can hold, and expects the account at hand to be able to change that
  rule. A standard account cannot without an administrator. This is the third finding of the
  same class (FA-8, FV-1, SX-1 were the others), so the fix is a design choice: put to David.
- Also by reading, every Mac install: while the Uni VPN user is signed out the rule stays on,
  so a third account's program on 1081 gets the browsers of accounts other than its own.
- Decision (David, 2026-11-24): "Add an admin helper on Macs".
- Fix: the Mac's guard of the proxy rule, design in docs/mac-guard.md. `uni-vpn-core guard`
  runs as root (LaunchDaemon `de.davidvinu.uni-vpn.guard`) and is the only one that switches
  our rule: on for port P only while the account at the screen (owner of /dev/console) asked
  for it and the program listening on 127.0.0.1:P is that account's (the system's table, as
  in SX-1), off otherwise; settings that are not ours are never touched. Accounts tell it over
  a Unix socket (`want P`, `refresh`, `unwant`, `state`, `version`), which names them by
  LOCAL_PEERCRED. sysproxy's Install, Uninstall, Refresh, Cover and Withdraw only tell it where
  it answers. The .pkg installs it; on a Mac set up before, setup and Repair install it with
  one administrator's password, and a standard account asks for Repair (`msg.guard_missing`)
  instead of connecting unguarded. It removes itself 10 minutes after the app went to the
  Trash. The eighth re-audit below found and fixed what was missing in it.
- Verification: internal/guard's tests with a fake Mac (policy for the account at the screen,
  the login window, another account's program, fast user switching, ports, foreign settings,
  the state file, the protocol's limits); sysproxy's guarded paths with a fake guard; setup
  and daemon tests. On a real Mac (CI macos-pkg, run 624 onwards): after the .pkg the guard
  runs as root with `--app` and the rule is on with `?v=`; with the runner's core stopped the
  guard switched the rule off within 2 s (guard log "is=off account=501", then
  `-setautoproxystate Ethernet off`). The full CI result is under EA-5 in the eighth
  re-audit.

### SV-2 Mac, standard account: a service added since setup

- Found by: the desktop reviewer. SX-2's quiet path kept the rule without asking where every
  service with a rule on had ours, so a service with no rule at all (added since) and one whose
  rule another account's Uni VPN switched off counted as done: setup and Repair said "ok" and
  that service never sent a website through the VPN.
- Reproduced: TestMacStandardAccountAsksForAServiceAddedSince (Wi-Fi with our rule, USB LAN
  with none, and with ours switched off) fails before: "ok" with no password asked.
- Root cause: "all ours" counted only the services that had a rule on.
- Fix: `macAllOurs` is true only when every service Apply would set has our rule on.
- Verification: the test passes; the reviewer's own test passes; `go test` for sysproxy and setup.

### SV-3 Download page, 32-bit tablet in desktop mode

- Found by: the site reviewer. The tablet branch of `detect()` skipped the 32-bit check.
- Fix: it makes the same check as the Android branch. Verified in headless Chromium: platform
  "Linux armv8l" with a touch screen gives android32 (android before), "Linux aarch64" android.

### Left as they are (Low), seventh re-audit

- Fixed with this round: docs/android.md says a tapped update goes in without a question once
  installs are allowed (Uni VPN closes) and counts the minute from the core's last answer; the
  Repair row in README names an administrator's password for a standard Mac account;
  docs/macos-test.md D1 expects the second prompt there; "Let Uni VPN update itself" no longer
  shows in debug builds.
- Android: the unattended install checks "not on screen and idle" once, then works for
  seconds; on Android 14 and newer the installer's screen does not open when the user left the
  app before Android answers (the next tap works); the row "Let Uni VPN update itself" stays
  on a phone whose policy forbids installs from unknown sources.
- Core: the keyring messages on step 1 of the assistant say "Try again" where the button is
  "Next"; a gateway that is down or does not resolve shows "No internet"; after Cisco
  disconnects the reconnect can take up to 30 s.
- Docs: docs/e2e.md and docs/macos-test.md use terminal commands (`uni-vpn log`, `doctor`,
  `status`) that only the Python core has; README's "no root after installation" is about the
  tunnel (a standard Mac account's setup, Repair and removal change the network settings as
  root through macOS's dialog).

## Eighth re-audit

Scope: the whole repository at 53a5eae (the SV-1 guard on top of #86 to #88), five fresh
reviewers in the same five areas (core connect path and local API, core system parts with the
guard, desktop and installers, phones, site with CI and texts); all five finished. Phones, the
site and the texts: no Critical, High or Medium. Every finding is in the new guard, its install
or its CI step.

| # | Bug | Severity | Status |
|---|---|---|---|
| EA-1 | Mac: any account can make the guard forget that 127.0.0.1:1081 is ours (16 other ports), so it leaves the rule on for another account's program | High | fixed |
| EA-2 | Mac with two accounts: an account whose core is older than the guard never says "want", so the guard keeps its rule off (for good with automatic updates off) | Medium | fixed |
| EA-3 | Mac: uninstall does not put back another local tool's rule (a proxy app's `http://127.0.0.1:1089/proxy.pac`) that setup replaced | Medium | fixed |
| EA-4 | Mac: an installed guard is never updated, Repair skips one that answers (docs said it updates it) | Medium | fixed |
| EA-5 | CI macos-pkg: the new guard step cannot pass (nobody's program cannot take 1081 right after the runner's core) | Medium | fixed |
| EA-6 | Mac, standard account: with the guard switched off under Login Items the app says "Click Repair" forever and Repair cannot help | Medium | fixed |

### EA-1 Another account's many ports make ours foreign

- Found by: the core reviewer (system parts). The guard's list of our ports was one list of
  16, the newest last, and every account's `want P` with a new port went into it. Sixteen
  wants from any account pushed 1081 out; from then on the guard took
  `http://127.0.0.1:1081/proxy.pac?v=...` for another tool's setting, never switched it off,
  and told the account "held". Saved, so a restart kept it.
- Reproduced: the reviewer's TestAudit9OtherAccountsWantsEvictTheStudentsPort and
  TestOtherAccountsPortsNeverMakeAWantedPortForeign (fails before: Wi-Fi stays on).
- Root cause: a bounded list any account can fill decided what is ours.
- Fix: `ours` counts 1081 and the port of every account's entry always; only old ports come
  from the bounded list.
- Verification: the test passes, also after a restart from the saved file.

### EA-2 An account set up before the guard

- Found by: the core reviewer (connect path) and the desktop reviewer, each on its own. Cores
  before the guard never send `want`; with automatic updates off a Mac copy stays so (the
  fourth re-audit's Low), and postinstall sets up only the account at the screen. That
  account, at the screen with its own core on 1081, got the rule switched off, and its old
  Cover put it back, off again within a minute.
- Reproduced: both reviewers' unit tests with the guard's fakes.
- Root cause: the guard knows only accounts that asked, and some cores cannot ask.
- Fix: `guard install` (the .pkg and Repair) adopts every account with its own Uni VPN service
  (`/Users/*/Library/LaunchAgents/de.davidvinu.uni-vpn.plist`, the folder's and the file's
  owner the same, not root) with the port of its own config.toml (read without following a
  link, the account's own file, at most 64 KB; 1081 otherwise), unless it has an entry.
- Verification: TestAnAccountSetUpBeforeTheGuardIsAdopted, TestPortOfConfig and
  TestServicesInTheHomeFolders (real files with other owners, links refused).

### EA-3 Another local tool's rule on uninstall

- Found by: the core reviewer (system parts). The guarded uninstall left out every backed-up
  address shaped `http://127.0.0.1:N/proxy.pac`, so a proxy app's own rule stayed replaced by
  ours, switched off, and the backup was deleted.
- Reproduced: TestGuardedUninstallPutsBackAnotherToolsLocalRule fails before.
- Fix: `LooksOurs` is only an address with `?v=` (only uni-vpn writes it) or the plain one at
  1081.
- Verification: that test and TestLooksOurs.

### EA-4 The guard is never updated

- Found by: the core (system parts), site and desktop reviewers. The automatic updates replace
  the account's copy only, and ensureGuard returned as soon as any guard answered.
- Root cause: nothing compared the running guard with the core's.
- Fix: the guard answers `version` with `sysproxy.GuardVersion` (raised with every change to
  the guard that must reach every Mac). Setup and Repair install it where it is missing or
  older, now before the service starts, so the new core finds it; a standard account asks for
  Repair until then (Unguarded). The docs say so.
- Verification: TestGuardState, TestEnsureGuardReplacesAMissingOrOlderGuard,
  TestVersionNamesTheGuardsVersion, TestSetupSetsUpTheMacsGuardBeforeTheRule (order).

### EA-5 The CI step

- Found by: the first CI runs and the site reviewer. After the runner's core stopped, macOS
  kept its closed connections on 1081 and refused another account's bind (EADDRINUSE), so the
  step failed before it checked anything; run 625 also bound before the core had let go.
  The guard itself did its part on that run (off within 2 s).
- Fix: the step waits until nothing listens on 1081, nobody's program retries its bind for up
  to 90 s, and the guard must say `off` without the core, `held` with nobody's program, the
  rule must stay off for 8 s, and `on` again with the core.
- Verification: CI runs 631 and 632 (c705532), macos-pkg on a real Mac: the guard runs as root
  with `--app`, the rule is on after the .pkg; with the runner's core stopped the guard set
  `-setautoproxystate Ethernet off` 2.3 s later and said `off`; nobody's program bound 1081
  after 27 s, the guard said `held` and the rule stayed off for 8 s; with the core back it was
  on again 0.15 s after nobody's program ended. Uninstall left the rule off. macos-trash: the
  guard's plist, program and log were gone 10 minutes after the app went to the Trash, and
  `launchctl print` no longer found it.

### EA-6 The guard switched off under Login Items

- Found by: the desktop reviewer, by reading. macOS 13 and newer list the new LaunchDaemon
  under "Allow in the Background"; switched off there, launchd refuses to start it, Repair's
  `guard install` fails, and the standard account's message comes back.
- Fix: the Mac app, when the core says `guard_missing` and macOS says the guard's item needs
  approval (`SMAppService.statusForLegacyPlist`), says so once per launch (`menu.guard_off`,
  `menu.guard_hint` in all six languages) and opens Login Items; README's table names it.
- Verification: by reading against the API, the texts with test_i18n; CI builds the app.

### Fixed with this round (Low)

- The guard reads the settings again at a `want` (setup may just have put our address over
  another setting while the rule is off for this account), at most once a second, else at its
  next look (TestWantReadsTheSettingsAgain); settings it could not read, or a list with no
  service, are read again at the next look (TestUnreadableSettingsAreTriedAgain).
- 32 connections at once and 4 of one account, so one account's silent connections do not shut
  the others out (TestOneAccountCannotTakeEveryConnection).
- Uninstall without a backup, or with a broken one, still says `unwant`.
- Restore on a Mac changes only what differs from the backup, so a standard account whose
  core removes itself after the guard switched ours off is asked for no password
  (TestMacRestoreLeavesWhatIsAsTheBackupHasIt).
- The guard's log goes with it; docs/mac-guard.md, docs/macos-test.md (no second prompt with
  the guard, D4 checks the guard is gone) and README (the tunnel needs no root, the guard does)
  match the code.
- Windows: the tray menu's dark look never came on, as Environment.OSVersion says 6.2.9200 to
  a program without a manifest; the build number comes from the registry now.

### Left as they are (Low), eighth re-audit

- `guard_missing` also stops the listed websites that would open directly (Cisco connected,
  gateway out of reach) until Repair; refusing them is part of asking for Repair once.
- After a cancelled guard dialog, the rule's own dialog can come once more on a standard
  account; a second "Repair" asks again.
- Once the core marked the port as another account's, it does not look again until it
  restarts (the note stays). A slow guard (networksetup holding its lock) can delay a connect.
- Android: an update download that started just before the VPN was switched off still
  installs; on mobile data the next check comes a day later; a failed tapped download shows
  no text.
- French "a besoin encore une fois" is awkward; promote waits only for the Python test jobs
  (stable serves only the terminal installers); the privacy page does not name the guard's
  local files (nothing leaves the Mac).
- Repair runs the account's own core as root for `guard install`: any program of that account
  could show the same macOS dialog itself.

## Ninth re-audit

Scope: the whole repository at c705532 (the eighth round's fixes on top of the guard), five
fresh reviewers in the same five areas. The core's connect path and local API, the phones,
the site with CI and texts: no Critical, High or Medium. Both Mediums are in the guard's
install and in what a removal puts back after the guard has gone.

| # | Bug | Severity | Status |
|---|---|---|---|
| NA-1 | Mac: a removal after the guard has gone puts our own rule back from the backup (on again with nothing to switch it off; a standard account gets a password dialog nobody asked for) | Medium | fixed |
| NA-2 | Mac: `guard install` over a running guard (every .pkg over one with the guard, Repair replacing an older one) bootstraps once right after bootout, with no retry | Medium | fixed |

### NA-1 Our own rule in the backup

- Found by: the desktop reviewer. A Mac's rule is the computer's. The core just started says
  `want` (its start-up Cover), so the guard has switched our rule on before setup takes the
  first backup; a second account's setup finds the first account's rule. Either way the
  backup held `http://127.0.0.1:1081/proxy.pac?v=...`. Only the guarded uninstall skipped
  such entries. Once the guard has gone (the app in the Trash for 10 minutes while this
  account was signed out, or a restart with the guard first), the account's core removes
  itself with the plain Restore, which set our old address again: on an administrator's
  account our rule was on with no Uni VPN left (any program on 1081 then serves every
  account's browsers), on a standard account macOS asked for an administrator's password
  minutes after login for it.
- Reproduced: TestARemovalAfterTheGuardWentPutsBackNoneOfOurRules,
  TestAFirstInstallUnderTheGuardBacksUpNoRule and TestRestoreNeverPutsBackOneOfOurRules
  (from the reviewer's tests) fail before. CI run 632's guard log shows the order on a real
  Mac: the rule went on at 12:43:56, before setup registered it.
- Root cause: the first backup recorded our own rule as if it were the user's setting.
- Fix: the first backup records every one of our rules (ours, any account's with `?v=`, the
  plain one at 1081) as unset, as backUpNew did for services added since; macOS Restore
  treats one of ours in an older backup as unset too, so it only switches it off.
- Verification: the three tests pass; `go test ./...`; the Python suite.

### NA-2 Replacing a running guard

- Found by: the desktop and core (system parts) reviewers, each on its own, by reading. The
  service's own install retries for exactly this ("Bootstrap failed: 5" right after bootout,
  internal/service), the guard's did not: the old guard was gone and the new one not started
  until the next restart. After the .pkg the user's setup then asked for an administrator's
  password a second time; after Repair, the password was given for nothing and a standard
  account kept "Click Repair".
- Fix: `guard install` tries the bootstrap up to five times, a second apart, like the service.
- Verification: macos-pkg now runs `guard install` over the running guard and checks it runs
  again with a new process, says `on` and the rule is on: CI run 633 on bf8c9a9 ("replaced
  while it runs (pid 2680, now 4101)"). The tenth re-audit widened the retries to 25 s.

### Fixed with this round (Low)

- A busy guard (a look's networksetup holds its lock for more than 2 s) reset a request that
  came in time, and the limit of 4 per account waited on the same lock, so one account's
  connections could take every slot. A standard account's "version" could then fail while
  "state" worked, and it was told to Repair a current guard. Connections are counted under
  their own lock and the line is read before the request waits
  (TestABusyGuardAnswersARequestSentInTime, TestABusyGuardKeepsEachAccountsLimit, from the
  reviewer's tests; both fail before).
- The Login Items hint named "Uni VPN", which macOS lists for the menu bar app (on). It now
  names the guard's item as macOS lists an unsigned program, de.davidvinu.uni-vpn.guard, and
  uni-vpn-core, in all six languages and in README's table.
- macos-trash also checks that the guard's folder with its list of accounts is gone.

### Left as they are (Low), ninth re-audit

- A guard that hangs (networksetup stuck) delays a Disconnect as well as a connect, by up to
  its 60 s answer time.
- Any account's `want P` makes `http://127.0.0.1:P/proxy.pac` count as ours, so another local
  tool's rule at exactly that address and port would be switched with ours.
- Uninstall says `unwant` before it stops the service: a Cover in between makes a new entry,
  and a guard set up without the app (get.sh) then never removes itself.
- An adopted core older than the guard: its own Refresh is undone within a minute (Safari
  keeps the old websites until sign-out); an old administrator's core in the background can
  switch its rule on for up to a minute while another account is at the screen. Adoption
  reads only a regular config.toml (1081 otherwise).
- Android: a tapped update and the unattended one can both start while the user is in
  Android's Settings (the update goes in either way); docs/android.md's building notes do not
  name the release start check.
- CI: main's run of #88 was red on a timed-out inprocess test (passed 15 of 15 here, the
  release went out); macos-pkg cannot test two accounts that both want the rule (unit tests
  do); docs/mac-guard.md says "then want" where installGuarded asks before and after.

## Tenth re-audit

Scope: the whole repository at bf8c9a9 (the ninth round's fixes), five fresh reviewers in the
same five areas: the core's connect path and local API; the core's system parts with the Mac
guard; the desktop apps, page and installers; the phones; the site with CI, release scripts,
texts and docs. **No Critical, High or Medium** in any area. Nothing was run on a real Mac by
the reviewers (no Mac here); the guard's darwin code was cross-compiled and read, and CI run
633 on bf8c9a9 ran it on macOS (macos-pkg, macos-trash: green).

### Fixed with this round (Low)

- Mac: the plain Restore (a removal after the guard has gone) switched off any automatic proxy
  on a service the backup had as unset, such as another account's own PAC put back by that
  account's removal; a standard account was asked for an administrator's password for it. An
  unset entry now switches off only one of our rules
  (TestRestoreLeavesAnotherToolsRuleOnAServiceBackedUpAsUnset fails before).
- A resume during the back-off after a failed connection waited out the rest of it (up to
  about 6 minutes, the clock of the wait stops during sleep), with the listed websites failing
  meanwhile. The resume now starts the next attempt at once, as it did for Offline, with the
  back-off from its first step (TestResumeDuringABackOffTriesAgainNow fails before).
- `guard install` over a guard in the middle of a slow networksetup: the old one ends only
  after it (launchd waits up to 20 s), and five tries a second apart could give up. It now
  tries for 25 s.
- docs/mac-guard.md: the core says `want` again with every Cover (every few minutes and after
  a network change), not only at its start. The comment in repair_windows.go and D15 said
  "Repair disconnects first": it does not; Repair is offered only where no tunnel runs.

### Left as they are (Low), tenth re-audit

- Mac guard:
  - When it removes itself, a request accepted just before its socket closed can still switch
    our rule on or write its list of accounts again (a window of milliseconds; the account's
    own removal follows).
  - A guarded uninstall and the guard's Off() see only active network services: one made
    inactive with our rule keeps our address, off or on, until it is used again. Services the
    guard covered are in no backup, so a removal without the guard (switched off under Login
    Items) leaves them.
  - Another account naming 16 ports can push an account's old port (after a hand-edited
    http_port) out of the list, so its old address counts as not ours. Two accounts on
    different ports can bring an administrator's dialog the guard did not need. An http_port
    below 1024 is refused by the guard, and that account works unguarded as before.
  - During a `guard install` (a few seconds) a standard account in the background can be told
    to Repair once; Repair then asks nothing. The same for a single failed `version`.
  - Its log in /Library/Logs is readable by every account (when each account was at the
    screen) and the privacy page does not name it; removed with the app.
  - Whether macOS lists the update zip's core as "uni-vpn-core" in Login Items (its signature
    has no identifier of ours) is not checked on a Mac.
- Core: before setup, a browser's connection shows the missing-password state instead of
  setup (the page still leads to setup). On Linux, putting the rule back after another account
  held the port can replace a proxy setting the user changed meanwhile.
- Android: an update can install without a tap after the screen went off with Uni VPN in front;
  any tap on the "Update Uni VPN" notification starts the update; the tile's
  requestListeningState does nothing (it is redrawn whenever the panel opens).
- Site and CI: a 32-bit Android tablet in desktop mode is told "this phone" (the download is
  right); macos-trash's worst-case waits leave little room before its 30-minute limit.
