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
  again. Repair disconnects first, so openconnect is not running meanwhile.
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
