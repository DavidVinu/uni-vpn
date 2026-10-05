# uni-vpn

Your university's Cisco AnyConnect VPN only for selected websites in the browser. Built at
Heidelberg University, with profiles for other universities. The tunnel comes up on the first
request to a listed domain, password and second factor live in the operating system's
keyring, and after 15 minutes without traffic the tunnel is torn down again. Everything else
on the machine keeps its normal connection.

## Universities

| University | Gateway | Second factor | State |
|---|---|---|---|
| Heidelberg University | vpn-ac.uni-heidelberg.de | TOTP | tested end to end |
| ETH Zurich | sslvpn.ethz.ch | TOTP | profile only |
| University of Bremen | vpn.uni-bremen.de | TOTP | profile only |
| University of Muenster | vpn.uni-muenster.de | TOTP | profile only |
| Harvard FAS Research Computing | vpn.rc.fas.harvard.edu | TOTP | profile only |
| University of Marburg | vpn.uni-marburg.de | TOTP after the password | profile only |
| Stanford University | su-vpn.stanford.edu | Duo push | profile only |
| University of Stuttgart | vpn.tik.uni-stuttgart.de | none | profile only |
| University of Bonn | unibn-vpn.uni-bonn.de | none | profile only |
| University of Mannheim | vpn.uni-mannheim.de | none | profile only |
| University of Kassel | univpn.uni-kassel.de | none | profile only |
| TU Dresden | vpn2.zih.tu-dresden.de | none | profile only |
| Texas A&M University | connect.tamu.edu | Duo push | profile only |
| University of Central Florida | secure.vpn.ucf.edu | Duo push | profile only |
| Ohio State University | vpn.service.osu.edu | Duo push | profile only |
| University of Texas at Austin | vpn.utexas.edu | Duo push | profile only |
| Arizona State University | sslvpn.asu.edu | Duo push | profile only |
| University of Florida | vpn.ufl.edu | Duo push | profile only |
| University of Minnesota | vpn.umn.edu | Duo push | profile only |
| University of Illinois Urbana-Champaign | vpn.illinois.edu | Duo push | profile only |
| University of Georgia | remote.uga.edu | Duo push | profile only |
| University of North Carolina at Chapel Hill | vpn.unc.edu | Duo push | profile only |
| North Carolina State University | vpn.ncsu.edu | Duo push | profile only |
| University of Waterloo | cn-vpn.uwaterloo.ca | Duo push | profile only |
| University of Ottawa | uovpn.uottawa.ca | Microsoft push | profile only |
| Carleton University | cuvpn.carleton.ca | Microsoft push | profile only |
| University of Hong Kong | vpn2fa.hku.hk | Microsoft push | profile only |
| University of Granada | vpn.ugr.es | none | profile only |
| Martin Luther University Halle-Wittenberg | vpn.uni-halle.de | none | profile only |
| Flensburg University of Applied Sciences | vpn.hs-flensburg.de | none | profile only |
| Westphalian University of Applied Sciences | vpn-gateway.w-hs.de | TOTP | profile only |
| Offenburg University of Applied Sciences | vpn.hs-offenburg.de | none | profile only |
| University of Hamburg | vpn.rrz.uni-hamburg.de | none | profile only |
| Friedrich-Alexander University Erlangen-Nuremberg | vpn.fau.de | none | profile only |
| Friedrich Schiller University Jena | vpn.uni-jena.de | none | profile only |
| Bauhaus-Universitaet Weimar | vpngate.uni-weimar.de | none | profile only |
| TH Koeln | vpn.th-koeln.de | none | profile only |
| Technische Hochschule Ulm | vpn.thu.de | none | profile only |
| TU Braunschweig | vpngate.tu-bs.de | none | profile only |
| Freie Universitaet Berlin | vpn.fu-berlin.de | browser sign-in (SAML) | not supported yet |
| University of Oxford | vpn.ox.ac.uk | browser sign-in (SAML) | not supported yet |

"Profile only" means the values come from the university's documentation, for some also from a
look at its login form, but nobody has logged in with uni-vpn yet. If it works for you, or if your
university is missing, open an issue or a pull request against `uni_vpn/universities.json`.
Not listed: choose "Not listed" in the setup assistant and enter the VPN address; uni-vpn reads
the gateway's login form and fills in what it can. Every value can be changed in
`config.toml`, for example:

```toml
university = "ethz"
user = "jdoe"
authgroup = "student-net"
username_suffix = "@student-net.ethz.ch"
```

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
listed domains there and everything else directly. The Uni VPN app shows the state, connects
or disconnects by hand and holds the settings: a small window of its own around the page on
`http://127.0.0.1:1081/` (WKWebView on macOS, WebView2 on Windows, WebKitGTK on Linux), plus an
icon in the menu bar, notification area or panel that starts at login. The installer builds
it on the machine (`app/`, `uni_vpn/desktop.py`).

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

The assistant then asks one thing per step: your university, user name and password, the TOTP
secret if your university uses one (see below, with a live check code), done. Restart open browsers once afterwards.
Without a desktop (SSH), or with `--no-gui` (`-NoGui` on Windows), the same questions come
in the terminal.

At Heidelberg, `https://sogo.uni-heidelberg.de` and `https://elearning-med.uni-heidelberg.de`
now go through the university, along with `cip.dmed.uni-heidelberg.de`, which elearning-med
embeds for its statistics, and the heiCO login at `heico.uni-heidelberg.de`. Everything else does not. For other universities the list starts
empty. Domains go under Settings, Websites.

