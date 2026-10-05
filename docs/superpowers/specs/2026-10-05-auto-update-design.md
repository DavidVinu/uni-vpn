# Automatic updates

Goal: nobody ever runs `uni-vpn update` by hand, on Linux, macOS or Windows.

## Role models

- **Google Chrome (Google Update / Omaha):** checks in the background every few hours, never
  asks, applies the new version at the next relaunch. Updates come from a release channel
  ("stable") that only gets builds that passed testing.
  Taken: silent periodic check, apply at a moment the user does not notice, a stable channel.
- **Tailscale:** the background service updates itself; one "Automatically update" switch in
  the settings, on by default for new installs; `tailscale update` for an immediate update.
  Taken: the service updates itself (no separate updater process or scheduled task), one
  switch in Settings, the existing `uni-vpn update` command for "now".
- **iOS Settings > Software Update > Automatic Updates:** a single switch row, no text.
  Taken: the row's look in the settings list.

Not taken: VS Code's "Restart to update" prompt. uni-vpn has no window the user keeps open,
and the restart costs nothing while the tunnel is down.

## Channel

CI promotes every green commit on `main` to the branch `stable` (fast-forward only, so an
older run finishing late never moves it back). Clients follow `stable`:

- check: `GET https://api.github.com/repos/DavidVinu/uni-vpn/commits/stable` with
  `Accept: application/vnd.github.sha` returns the commit id as plain text (no JSON, 60
  unauthenticated requests per hour and address, we make one every few hours);
- download: `https://codeload.github.com/DavidVinu/uni-vpn/zip/<commit>`, the exact commit,
  so a push between check and download changes nothing. GitHub writes the commit id into the
  zip comment, which is checked against the requested one.

Rolling everyone back is moving `stable` back by hand (`git push -f origin <good>:stable`):
clients follow whatever `stable` points to, forwards or backwards.

The repository and branch are constants in the code, never read from `config.toml`: on
Windows the service runs elevated and writes to Program Files, so a user-writable setting
must not decide which code it installs.

## Flow in the service

1. 10 minutes after start (not at login, where it would compete with everything else), then
   every 5 hours with up to 30 minutes of jitter: ask for the `stable` commit.
2. Same as the installed one (`<app>/.commit`, or `git rev-parse HEAD` in a git checkout):
   nothing to do. Unknown installed commit (installed before this feature): update once.
3. Wait until the tunnel is down and nothing wants it (no browser connection, no connect
   loop, no explicit connect). The tunnel goes down by itself after 15 idle minutes.
4. Download and unpack next to the program into a staging folder, then a smoke test: the new
   code's Python modules must all import with the current Python. A broken download or a
   failing import changes nothing.
5. Still idle: move the files in place (milliseconds), write `.commit`, stop the service
   loop, and restart on the new code:
   - Linux and macOS: `exec` of the same command line, same process id, so systemd and
     launchd do not notice anything.
   - Windows: Task Scheduler does not restart a task that exits normally, and the service's
     job object kills its children when it ends. So the old process stays as a small
     supervisor that starts the new code as its child and starts it again after the next
     update. Ending the task ends both.

Git checkouts (the old `git clone` install): only when the working tree is clean and the
current commit is an ancestor of `origin/stable`, so a developer's own commits or changes are
never touched. Then `git merge --ff-only`, smoke test, and back with `git reset --keep` if it
fails.

## Settings

`auto_update = true` in `config.toml`, default on, like Chrome and Tailscale. The switch
"Automatic updates" in Settings, About, above Version, writes it. The Version row shows the
version and the short commit.

`uni-vpn update` stays the way to update immediately. Without a git checkout it now fetches
`stable` too, like the service.

## What is out of scope

- A dependency changes (a new OpenConnect, a new Python): the update brings the code only.
  The installers stay the place for system packages.
- The service definition changes (systemd unit, launchd plist, Windows task): the update
  keeps the existing one. When one of these changes in the future, the new code has to
  re-register it at start.
