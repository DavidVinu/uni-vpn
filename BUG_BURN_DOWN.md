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
| WC-19 | 32-bit ARM Linux is offered the 64-bit ARM packages | Low | fixed |
| WC-20 | An older run's queued publish can replace a newer one's, so the newest green commit waits for the next push | Low | left: GitHub's concurrency queue keeps one waiting job; nothing is lost, the next green push publishes |
| WC-21 | CI removes the Linux package only after uninstalling, so the "removed in the app store" path runs only in unit tests | Low | left: covered by removal_test.go; an end-to-end run waits two 5-minute checks |
| WC-22 | Windows in Chinese (Hong Kong, Macau) probably gets the installer's few texts in Simplified Chinese | Low | left: unverified, the installer shows almost no text and the app itself picks Traditional |

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
  file. The publish job uses it in the order it had (zips, manifest, installers).
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
