# uni-vpn in the Microsoft Store

Goal: Windows users install uni-vpn without SmartScreen, Smart App Control or browser warnings,
and without anyone paying for a code signing certificate.

## What happens today (2026-11-28)

Proven by `.github/workflows/warnings-probe.yml` (Windows Server 2025 runner, the file the site
links, Mark of the Web set the way browsers set it):

- `uni-vpn-setup.exe` carries no Authenticode signature.
- A double-click shows "Windows protected your PC: Microsoft Defender SmartScreen prevented an
  unrecognized app from starting". The user has to click "More info", then "Run anyway".
- Microsoft Defender's scan finds nothing.

From Microsoft's documentation, not reproduced (the runners have Smart App Control off):
Smart App Control on a freshly installed Windows 11 blocks unsigned programs with no way around
it. Every release changes the file, so SmartScreen reputation of an unsigned file never builds up.

## Store routes

| Route | Who signs | Fits uni-vpn today |
|---|---|---|
| EXE or MSI submission | we must, with a certificate from a Trusted Root Program CA (policy 10.2.9) | yes, but needs the certificate the Store was meant to avoid |
| MSIX | Microsoft, free, on submission | only without administrator rights: `allowElevation` and packaged services are restricted capabilities Microsoft rarely approves |

Partner Center accounts are free for individuals since September 2025 (ID document and selfie).
Azure Artifact Signing is open to individuals only in the US and Canada (Nov 2026).

So the free route is MSIX, and MSIX needs a Windows core that runs as the signed-in user.

## Design

### 1. Tunnel without Wintun (in-process, as on iOS)

Today: `openconnect.exe` makes a Wintun adapter, which needs administrator rights, hence the
elevated scheduled task and the install in Program Files.

New: the core links libopenconnect (already built for Windows by `build-openconnect.sh`) and
runs `internal/libtunnel`, the in-process tunnel iOS uses: libopenconnect writes IP packets to
one end of a pipe, the gVisor network stack reads the other, the SOCKS server dials through it.
No adapter, no driver, no administrator.

- Windows has no datagram socketpair. libopenconnect's Windows tun code does overlapped
  `ReadFile`/`WriteFile` on a `HANDLE`, so a message-mode named pipe (one message per packet)
  serves as the tun handle. `openconnect_setup_tun_fd` first toggles the TAP media status with an
  ioctl a pipe refuses; `build-openconnect.sh` patches it to skip that for a pipe.
- The command pipe (`openconnect_setup_cmd_pipe`) is a socket on Windows: write with `send`.
- The core for Windows is built with cgo against the MinGW libopenconnect, in the Fedora job
  that builds openconnect.
- Tests: Linux CI runs the Windows core under Wine against a local ocserv (the inprocess e2e);
  the Windows runners check that it loads and reaches the Heidelberg gateway.

### 2. Core as the signed-in user

With no adapter the service needs no elevation: the scheduled task runs with normal rights,
the program lives per user. Setup and updates no longer ask for an administrator. Existing
elevated installs move over through the normal self-update.

### 3. MSIX for the Store

- One package: core, libopenconnect and its DLLs, the app window. Capability `runFullTrust` only.
- The core starts at login through the package's `windows.startupTask`.
- The Store build turns off self-update (the Store updates it), like Android's play flavor.
- CI builds `uni-vpn.msix` on every run and installs it on the runner with a throwaway test
  certificate, like the installer test. Microsoft re-signs it on submission.

### 4. Site

Once the Store listing is live, the Windows button links to it.

## What David does

1. Create a free individual developer account in Partner Center (Microsoft account, ID, selfie).
2. Reserve the app name "Uni VPN".
3. Upload the `uni-vpn.msix` CI built, fill in the listing (screenshots exist) and submit.
   Later submissions can go through the Microsoft Store submission API from CI with an Entra
   app's credentials as secrets.
