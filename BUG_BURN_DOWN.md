# Bug burn-down

Each area keeps its own section. Severity: Critical (security hole, data loss, or unusable for
most users), High (a main feature broken for many users), Medium (wrong behaviour or a dead end in
a specific but realistic situation), Low (cosmetic or very rare).

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
