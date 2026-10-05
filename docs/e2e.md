# End-to-end test on Linux

Prerequisites: the installer has run (`./install.sh` or the one-liner
`curl -fsSL https://raw.githubusercontent.com/DavidVinu/uni-vpn/main/get.sh | bash`) and setup is
finished in the browser assistant at http://127.0.0.1:1081/ (university ID and password, then the
TOTP secret with a live check code, then "All set"), so password and TOTP secret are in the keyring.
Cisco Secure Client is disconnected (`/opt/cisco/secureclient/bin/vpn state` shows `Disconnected`).

1. `uni-vpn doctor`: everything `[OK]` including "Second factor", daemon `idle`.
2. Trigger the tunnel via curl and check the university address:
   `curl -s --socks5-hostname 127.0.0.1:1080 https://ifconfig.me` returns an address from
   `129.206.0.0/16` or `147.142.0.0/16`. Note the duration of the first call (`time`).
3. `uni-vpn status` shows `connected`, `uni-vpn log` contains "Tunnel ready after X s".
4. Status page `http://127.0.0.1:1081/` shows green, the "Disconnect" button works, state `idle`.
4a. Right after disconnecting, repeat step 2 (within the same 30 s window as the login):
    `uni-vpn status` briefly shows "Waiting for the next one-time code", then `connected`.
5. Idle: temporarily set `idle_minutes = 1` in `config.toml`, `uni-vpn service restart`,
   repeat step 2, after about 60 s without traffic `uni-vpn status` shows `idle` again.
   Reset the value, restart the service.
6. Chrome (restarted, no proxy setting of its own): open `https://sogo.uni-heidelberg.de/SOGo/so/`,
   status page shows `connected`. `https://ifconfig.me` in the same browser shows the
   normal address (DIRECT). `uni-vpn doctor` shows "Proxy rule: system reads ...".
7. Firefox (restarted, network settings set to "Use system proxy settings"): the same.
   Add a domain on the status page and check that both browsers pick up the new rule without
   a restart.
8. Wrong password: `uni-vpn password` with garbage, then load a page: status page shows
   "Login rejected", `uni-vpn log` shows exactly one login attempt. Set the correct password.
8a. Wrong TOTP secret: `uni-vpn totp` with any valid Base32 value, then load a page: status page
    shows "One-time code rejected", `uni-vpn log` shows "Generating OATH TOTP token code" followed
    by "Login failed.", exactly one openconnect run. Set the correct secret. Afterwards there is no
    `totp-*` file in the state folder (`~/.local/state/uni-vpn/`).
9. Cisco: with the tunnel up, connect the Cisco client, wait up to 30 s: status page shows
   "Cisco Secure Client is connected", loading a page gives a proxy error. Disconnect Cisco,
   wait up to 30 s, reload the page: connects on its own.
10. Suspend/resume: close the laptop lid for 1 minute, open it, load a page. `uni-vpn log` shows
    "Resume detected".

Record results with date, openconnect version and Chrome/Firefox version below.

## Log

