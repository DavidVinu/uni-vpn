# uni-vpn

Heidelberg University's VPN (Cisco AnyConnect) only for selected websites in the browser.
The tunnel comes up on the first request to a listed domain, password and TOTP secret live
in the operating system's keyring, and after 15 minutes without traffic the tunnel is torn
down again. Everything else on the machine keeps its normal connection.

## Platforms

| Platform | State |
|---|---|
| Ubuntu 24.04 (GNOME), Chrome and Firefox | tested end to end with a real login, see `docs/e2e.md` |
| Other Linux (Debian, Fedora, openSUSE, Arch; GNOME or KDE) | installer and unit tests only, no real login yet |
| macOS 14+ (Apple Silicon and Intel) | CI only (unit tests, keychain, installer dry run), checklist in `docs/macos-test.md` |
| Windows 10 and 11 (64-bit, Intel or AMD) | CI only (unit tests, Credential Manager, registry, Task Scheduler, installer dry run), checklist in `docs/windows-test.md` |

## How it works

A small Python daemon (standard library only) runs as a background service and offers the
VPN as a SOCKS5 proxy on `127.0.0.1:1080`. A PAC rule in the system proxy settings sends the
listed domains there and everything else directly. The app at `http://127.0.0.1:1081/`
shows the state, connects or disconnects by hand and holds the settings.

- Linux and macOS: `openconnect --script-tun` with `ocproxy`, so the VPN never becomes a
  network interface. No root, no routes, no DNS changes.
- Windows: openconnect has no `--script-tun` there, so it uses a Wintun adapter (needs
  administrator rights). The adapter gets an address but no gateway and no DNS, and its
  default route has metric 9000, so Windows itself never picks it. Only the daemon's own
  SOCKS server binds its connections to the tunnel address and asks the university DNS.

## Installation

One line in a terminal. It installs what is missing, then opens the setup assistant in the
browser.

Linux and macOS:

```
curl -fsSL https://raw.githubusercontent.com/DavidVinu/uni-vpn/main/get.sh | bash
```

Windows (PowerShell, asks for administrator rights once):

```
irm https://raw.githubusercontent.com/DavidVinu/uni-vpn/main/get.ps1 | iex
```

Or download the repository and double-click `install.cmd` (Windows) or run `./install.sh`
(Linux, macOS).

What the installer adds when missing:

- Linux: `openconnect`, `ocproxy`, `secret-tool` and Python 3.11+ through apt, dnf, zypper
  or pacman (sudo asks for your password). On Arch, `ocproxy` comes from the AUR.
- macOS: Homebrew (it asks first), then `openconnect`, `ocproxy` and Python.
- Windows: Python 3.12 (winget, or the signed python.org installer) and OpenConnect with
  Wintun (the OpenConnect-GUI 1.6.2 installer, checked against its SHA-256).

The assistant then asks for three things, one per step: university ID and password, the TOTP
secret (see below, with a live check code), done. Restart open browsers once afterwards.
Without a desktop (SSH), or with `--no-gui` (`-NoGui` on Windows), the same questions come
in the terminal.

`https://sogo.uni-heidelberg.de` and `https://elearning-med.uni-heidelberg.de` now go
through the university, along with `cip.dmed.uni-heidelberg.de`, which elearning-med embeds
for its statistics. Everything else does not. Further domains go under Settings, Websites.

### Second factor

The URZ requires a time-based one-time password (TOTP) for VPN login. So that uni-vpn can
connect without asking, the machine gets a token of its own, exactly as the URZ describes
for KeePassXC. The app on your phone stays as it is.

1. On the university network, or with the Cisco client connected, open
   https://mfa.uni-heidelberg.de
2. Under "Soft-Token (zeitbasiert)" click "Einrichten", give it a name, then "Weiter".
3. Below the QR code, click "Tokendetails einblenden" and copy the text between `secret=`
   and `&issuer=`, or the whole `otpauth://` line.
4. Paste it into the installer, or later via `uni-vpn totp` or the app. The check
   code it shows is the one-time password for the portal's "Testen" step.

### How the proxy rule reaches the browser

The service serves a rule at `http://127.0.0.1:1081/proxy.pac`: listed domains over
`127.0.0.1:1080`, everything else direct. The installer registers that address as the
system's automatic proxy configuration (GNOME and KDE settings on Linux, network settings on
macOS, Internet Options on Windows). Chrome, Edge and Firefox read it from there. On other
desktops (Xfce and others), enter the address as a PAC URL in the browser's own proxy
settings. `uni-vpn doctor` prints it.

## Usage

The app is `http://127.0.0.1:1081/`, also in the start menu, Launchpad or app grid as
"Uni VPN". The `uni-vpn` command does the same in a terminal (Windows: new terminals only).

