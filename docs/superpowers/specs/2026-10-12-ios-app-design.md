# iPhone app design

David asked for the iPhone app on 2026-10-12 ("also iOS"), after the Android app (PR #23). It follows
the mobile plan (https://claude.ai/artifact/7akPD8xja1PJfRFeHG8CGS) and the Android app
(docs/android.md): same Go core, same local API, same screens, same translations.

## What differs from Android

iOS runs no child processes and keeps an app running only while it is on screen. So:

- **The core is a library.** `cmd/uni-vpn-ios` builds the Go core with `-buildmode=c-archive` for
  iOS. It exports `UniVPNStart(configHome, stateHome, apiToken, secretsKey, deviceName) int` and
  `UniVPNStop()`; both run `daemon.New(...).Run()` / `Stop()` like `cmd/uni-vpn daemon`.
- **openconnect is a library too.** libopenconnect (OpenSSL, the SDK's libxml2 and zlib), linked
  statically and driven through cgo (`internal/octunnel`). Its tunnel goes to one end of a
  socketpair (`openconnect_setup_tun_fd`).
- **No ocproxy.** The other end of the socketpair feeds a userspace network stack
  (`golang.zx2c4.com/wireguard/tun/netstack`, gVisor). The core's existing SOCKS server
  (`internal/socks`) dials through it instead of from a source address. `internal/libtunnel`
  puts the three together behind the daemon's `Tunnel` interface, so the daemon does not change.
  The same code builds and runs on Linux (`-tags libopenconnect`), where CI tests it end to end
  against a local ocserv.
- **Where the core runs.** In the Network Extension (`NEPacketTunnelProvider`) while the VPN is
  on, because only the extension keeps running in the background. While the VPN is off and the
  app is on screen, the app runs the core itself, so setup and settings work without a VPN. They
  hand over through the core's lock file in the shared App Group container: the app stops its
  core before it starts the VPN and when it leaves the screen; the extension waits up to 10 s for
  the lock. Both listen on 127.0.0.1:1080/1081; the app talks to whichever runs, with the token.
- **Proxy rule.** The extension sets `NEPacketTunnelNetworkSettings` with one unused address
  (192.0.2.1/32, route 192.0.2.2/32, nothing else routed) and
  `NEProxySettings.autoProxyConfigurationEnabled` with `http://127.0.0.1:1081/proxy.pac?v=<hash>`,
  like Android. A changed website list re-applies the settings.
- **Connect on demand** (iOS's strength): `NEOnDemandRuleEvaluateConnection` with the website
  domains, `connectIfNeeded`, plus a `Connect` rule so the VPN comes back after a restart.
  Turned on by the home switch (switch on = on demand enabled).
- **Secrets.** Same `FileStore` as Android; the 32-byte key lives in the Keychain
  (`kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly`, shared access group of app and extension).
- **Certificates.** OpenSSL on iOS has no CA file, so its own check fails and openconnect asks
  the `validate_peer_cert` callback. On iOS that callback checks the chain
  (`openconnect_get_peer_cert_chain`) with the phone's own trust store (`SecTrustEvaluateWithError`,
  SSL policy for the gateway's host name) and accepts only on success. On Linux it rejects, so
  only the system CA file decides, as with the openconnect program.
- **Memory.** The extension may use about 50 MB. The core sets `debug.SetMemoryLimit(30 MiB)` on
  iOS and the netstack uses small buffers.

## Screens and role models

Same as Android, in SwiftUI: Tailscale iOS (home switch card, VPN prompt at the end of setup),
WhatsApp/Signal linked devices (pairing), eduroam CAT (university search), Google Authenticator
(scan the setup key), iOS Settings (grouped `List`). Scanning uses VisionKit's
`DataScannerViewController` (one camera prompt). A Control Center button
(`ControlWidget`, iOS 18) mirrors the Quick Settings tile.

## Testable without David

- Linux CI: libopenconnect + netstack + SOCKS end to end against ocserv (real data), and
  against Heidelberg's gateway with a made-up account (`auth_failed`).
- macOS CI: native libraries and the core for iOS device and simulator, the Xcode project
  (XcodeGen), unit and simulator tests of the in-app core (API token, `proxy.pac`, setup with a
  made-up account reaching `auth_failed`). The extension cannot run in the simulator.

## Needs David

An Apple Developer account in an organization's name (App Store rule 5.4 for VPN apps; D-U-N-S
number), the Network Extension and App Group capabilities on two app IDs, signing secrets in
GitHub, then TestFlight and App Review. Licence: an App Store exception to the AGPL.
