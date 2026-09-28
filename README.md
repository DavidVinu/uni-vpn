# uni-vpn

Heidelberg University's VPN (Cisco AnyConnect) only for selected websites in the browser.
The tunnel comes up on the first request to a listed domain, password and TOTP secret live
in the operating system's keyring, and after 15 minutes without traffic the tunnel is torn
down again. Nothing else on the machine is touched: no root, no tun device, no changes to
routes or DNS.

Supported: Ubuntu 24.04 (GNOME) with Google Chrome and Firefox. macOS 14+ on Apple Silicon
is prepared but not yet tested on real hardware, see `docs/macos-test.md`. Anything else may
work, without support.

## How it works

A small Python daemon (standard library only) runs as a user service. It drives
`openconnect` in `--script-tun` mode with `ocproxy`, so the VPN exists only as a SOCKS5
proxy on `127.0.0.1:1080`. A PAC rule in the system proxy settings sends the listed
domains there and everything else directly. A status page on `http://127.0.0.1:1081/`
shows the state and lets you connect or disconnect by hand.

## Installation

Linux:

```
sudo apt install git
git clone https://github.com/DavidVinu/uni-vpn.git ~/uni-vpn
~/uni-vpn/install.sh
```

macOS: install [Homebrew](https://brew.sh) first (admin password, a few minutes), then the
same three lines without `sudo apt`.

The installer asks for your university ID, password and TOTP secret, sets up the background
service, registers the proxy rule with the system, and finishes with a self-diagnosis.
Restart any open browser afterwards.

That is it. `https://sogo.uni-heidelberg.de` and `https://elearning-med.uni-heidelberg.de`
now go through the university, along with `cip.dmed.uni-heidelberg.de`, which elearning-med
embeds for its statistics. Everything else does not. Further domains are listed on the
status page at `http://127.0.0.1:1081/`.

### Second factor

The URZ requires a time-based one-time password (TOTP) for VPN login. So that uni-vpn can
connect without asking, the machine gets a token of its own, exactly as the URZ describes
for KeePassXC. The app on your phone stays as it is.

1. On the university network, or with the Cisco client connected, open
   https://mfa.uni-heidelberg.de
2. Under "Soft-Token (zeitbasiert)" click "Einrichten", give it a name, then "Weiter".
3. Below the QR code, click "Tokendetails einblenden" and copy the text between `secret=`
   and `&issuer=`, or the whole `otpauth://` line.
4. Paste it into the installer, or later via `uni-vpn totp` or the status page. The check
   code it shows is the one-time password for the portal's "Testen" step.

### How the proxy rule reaches the browser

The service serves a rule at `http://127.0.0.1:1081/proxy.pac`: listed domains over
`127.0.0.1:1080`, everything else direct. The installer registers that address as the
system's automatic proxy configuration (Linux: GNOME settings, macOS: network settings).
Chrome and Firefox read it from there. On other desktops (KDE, Xfce), enter the address as
a PAC URL under network or proxy settings in the browser itself. `uni-vpn doctor` prints it.

## Usage

| What | How |
|---|---|
| Status | `http://127.0.0.1:1081/` (bookmark it) or `uni-vpn status` |
| Connect / disconnect | button on the status page |
| Change password | status page, form at the bottom, or `uni-vpn password` |
| Change TOTP secret | status page, second form, or `uni-vpn totp` |
| Change domains | status page, "Domains" field (Chrome picks it up at once, Firefox after about 10 s; on macOS restart the browser), or `~/.config/uni-vpn/domains.txt` followed by a browser restart |
| Something is wrong | `uni-vpn doctor`, paste the output into an issue |
| Update | `uni-vpn update` |
| Remove | `~/uni-vpn/install.sh --uninstall`, which also resets the proxy setting |

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| Browser: `ERR_PROXY_CONNECTION_FAILED` or "proxy refused the connection" | service not running, or tunnel in an error state | `uni-vpn doctor`, then `uni-vpn service start` |
| A university page loads without the VPN, for example the university login page instead of the content | the browser has not read the proxy rule | restart the browser; `uni-vpn doctor` shows whether the rule is registered with the system |
| Status page: login rejected | password wrong or expired | `uni-vpn password`; if it persists, check `uni-vpn log` and open an issue |
| Status page: one-time code rejected | the machine's clock is off, or the TOTP secret is wrong | turn on automatic time; otherwise create a new token in the MFA portal and run `uni-vpn totp` |
| Status page: no TOTP secret stored | second factor not set up yet | `uni-vpn totp`, see "Second factor" |
| Status page: waiting for the next one-time code | within 30 s of the last login the same code cannot be reused | nothing to do, it continues by itself |
| Status page: Cisco Secure Client is connected | the Cisco client is active | disconnect Cisco, uni-vpn then connects on its own |
| Status page: keyring locked | the keyring was not unlocked after autologin | log out and log in with your password |
| Status page: no network or captive portal | the WLAN login page has not been confirmed | open the login page, it continues by itself afterwards |
| The first page after a longer pause does not load | bringing the tunnel up took longer than the browser waits | reload the page |
| The page is there but the tab keeps loading for minutes | the page embeds something from another university host that is not on the list | in the browser's developer tools, under Network, find the host with `ERR_CONNECTION_TIMED_OUT` and add it on the status page |
| Firefox: a university page loads without the VPN shortly after a hiccup | after a failed proxy attempt Firefox sends requests directly for 10 s | reload the page after a few seconds |
| Firefox: "server not found" for a university host that works in Chrome | Firefox resolves names locally, ahead of the proxy; the host exists only in the university DNS | in `about:config`, set `network.proxy.socks_remote_dns` to `true` |

## What the machine notices

- One background process (`uni-vpn daemon`) with two local ports: 1080 (SOCKS5) and 1081
  (status page and proxy rule). Both are reachable only from this machine. Any program on
  the machine could use `127.0.0.1:1080` as a proxy, and that is intended, for example
  `curl --socks5-hostname 127.0.0.1:1080`.
- The system's automatic proxy configuration points at the service. Programs that honour it
  (browsers, some mail clients) send only the listed domains through the university;
  everything else goes direct as before. Command line tools ignore the setting.
- No routes, no DNS, no root after installation. sudo is needed only for `apt install`.
- The tunnel runs entirely in user context and manages roughly 0.5 MB/s per connection
  (ocproxy, fixed 64 KB window). Enough for mail and Moodle; for large downloads the Cisco
  client is faster.
- Password and TOTP secret live in the GNOME keyring or the macOS keychain, nowhere else.
  While connecting, openconnect reads the secret from a file readable only by the user,
  which is deleted immediately afterwards. The machine is therefore the second factor, the
  same way the KeePassXC token documented by the URZ is.
- macOS lists the service under System Settings, General, Login Items.

## Development

`python3 -m unittest discover -s tests -t . -v` runs without a real VPN, against a fake
openconnect. Design: `docs/superpowers/specs/2026-09-07-uni-vpn-design.md`. End-to-end test:
`docs/e2e.md`.

## License

AGPL-3.0-or-later, Copyright (C) 2026 David Vinu.
