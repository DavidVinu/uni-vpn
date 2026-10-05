# Double-click installers

Goal: the least technical user installs uni-vpn by downloading one file and opening it. No
terminal, no Homebrew, no package manager to understand.

## Role models

- **Zoom and Tailscale (standalone) on macOS:** a `.pkg` downloaded straight from the
  website, Installer's standard pages, the app opens when it is done, everything the app
  needs lives inside its bundle in `/Applications`. Taken as is. Not taken: a `.dmg` around
  the `.pkg` (an extra window and an extra double-click that adds nothing).
- **Signal and Zoom on Windows:** one `.exe`, no wizard pages, one administrator prompt, a
  progress bar, the app opens at the end, an entry in Settings, Apps to remove it. Taken as
  is, built with Inno Setup.
- **Signal, Zoom and Chrome on Linux:** a `.deb` that Ubuntu's App Center (or GNOME Software,
  Mint's installer) opens on double-click, an app grid entry afterwards. Taken, plus an `.rpm`
  for Fedora from the same description. Not taken: Chrome's apt repository (uni-vpn updates
  itself, see below), AppImage (needs "Allow executing" and libfuse2 first) and Flatpak (its
  sandbox blocks the background service and the proxy setting).

## Where the files come from

CI (`.github/workflows/ci.yml`) builds all four files on every push and installs each one on a
fresh runner the way a user would (macOS and Windows: then checks that the service answers;
Windows: also uninstalls). On `main`, once the tests are green, `publish` replaces the files
of the GitHub release `latest`, so these links always give the newest version:

    https://github.com/DavidVinu/uni-vpn/releases/latest/download/uni-vpn.pkg
    https://github.com/DavidVinu/uni-vpn/releases/latest/download/uni-vpn-setup.exe
    https://github.com/DavidVinu/uni-vpn/releases/latest/download/uni-vpn.deb
    https://github.com/DavidVinu/uni-vpn/releases/latest/download/uni-vpn.rpm

## What each one does

All three keep the layout the architecture review asked for, "core + openconnect + app
window", so the Go core replaces the Python files without touching the pipeline.

- **macOS** `Uni VPN.app` in `/Applications` is the app window and menu bar item (app/macos,
  built in CI for both processors), with `Contents/Resources/<arm64|x86_64>/`
  holding openconnect, ocproxy and their libraries (built by Homebrew on a CI runner of that
  processor, then made independent of Homebrew) and, as a stopgap until the Go core, a
  standalone Python. The `postinstall` script runs `uni-vpn-open --setup` as the signed-in
  user: it copies the program to `~/Library/Application Support/uni-vpn/app` and runs the
  setup, which starts the service and opens the assistant. Opening the app later shows uni-vpn,
  or sets it up first for another user of the same Mac.
  Homebrew's GnuTLS looks for certificates in Homebrew's folder; the bundled openconnect gets
  `--cafile=/etc/ssl/cert.pem` (macOS's own certificates) and the bundled Python
  `SSL_CERT_FILE` for the same reason.
- **Linux** installs the program to `/usr/share/uni-vpn/app` and depends on `openconnect`,
  `ocproxy`, `secret-tool` and Python 3.11. The service runs per user (systemd user service),
  which a package cannot set up for each user, so the "Uni VPN" app entry does it the first
  time it is opened, the same way as on macOS.
- **Windows** installs the program and the app window (built in CI, marked `.prebuilt`) to
  `C:\Program Files\uni-vpn`, where `install.ps1` expects it, then runs `install.ps1
  -Unattended` for the signed-in user, which adds OpenConnect if missing, python.org's
  embeddable Python inside the same folder (a regular Python installer does nothing when the
  user already has that version), and runs the setup.

## Removing it

Each system's own way, nothing else to do:

- **macOS:** drag "Uni VPN" to the Trash (Zoom, Signal). **Linux:** remove it in the App
  Center. Neither runs anything of ours, so the service watches the installer's folder
  (`uni_vpn/removal.py`, the folder is recorded in `.package` of the user's copy): gone on
  two checks five minutes apart (a new version being installed is back by then), it restores
  the proxy setting, stops the menu bar or panel icon, deletes its files and ends itself. The
  keyring entries stay, like the data of any app moved to the Trash.
- **Windows:** Settings, Apps, "Uni VPN", Uninstall (Signal, Zoom) runs `install.ps1
  -Uninstall`, then removes the folder.

## Updates

Unchanged from the automatic updates (`uni_vpn/updater.py`): every package ships `.commit`,
and the service then follows `stable` in the folder it runs from (the user's own copy on
macOS and Linux, Program Files on Windows). Installing a newer package over an older one also
works. The bundled programs (openconnect, ocproxy, Python) only change with a new package.

## Signing (needs David's accounts)

The CI steps are in place and switch on by themselves once the secrets exist.

- **Apple Developer Program, 99 USD a year** (also needed for an iPhone app). Without it a
  downloaded `.pkg` is blocked with "cannot be opened"; the user has to find "Open Anyway" in
  System Settings, Privacy & Security. With it: no warning at all.
  Secrets: `MACOS_CERTIFICATES_P12` (Developer ID Application and Developer ID Installer,
  base64), `MACOS_CERTIFICATES_PASSWORD`, `APPLE_ID`, `APPLE_TEAM_ID`, `APPLE_APP_PASSWORD`.
- **Azure Trusted Signing, about 10 USD a month** (where Microsoft offers it to individuals).
  Without it Windows SmartScreen shows "Windows protected your PC" and the user clicks "More
  info", "Run anyway". With it the publisher name shows and the warning goes away once the
  file has some downloads behind it.
  Secrets: `AZURE_TENANT_ID`, `AZURE_CLIENT_ID`, `AZURE_CLIENT_SECRET`,
  `AZURE_SIGNING_ENDPOINT`, `AZURE_SIGNING_ACCOUNT`, `AZURE_SIGNING_PROFILE`.
- Linux packages need no signature to be opened from the file manager.

## Later

- With the Go core: drop the bundled Python, Windows `.msi` (the payload is then plain files,
  no per-user step), signed update manifest instead of `stable` source archives.
- The native app windows (settings thread) go into the same bundle and entries.