Without a desktop, `--university ID` (`-University ID` on Windows) picks the university, for
example `./install.sh --university ethz`.

### Second factor

Universities with a time-based one-time password (TOTP): so that uni-vpn can connect without
asking, the machine gets a token of its own, like a second phone. Add one in your
university's MFA portal and paste its secret (the `otpauth://` line) into the assistant. With
Duo push, confirm the request on the phone; without a second factor there is nothing to do.

At Heidelberg the URZ requires TOTP for VPN login and describes the same for KeePassXC. The
app on your phone stays as it is.

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

Open "Uni VPN" from the start menu, Launchpad or app grid, or click the shield icon in the
menu bar (macOS), notification area (Windows) or panel (Linux). The icon shows whether the VPN
is connected; its menu connects, disconnects and opens Settings. Closing the window keeps the
icon. The `uni-vpn` command does the same in a terminal (Windows: new terminals only); `uni-vpn
app` opens the window. Without the app (no Swift compiler on macOS, no WebKitGTK on Linux) the
entry opens `http://127.0.0.1:1081/` in the browser instead.

| What | How |
|---|---|
| Status, connect, disconnect | the app, or `uni-vpn status`, `uni-vpn connect`, `uni-vpn disconnect` |
| Change university, username, password or one-time code secret | the app, Settings (gear icon or the icon's menu), Account |
| Change websites | the app, gear icon, Websites (Chrome picks it up at once, Firefox after about 10 s; on macOS and Windows restart the browser) |
| Something is wrong | `uni-vpn doctor`, paste the output into an issue |
| Update | nothing to do, uni-vpn updates itself (see below) |
| Remove | `uni-vpn uninstall` (Linux, macOS) or, on Windows, `install.ps1 -Uninstall` from `%ProgramFiles%\uni-vpn` (it asks for administrator rights); both also reset the proxy setting |

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| Browser: `ERR_PROXY_CONNECTION_FAILED` or "proxy refused the connection" | service not running, or tunnel in an error state | `uni-vpn doctor`, then `uni-vpn service start` |
| A university page loads without the VPN, for example the university login page instead of the content | the browser has not read the proxy rule | restart the browser; `uni-vpn doctor` shows whether the rule is registered with the system |
| App: login rejected | password wrong or expired | `uni-vpn password`; if it persists, check `uni-vpn log` and open an issue |
| App: one-time code rejected | the machine's clock is off, or the TOTP secret is wrong | turn on automatic time; otherwise create a new token in the MFA portal and run `uni-vpn totp` |
| App: no TOTP secret stored | second factor not set up yet | `uni-vpn totp`, see "Second factor" |
| App: this university signs in through a browser (SAML) | the university uses single sign-on in a browser | not supported yet, use the Cisco client |
| App: login rejected with Duo | the push was denied or not answered in time | connect again and confirm the push |
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
  "uni-vpn"), because Wintun needs administrator rights. For the same reason the program sits in
  `%ProgramFiles%\uni-vpn` and runs only Python and openconnect from Program Files, so nothing
  the user account can change runs elevated. Updates install without asking for a password.
- Updates: uni-vpn keeps itself up to date. A few times a day it asks GitHub whether there
  is a new version. If there is, it installs it while you are not on a university website.
  You notice nothing. To turn this off: open the app, click the gear, switch off "Automatic
  updates".
- Password and TOTP secret live in the GNOME keyring, the macOS keychain or the Windows
  Credential Manager, nowhere else. Setting up a university that is not listed asks its
  gateway for the login form once, without any user data. While connecting, openconnect reads the secret from a
  file readable only by the user, which is deleted immediately afterwards. The machine is
  therefore the second factor, the same way the KeePassXC token documented by the URZ is.
- macOS lists the service under System Settings, General, Login Items.

## Role models for the app

| Role model | Pattern taken |
|---|---|
| Mullvad VPN, Tailscale | main screen with one state, one button, settings behind a gear |
| Tailscale (macOS) | menu bar icon that shows the state, menu with state, Connect/Disconnect, Settings…, Quit; Dock icon only while the window is open |
| Cisco Secure Client, Mullvad VPN (Windows) | notification area icon: left click opens the small fixed window, right click the menu; closing the window keeps the icon; Exit at the bottom |
| macOS System Settings | Account rows open their page, the back arrow returns; Cmd+, opens Settings |
| Google sign-in | one question per card, Next bottom right, Back bottom left, inline errors |
| Google Authenticator | the check code with a countdown ring |
| iOS and macOS Settings | grouped settings list, hints behind info icons |
| Mullvad VPN, Spotify | language follows the system, Settings, Language overrides it, each language in its own name |

## Development

`python3 -m unittest discover -s tests -t . -v` runs without a real VPN, against a fake
openconnect, on Linux, macOS and Windows. Design: `docs/superpowers/specs/2026-09-07-uni-vpn-design.md`,
other universities: `docs/superpowers/specs/2026-10-05-multi-university-design.md`. End-to-end tests:
`docs/e2e.md` (Linux), `docs/macos-test.md`, `docs/windows-test.md`.

Languages: English, German, French, Spanish, Chinese (Simplified and Traditional), one per
university language in the list. The app's text lives in `uni_vpn/locales/<code>.json`,
`en.json` is the source; a new language is a copy of it plus an entry in `uni_vpn/i18n.py`.
Messages from the service stay English in the log and on the command line, the app shows them
translated (`message_t` in `status.json`, `error_t` in API errors). The app never tells anyone
to run a command; where the terminal names one, its wording lives in `en-cli.json`.

## License

AGPL-3.0-or-later, Copyright (C) 2026 David Vinu.
