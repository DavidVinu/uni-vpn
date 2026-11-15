# Bug burn-down

Started 2026-11-15. Each area has its own section, kept by the thread that works on it. Every
entry lists the bug, its severity, status, root cause, fix and how the fix was verified.
Severity: Critical (data loss, security hole, nothing works), High (a main path fails for many
users), Medium (a path fails for some users, or a clear harm), Low (rare, cosmetic or easy to
recover from).

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
    windows-arm-install), on macOS by a loop that loads it as soon as it can (macos-pkg).
    Setup must succeed.
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
  nothing, WebView2 starts one for its error page).

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
  write an earlier version's task (priority 7) again.

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