| Date | Step | Result |
|---|---|---|
| 2026-09-07 | Pre-test without login (daemon started by hand with unpacked packages openconnect 9.12, ocproxy 1.60, libsecret-tools 0.21.4; Cisco client connected) | Daemon starts, status page and `/status.json` respond, SOCKS connection leads to `blocked: Cisco Secure Client is connected`, curl gets EOF immediately. POST without headers, with `Origin: null`, and GET with a foreign `Host` return 403. `uni-vpn doctor` reports ports bound, daemon started by hand, keyring without password, Cisco connected. Log file 0600. |
| 2026-09-07 | CI (GitHub Actions, ubuntu-latest and macos-latest) | 145 unit tests green on both, including the real macOS Keychain roundtrip (`security` create, read, overwrite, delete), `plutil -lint` for the plist, installer dry run, 17 Node tests. |
| 2026-09-08 | 1 Doctor after `install.sh` (openconnect 9.12-1ubuntu1.24.04.1, ocproxy 1.60, Ubuntu 24.04, university ID bd346) | All lines `[OK]`, including "Second factor: TOTP secret stored". The installer had already connected after the password. |
| 2026-09-08 | First real login | Log: username/password form, then challenge "Please enter second factor (OTP)", "Generating OATH TOTP token code", CSTP connected. Tunnel ready after 1.5 s. Session expiry according to the server after 24 h. State folder afterwards contains only `daemon.log`; the process list shows only the path of the (already deleted) secret file. |
| 2026-09-08 | 2 curl via SOCKS | `https://ifconfig.me` returns 147.142.12.203 (direct: 212.47.181.7). `sogo.uni-heidelberg.de/SOGo/so/` and `elearning-med.uni-heidelberg.de/` respond HTTP 200 in 0.2 s and 0.4 s respectively. |
| 2026-09-08 | 4 Disconnect via API, then on-demand reconnect | `disconnecting` -> `idle` in 1 s (openconnect exit 0 with logout). First SOCKS connection afterwards: tunnel ready after 1.8 s, curl total 6.5 s. Of that, 2.2 s was the Cisco client's `vpn state`; since then Linux only checks `cscotun0`. |
| 2026-09-08 | 4a Reconnect within the same 30 s window | Failed at first: the server rejects the already used one-time code with "Login failed". After the fix: "Waiting for the next one-time code", connection 4 s later, curl total 7.2 s, address 147.142.45.220. |
| 2026-09-08 | 5 Idle (`idle_minutes = 1`) | "Idle for 63 s, tearing down the tunnel", state `idle`. Value reset. |
| 2026-09-08 | 8 Wrong password (test account `e2etest` with garbage, real entries untouched) | Log: "Login failed." before any OTP prompt, form again, "User input required", exactly one openconnect run, state `auth_failed`. The message first named both factors, since the fix "check your password (uni-vpn password)". |
| 2026-09-08 | 8a Wrong TOTP secret | Log: OTP prompt, "Generating OATH TOTP token code", then "Login failed." and the form from the start; no second OTP form, so never "Server is rejecting the soft token". Exactly one run, no `totp-*` file. After the fix the message reads "One-time code rejected ... (uni-vpn totp)". `uni-vpn connect` after the correct secret connects again. |
| 2026-09-08 | Process list | Before the fix, `/bin/sh -c .../uni-vpn-ocproxy <port>` stayed next to ocproxy (dash does not exec the last command). With `--script=exec ...` only openconnect and ocproxy remain. |
| 2026-09-08 | 6 Chrome (Chrome for Testing 152 headless in a throwaway profile, extension via `--load-extension`, controlled via the DevTools protocol; Google Chrome Stable has ignored `--load-extension` since version 137) | Extension service worker loaded, proxy mode `pac_script`, `levelOfControl: controlled_by_this_extension`. `ifconfig.me` before and after the university pages 212.47.181.7 (direct). `sogo.uni-heidelberg.de/SOGo/so/` returns the SOGo login page in 6.3 s, the daemon counts the connection and 2.2 MB. `elearning-med.uni-heidelberg.de/` returns the Moodle start page (12.8 MB in 33 requests, images up to 4.7 MB taking 13 s each). Popup shows "Connected", "Proxy rule active". |
| 2026-09-08 | 6 Finding elearning-med | Page load finished only after 140 s: `cip.dmed.uni-heidelberg.de/matomo2/matomo.js` went DIRECT and ran into `ERR_CONNECTION_TIMED_OUT` (136 s). Host added to the defaults, afterwards load finished after 11.6 s (35 requests, 12.9 MB, largest image 4.7 MB in 11 s). |
| 2026-09-08 | Throughput | 10 MB from speed.cloudflare.com: through the tunnel 455 KB/s, direct 7.4 MB/s (RTT to the VPN server 22 to 113 ms, mean 48 ms). ocproxy: `TCP_WND` 64 KB, `TCP_MSS` 1024, no runtime option. Documented as a limit in the readme. |
| 2026-09-08 | 7 Firefox (Firefox 155 headless in a throwaway profile, temporary add-on via Marionette `Addon:Install`, host permissions granted via `ExtensionPermissions.add`) | After temporary loading the add-on only has `http://127.0.0.1/*` and its own origin; the domain permissions must be granted by the user (as described in the readme), in the test programmatically. Then: `ifconfig.me` before and after the university pages 212.47.181.7 (direct); sogo login page in 11 s including on-demand tunnel setup; elearning-med entirely through the tunnel (13 MB in 28 s); popup shows "Proxy rule active"; in a separate run it shows the daemon state after 1 s and the note "Not active in private windows" (expected, because not granted in the test). Firefox warns twice on load about unknown manifest keys (Chrome-specific entries), without consequences. |
| 2026-09-08 | 7 Outlier | A first Firefox run at 17:53 took 96 s for elearning-med with only 117 KB through the tunnel. Cause according to the daemon log: at exactly that moment the Cisco client connected (step 9), openconnect reported "Network is unreachable" and tried to reconnect for 60 s. After a failed proxy connection attempt Firefox falls back to a direct connection for 10 s (`failoverTimeout` of extension proxies): elearning-med is publicly reachable and loaded directly, `cip.dmed` is not and hung for 90 s. Not a uni-vpn bug, noted in the readme as a Firefox quirk. |
| 2026-09-08 | 9 Cisco in parallel (David, GUI) | Failed at first: Cisco connected while the tunnel was up, uni-vpn kept running unchanged because Cisco was only checked at connection setup. Fix: check every 30 s also in state `connected`, then tear down and `blocked`. Retest pending. |
| 2026-09-08 | Rework: extension dropped, proxy rule as PAC via GNOME | `uni-vpn setup` again: "Proxy rule registered with the system", `gsettings` shows `mode 'auto'` and the PAC URL, doctor "Proxy rule: system reads http://127.0.0.1:1081/proxy.pac". Two bugs found and fixed along the way: `systemctl enable --now` left the old daemon code running (the installer now restarts it); a unit test called `sysproxy.install` without a fake and had already set the real setting earlier, so the backup contained "auto" (the harness now blocks this, backup set to "none" by hand). |
| 2026-09-08 | 6 Google Chrome 152 (headless, throwaway profile, no extra options, reads GNOME) | `ifconfig.me` 212.47.181.7 (direct), sogo login page through the tunnel (10.4 s including on-demand setup), domain `ifconfig.me` added via `/api/domains`: 3 s later ifconfig.me returns 129.206.196.152 (university), after removal 212.47.181.7 again. So Chrome reloads the PAC immediately when the GNOME setting changes. |
| 2026-09-08 | 7 Firefox 155 (headless, fresh profile, no add-on, `network.proxy.type` 5) | sogo through the tunnel (5.3 s), ifconfig.me direct. After a domain change: still direct after 3 s, via the university after 10 s; removal likewise. `network.proxy.socks_remote_dns` is `false`: Firefox resolves locally, noted in the readme. |
| 2026-09-08 | 6/7 in David's browsers (GUI, after restart, proxy from GNOME) | Feedback "Works"; at that moment the daemon shows `connected` with one active connection. |
| 2026-09-08 | Harmless | openconnect reports once "Failed to set vring #0 RX backend: Socket operation on non-socket" (vhost-net attempt with `--script-tun`), without effect. |
