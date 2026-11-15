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
  task with the tray app running and records how fast the tray app starts it again, measures the
  installer's process search, and installs again over the running app, then checks that the
  core of this commit answers.

### D2. Repair can fail because the tray or menu bar app starts the service in the middle of it (all desktops)

- Severity: Medium
- Status: fixed
- Root cause: the shells start a stopped service after two missed polls, without knowing that
  Repair stops and starts it on purpose. On Windows, Repair's setup ends the task, waits until
  Task Scheduler no longer reports it running, then starts it; a tray app that starts it in
  between makes setup fail ("The running service did not stop") before the proxy rule and the
  app are set up again. On macOS a menu bar app that loads the service between Repair's bootout
  and bootstrap makes all five bootstrap attempts fail.
- Fix: the status already says `repairing`. All three shells remember when they last saw it and
  do not start the service within 60 s of that; a service that Repair did not bring back is
  started after that.
- Verification: reproduced with the real Linux shell under Xvfb against a fake core that says
  `repairing` and then goes away: before the fix the shell ran `systemctl --user start` 3.1 s
  later. After it: nothing within 20 s, a start 62.6 s after the core went away, and a service
  that stops outside Repair is still started again within 6 s. The Windows shell compiles with
  mcs as C# 5 (as csc in CI), the macOS shell in CI.

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
- Verification: CI macos-pkg checks the priority of the installed service with `ps -o pri`
  (a background job shows 4). Go tests: the rendered plist is interactive; an older plist is
  rewritten once, keeps its mode, and only on macOS.

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
  processes and its browser process and checks that the page loads again and a new window
  comes up.

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
  the WebView2 folder, 20 s later and again after the browser process ended.

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
