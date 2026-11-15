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
- Verification: curl of the live pages showed the rewritten addresses before; after the merge the
  live pages are checked again (the server refreshes from main every 10 minutes).

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