| What | How |
|---|---|
| Status, connect, disconnect | the app, or `uni-vpn status`, `uni-vpn connect`, `uni-vpn disconnect` |
| Change university ID, password or TOTP secret | the app, gear icon, or `uni-vpn password`, `uni-vpn totp` |
| Change websites | the app, gear icon, Websites (Chrome picks it up at once, Firefox after about 10 s; on macOS and Windows restart the browser) |
| Something is wrong | `uni-vpn doctor`, paste the output into an issue |
| Update | `uni-vpn update` |
| Remove | `./install.sh --uninstall` or, on Windows, `install.ps1 -Uninstall`; both also reset the proxy setting |

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| Browser: `ERR_PROXY_CONNECTION_FAILED` or "proxy refused the connection" | service not running, or tunnel in an error state | `uni-vpn doctor`, then `uni-vpn service start` |
| A university page loads without the VPN, for example the university login page instead of the content | the browser has not read the proxy rule | restart the browser; `uni-vpn doctor` shows whether the rule is registered with the system |
| App: login rejected | password wrong or expired | `uni-vpn password`; if it persists, check `uni-vpn log` and open an issue |
| App: one-time code rejected | the machine's clock is off, or the TOTP secret is wrong | turn on automatic time; otherwise create a new token in the MFA portal and run `uni-vpn totp` |
| App: no TOTP secret stored | second factor not set up yet | `uni-vpn totp`, see "Second factor" |
| App: waiting for the next one-time code | within 30 s of the last login the same code cannot be reused | nothing to do, it continues by itself |
| App: Cisco Secure Client is connected | the Cisco client is active | disconnect Cisco, uni-vpn then connects on its own |
| App: keyring locked | the keyring was not unlocked after autologin | log out and log in with your password |
| Windows: "Needs administrator rights" | the service was started without elevation | `uni-vpn service restart`; the scheduled task "uni-vpn" runs with highest privileges |
| App: no network or captive portal | the WLAN login page has not been confirmed | open the login page, it continues by itself afterwards |
| The first page after a longer pause does not load | bringing the tunnel up took longer than the browser waits | reload the page |
| The page is there but the tab keeps loading for minutes | the page embeds something from another university host that is not on the list | in the browser's developer tools, under Network, find the host with `ERR_CONNECTION_TIMED_OUT` and add it under Settings, Websites |
| Firefox: a university page loads without the VPN shortly after a hiccup | after a failed proxy attempt Firefox sends requests directly for 10 s | reload the page after a few seconds |
| Firefox: "server not found" for a university host that works in Chrome | Firefox resolves names locally, ahead of the proxy; the host exists only in the university DNS | in `about:config`, set `network.proxy.socks_remote_dns` to `true` |

## What the machine notices

- One background process (`uni-vpn daemon`) with two local ports: 1080 (SOCKS5) and 1081
  (app and proxy rule). Both are reachable only from this machine. Any program on the
  machine could use `127.0.0.1:1080` as a proxy, and that is intended, for example
  `curl --socks5-hostname 127.0.0.1:1080`.
- The system's automatic proxy configuration points at the service. Programs that honour it
  (browsers, some mail clients) send only the listed domains through the university;
  everything else goes direct as before. Command line tools ignore the setting.
- Linux and macOS: no routes, no DNS, no root after installation. The tunnel runs entirely
  in user context and manages roughly 0.5 MB/s per connection (ocproxy, fixed 64 KB
  window). Enough for mail and Moodle; for large downloads the Cisco client is faster.
- Windows: while connected, a network adapter "uni-vpn" exists, with an address but no
  gateway and no DNS. The service runs elevated as the signed-in user (Task Scheduler,
  "uni-vpn"), because Wintun needs administrator rights.
- Password and TOTP secret live in the GNOME keyring, the macOS keychain or the Windows
  Credential Manager, nowhere else. While connecting, openconnect reads the secret from a
  file readable only by the user, which is deleted immediately afterwards. The machine is
  therefore the second factor, the same way the KeePassXC token documented by the URZ is.
- macOS lists the service under System Settings, General, Login Items.

## Role models for the app

| Role model | Pattern taken |
|---|---|
| Mullvad VPN, Tailscale | main screen with one state, one button, settings behind a gear |
| Google sign-in | one question per card, Next bottom right, Back bottom left, inline errors |
| Google Authenticator | the check code with a countdown ring |
| iOS and macOS Settings | grouped settings list, hints behind info icons |

## Development

`python3 -m unittest discover -s tests -t . -v` runs without a real VPN, against a fake
openconnect, on Linux, macOS and Windows. Design: `docs/superpowers/specs/2026-09-07-uni-vpn-design.md`. End-to-end tests:
`docs/e2e.md` (Linux), `docs/macos-test.md`, `docs/windows-test.md`.

## License

AGPL-3.0-or-later, Copyright (C) 2026 David Vinu.
