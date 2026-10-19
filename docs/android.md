# Android app

`app/android` is Uni VPN for Android 10 and newer, built from the mobile plan
(https://claude.ai/artifact/7akPD8xja1PJfRFeHG8CGS). Like the desktop apps it is a thin shell:
the Go core does the work and the app only talks to its local API.

## How it works

- **Core.** `cmd/uni-vpn` built for Android (`libunivpn.so`) runs as a child process of the app,
  started with `daemon`. Android only runs programs from an app's native library folder, so the
  core, `openconnect` and `ocproxy` ship there as `lib*.so` files (`jniLibs`, extracted at
  install). The core finds them through `UNI_VPN_NATIVE_DIR` (`internal/platform`) and starts
  ocproxy straight from openconnect's `--script`, since there is no wrapper script on a phone.
- **openconnect.** `packaging/android/build-native.sh` cross-builds openconnect 9.21 with OpenSSL,
  libxml2, and ocproxy 1.60 with libevent, statically, from pinned and checksummed sources.
  openconnect's `/bin/sh` becomes `/system/bin/sh`, and its OpenSSL accepts gateways without
  secure renegotiation, as the computers' GnuTLS does. Its OpenSSL cannot read Android's
  certificate folder, so the app writes the phone's trusted certificates to `ca.pem` and the core
  passes `--cafile`. On Android the core sends `--os=linux-64` unless the profile says otherwise,
  to match the desktop user agents.
- **Only university websites.** The VPN tunnels nothing: it has one unused address and route
  (TEST-NET-1), keeps both address families open, and carries the core's proxy rule
  (`VpnService.Builder.setHttpProxy` with `proxy.pac`). Browsers send the listed websites to the
  core's SOCKS port, the core opens the tunnel on demand, everything else and the phone's DNS stay
  as they are. The app itself is outside the VPN. A changed rule re-establishes the VPN with a new
  `?v=` (a hash of the rule) in its address so browsers reload it.
- **Local API.** Every app on a phone can reach 127.0.0.1, so the core answers only requests
  with the app's token (`X-Uni-VPN-Token`, `UNI_VPN_API_TOKEN`). `proxy.pac` stays open for the
  browsers. Any app can also listen on the core's ports first and collect the token and the
  password: with a token, the core ends (exit code 3) when it cannot have both ports, and
  creates `api-ready` in its state folder once it has them. The app deletes that file before it
  starts the core and sends requests, and hands Android the proxy rule, only while its own core
  runs and the file exists; when the core ended for that, the app says another app is in the way.
- **Known gap: the proxy port.** The SOCKS port (127.0.0.1:1080) is open to every app on the
  phone, as browsers cannot send a token. An installed app could reach the listed university
  websites through the user's tunnel. On a computer that is limited to the user's own programs;
  on a phone each app is its own user. Closing it needs the core to ask who owns each connection
  (`ConnectivityManager.getConnectionOwnerUid`, which only the VPN app may call) and to allow
  browsers only; not done yet.
- **Secrets.** The core keeps the password and the setup key in `secrets.json`, each sealed with
  XChaCha20-Poly1305 (`internal/credentials/filecred.go`). The key comes from the app
  (`UNI_VPN_SECRETS_KEY`), stored wrapped by an AES key in the Android Keystore. Not tied to
  unlocking, so reconnects work in a pocket. Backups and device transfer exclude the app's data.
- **Updates** come from the store; the core's self-updater is off on Android.
- **Later: openconnect in the core's own process.** The iPhone app runs libopenconnect inside
  the core, with a userspace network stack instead of ocproxy (`docs/ios.md`, build tag
  `libopenconnect`). Android could use the same engine, which would drop the two extra programs
  and their child processes; not done yet.

## Screens and their role models

| Screen | Role model | Taken over |
|---|---|---|
| Home | Tailscale | Name and settings gear in the top bar, one switch with the state above everything, the list below (websites instead of devices) |
| Quick Settings tile | Tailscale | One tap on or off; the app opens when the system prompt or setup is needed |
| System prompt | Tailscale | Android's "connection request" right after setup |
| Welcome, Add a device | WhatsApp, Signal linked devices | Scan the code the computer shows; the code under the QR code to type instead |
| University | eduroam CAT | Search by name or city, "Not listed" for a VPN address |
| Sign-in code | Google Authenticator, Microsoft Authenticator | "Scan QR code" with the system scanner (no camera permission), or type the setup key; the live test code. Where the university's MFA portal allows it, one code from the user's app instead ("sign in to add your work account") |
| Settings | Android Settings | Grouped list, the value under each title, info icon for hints |

Texts come from `uni_vpn/locales/<lang>.json` (`/locales.json`); `phone.<key>` replaces a text
that speaks of a computer. The language follows Settings, else the phone's.

## Building

`packaging/android/build-apk.sh COMMIT [GRADLE-TASK...]` with `ANDROID_HOME`, `ANDROID_NDK_HOME`
(NDK r28) and Go. CI (`android` job) builds the release APK and bundle, lint, unit tests, and runs
`app/android/app/src/androidTest` on an Android 14 emulator: the core and openconnect start,
reach Heidelberg's gateway with a made-up account (it must answer "rejected"), the secrets file
holds no clear text, and the VPN carries the proxy rule.

## Publishing (needs David)

1. Create an upload key once, on any computer with Java:
   `keytool -genkeypair -v -keystore upload.jks -alias upload -keyalg RSA -keysize 4096 -validity 10000`.
2. Add four repository secrets: `ANDROID_KEYSTORE_BASE64` (`base64 -w0 upload.jks`),
   `ANDROID_KEYSTORE_PASSWORD`, `ANDROID_KEY_ALIAS` (`upload`), `ANDROID_KEY_PASSWORD`. From then
   on CI signs `uni-vpn.apk` and `uni-vpn.aab`, and main adds `uni-vpn.apk` to the release.
3. Google Play Console (25 USD once): new app "Uni VPN", package `de.davidvinu.univpn`, Play App
   Signing on, upload `uni-vpn.aab` from the CI run's `android-app` artifact. A new personal
   account must run a closed test with 12 testers for 14 days before production.
4. Store listing answers: VPN apps declare the `VpnService` use (the app routes only the
   university websites the user lists, through the user's own university VPN, collects nothing),
   and the privacy policy is the one the download page links once the legal texts are published.

Until then `uni-vpn-debug.apk` from the CI artifact installs for testing (allow the browser or
Files app to install apps when Android asks).

## Not proven yet

On a real phone with a real account: the full sign-in with a second factor, Chrome and other
browsers following the rule, Duo push, a network change while connected, Always-on VPN after a
restart. The emulator test covers starting, reaching the gateway, secrets and the rule.
