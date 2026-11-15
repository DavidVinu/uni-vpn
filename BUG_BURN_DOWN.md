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
  (helpers replaced, executable); installer staging differs only by the phone sources.

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
  leave every old file, a lost answer to a delete, a file left over from a failed run.

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
  again after 10 minutes. Checked against ApkUpdate.kt (RETRY = 10 minutes, DAY).
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
