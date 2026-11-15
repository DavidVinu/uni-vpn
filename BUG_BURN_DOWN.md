# Bug burn-down

Each area keeps its own section. Severity: Critical (security hole, data loss, or unusable for
most users), High (a main feature broken for many users), Medium (wrong behaviour or a dead end in
a specific but realistic situation), Low (cosmetic or very rare).

## Mobile apps

Scope: `app/android`, `app/ios`, `packaging/android`, `packaging/ios`, `cmd/uni-vpn-ios` and the
phone-only Go packages (`internal/octunnel`, `internal/netstack`, `internal/libtunnel`).

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
  "Update Uni VPN"; a tap hands the file to `PackageInstaller`.
- **Verification:** unit tests for `uni-vpn-apk.json` and the digest (`ApkReleaseTest`), an
  emulator test that the signing check accepts this app's own APK and refuses another package
  (`ApkUpdateTest`), CI builds both flavors.
