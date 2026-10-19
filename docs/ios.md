# iPhone app

`app/ios` is Uni VPN for iPhones with iOS 18 and newer, built from the mobile plan
(https://claude.ai/artifact/7akPD8xja1PJfRFeHG8CGS) after the Android app (`docs/android.md`).
The same Go core, the same local API, the same screens and the same translations: the app is a
thin SwiftUI shell around the core.

## How it works

- **The core is a library.** iOS runs no child processes, so `cmd/uni-vpn-ios` builds the core
  with `-buildmode=c-archive`. The app and its tunnel extension call
  `UniVPNStart(configHome, stateHome, apiToken, secretsKey, deviceName, lockWaitMs)`,
  `UniVPNStop()` and `UniVPNRunning()` (`app/ios/Core/UniVPNCore.h`, `Shared/CoreHost.swift`);
  they run the same daemon as `uni-vpn daemon`.
- **openconnect is a library too.** `packaging/ios/build-native.sh` builds libopenconnect 9.21
  and OpenSSL 3.5.9 statically, from the same pinned and checksummed sources as the Android app;
  libxml2 and zlib are the system's. Two small changes to openconnect's source: a call that sets
  the group and turns off web sign-in, as the program's `--authgroup` and `--no-external-auth`
  do, and OpenSSL accepting gateways without secure renegotiation, as the computers' GnuTLS does.
  The core drives libopenconnect through cgo, its tunnel goes
  to a socket pair, the other end feeds a userspace network stack (gVisor), and the core's SOCKS
  server dials through that stack. No ocproxy, no tun device.
- **Where the core runs.** iOS keeps an app running only while it is on screen; only the Network
  Extension (`Tunnel/PacketTunnelProvider.swift`) keeps running in the background. So the
  extension runs the core while the VPN is on, and the app runs it itself while the VPN is off
  and the app is on screen, so setup and settings work without a VPN. Both use the same folders
  in the App Group container and hand over through the core's lock file there: the app stops its
  core before it starts the VPN and when it leaves the screen (during a sign-in the user waits
  for, a Duo push or the one-code second factor, only when iOS's background time runs out), the
  extension waits up to 10 s for the lock. Both listen on 127.0.0.1:1080 and 1081; the app talks
  to whichever runs.
- **Only university websites.** The extension tunnels nothing: one unused address and route
  (TEST-NET-1, 192.0.2.1 and 192.0.2.2), no DNS settings, and the core's proxy rule
  (`NEProxySettings`, `http://127.0.0.1:1081/proxy.pac?v=<hash of the websites>`). Safari and
  other browsers send the listed websites to the core's SOCKS port, the core opens the tunnel on
  demand, everything else stays as it is. A changed website list re-applies the settings with a
  new `?v=` (at once when changed in the app, within 30 s when changed on another device).
- **The switch is connect on demand.** On turns on a configuration with one rule,
  `NEOnDemandRuleConnect`: iOS keeps the VPN up, also after a restart or a network change. Rules
  that wait for the website domains (`NEOnDemandRuleEvaluateConnection`) would add nothing, as
  the core inside idles until a browser asks, and iOS uses only the first rule that matches.
- **Local API.** Every app can reach 127.0.0.1, so the core answers only requests with the
  app's token (`X-Uni-VPN-Token`). `proxy.pac` stays open for the browsers. Any app can also
  listen on the core's ports first and collect the token and the password, as on Android: the
  core ends (`UniVPNStart` returns 3) when it cannot have both ports, and creates `api-ready` in
  its state folder once it has them. It removes the file before it lets go of the lock, so a
  core still ending after the handover never removes the next one's. The app and the extension
  send requests, and the extension hands iOS the proxy rule, only while the file exists; when
  another app is in the way, the app says so.
- **Known gap: the proxy port**, as on Android: the SOCKS port is open to every app on the phone
  while the VPN is on, since browsers cannot send a token.
- **Secrets.** The core keeps the password and the setup key in `secrets.json`, sealed with
  XChaCha20-Poly1305. The key and the API token live in the Keychain
  (`kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly`, shared by app and extension through the
  App Group), so reconnects work in a pocket and nothing goes into backups or to another phone.
- **Certificates.** OpenSSL on iOS has no CA file, so openconnect asks its certificate callback,
  which checks the gateway's chain with the phone's own trust store.
- **Memory.** The extension may use about 50 MB; the core limits itself to 30 MB.
- **Updates** come from the App Store; the core's self-updater is off on iOS.

## Screens and their role models

| Screen | Role model | Taken over |
|---|---|---|
| Home | Tailscale iOS | Large title with the settings gear, one card with the state and the switch above everything, the list below (websites instead of devices) |
| System prompt | Tailscale iOS | iOS's "Add VPN Configurations" right after setup, from the last page's Done |
| Control Center button | Tailscale iOS | One tap on or off (iOS 18); the tap's intent runs in the app's process, which may change the VPN configuration; before setup the app is the way in |
| Welcome, Add a device | WhatsApp, Signal linked devices | Scan the code the computer shows; the code under the QR code to type instead |
| University | eduroam CAT | Search by name or city, "Not listed" for a VPN address |
| Sign-in code | Google Authenticator, Microsoft Authenticator | Where the university allows it, one code from the user's app and the core adds itself in the MFA portal (as on the desktop); else "Scan QR code" with VisionKit's scanner (one camera prompt), or type the setup key; the live test code |
| Settings | iOS Settings | Grouped list, the value on the right, info button for hints |

Texts come from `uni_vpn/locales/<lang>.json` (`/locales.json`); `phone.<key>` replaces a text
that speaks of a computer, the same rules as the Android app (`Shared/Strings.swift`). The
camera prompt is the system's and comes from `phone.camera_use` through `InfoPlist.strings`,
written by `build-core.sh`. The language follows Settings, else the phone's.

## Building

On a Mac with Xcode 26, Go and Homebrew:

```sh
brew install xcodegen pkg-config
packaging/ios/build-core.sh "$(git rev-parse HEAD)"   # openconnect, OpenSSL, the core: app/ios/build
cd app/ios && xcodegen && open UniVPN.xcodeproj
```

`build-core.sh` builds three slices (phone, simulator on Apple Silicon and on Intel) into
`app/ios/build/UniVPNCore.xcframework`, plus the licence texts that Settings shows. The
extension cannot run in the simulator: the VPN needs a phone and a signed build.

CI (`ios` job, macos-26) builds all of it, runs `app/ios/Tests` in the simulator (the core starts
in the app, only the app may use the API, `proxy.pac` is open, a made-up account reaches
Heidelberg's real gateway and is turned down, the secrets file holds no clear text) and archives
an unsigned build for phones (`ios-app` artifact). With the signing secrets it also signs the app
and sends it to TestFlight from main.

## Needs David

1. **Apple Developer account in an organization's name.** App Store rule 5.4: VPN apps must come
   from an organization, not a person. That needs a legal entity and its D-U-N-S number (free
   from Dun & Bradstreet, takes days to weeks), then the Apple Developer Program for
   organizations (99 USD a year). The macOS app uses the same team (`APPLE_TEAM_ID`).
2. **App IDs and capabilities** (Certificates, Identifiers & Profiles): the App Group
   `group.de.davidvinu.univpn`, and three App IDs that all have Network Extensions (Packet
   Tunnel) and App Groups (that group): `de.davidvinu.univpn` (the app),
   `de.davidvinu.univpn.tunnel` (the tunnel extension), `de.davidvinu.univpn.control` (the
   Control Center button). CI's automatic signing can create missing ones; making them by hand
   first avoids surprises.
3. **Signing secrets** in GitHub (Settings, Secrets and variables, Actions):
   - `IOS_CERTIFICATE_P12_BASE64`: an "Apple Distribution" certificate with its private key,
     exported from Keychain Access as .p12, then `base64 -i dist.p12`.
   - `IOS_CERTIFICATE_PASSWORD`: the .p12's password.
   - `APPLE_TEAM_ID`: the team ID (already used by the macOS job).
   - `APP_STORE_CONNECT_API_KEY_BASE64`, `APP_STORE_CONNECT_API_KEY_ID`,
     `APP_STORE_CONNECT_API_ISSUER_ID`: an App Store Connect API key with the App Manager role
     (Users and Access, Integrations), the .p8 file as `base64 -i AuthKey_XXXX.p8`, its key ID
     and the issuer ID. Xcode uses it to fetch the profiles, altool to upload.
   From then on every push to main signs the app and uploads it to TestFlight; the signed
   `UniVPN.ipa` is also in the `ios-app` artifact.
4. **App Store Connect**: a new app "Uni VPN", bundle ID `de.davidvinu.univpn`, primary language
   English, the encryption question (standard encryption only, exempt), the privacy answers (no
   data collected; the password stays on the phone, as the `PrivacyInfo.xcprivacy` of each
   target says) and the privacy policy URL of the download page.
5. **TestFlight**: the first build of each version waits for a short beta review; then add
   testers by e-mail. Real tests need a real university account (see below).
6. **App Review**: VPN apps are reviewed by hand. The notes should say that the app sets up the
   user's own university VPN (Cisco AnyConnect), routes only the university websites the user
   lists, collects nothing, and uses the Network Extension only for that. Reviewers cannot sign
   in to a university: send a screen recording of setup and a connection, and offer a test
   account if a university provides one.
7. **Licence: an App Store exception to the AGPL.** App Store terms add restrictions that the
   AGPL forbids, so as the copyright holder add an additional permission (AGPL section 7) that
   allows distribution through the App Store, for example to `LICENSE` or the README's License
   section. Contributions by others would need their consent. openconnect is LGPL 2.1 and linked
   statically: the app's full source and build scripts are public, which lets anyone relink it;
   OpenSSL (Apache 2.0) and the Go modules (BSD, MIT, Apache) only need their notices, which
   Settings, Open source licenses shows.
8. **The phone's own name in the device list.** Since iOS 16 an app reads only "iPhone" as the
   phone's name unless Apple grants it the entitlement
   `com.apple.developer.device-information.user-assigned-device-name` (requested in the
   developer account). Without it every iPhone shows as "iPhone" in the sync device list. Once
   granted, add it to the App and Tunnel entitlements in `project.yml` (both pass the name to
   the core); before that, signing would fail.

## Not proven yet

Everything that needs a phone and a signed build: the extension starting and taking over the
core, Safari and other browsers following the proxy rule, connect on demand after a restart and
a network change, the Control Center button, the camera scanner, a full sign-in with a real
account and a second factor, the one-code second factor, Duo push, and the memory limit of the
extension. The simulator tests cover the core, libopenconnect and the gateway, the API token and
the secrets.
