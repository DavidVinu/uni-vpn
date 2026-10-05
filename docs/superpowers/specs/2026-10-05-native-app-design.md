# Uni VPN as a native app, settings any time

## Goal

After setup, anyone can change the configuration (university, username, password, second
factor, websites) without a terminal and without a browser tab. The interface is an app of its
own: a window with a Dock or taskbar presence and an icon in the menu bar, notification area or
panel, like the VPN clients people know.

## Approach

The page on `127.0.0.1:<http_port>` stays the one interface (setup assistant, status, settings).
Each platform gets a small native shell around its own system web view, built on the machine by
`uni-vpn setup` (`uni_vpn/desktop.py`):

| Platform | Shell | Built with | Starts at login via |
|---|---|---|---|
| macOS | `app/macos/main.swift`: NSStatusItem, NSWindow with WKWebView | `swiftc` from the Command Line Tools (Homebrew requires them) | LaunchAgent `de.davidvinu.uni-vpn.app` with `--hidden` |
| Windows | `app/windows/UniVpn.cs`: NotifyIcon, WinForms window with WebView2 | `csc.exe` of the .NET Framework 4.x (in every Windows 10/11), WebView2 SDK from NuGet, pinned by SHA-256 | Startup folder shortcut with `--hidden` |
| Linux | `app/linux/uni-vpn-app.py`: Gtk.Application, WebKitGTK, AyatanaAppIndicator | the distribution's Python with PyGObject (installed by install.sh) | `~/.config/autostart`, only where a panel icon is possible |

Rejected: Chrome/Edge `--app` windows (still the browser, its icon in the Dock), a PWA (David:
no), pywebview (pip dependencies in an otherwise standard-library project, and on macOS the
Dock would show "Python"), Electron or Tauri (a runtime or toolchain per user, release builds).

Fallback: when a shell cannot be built (no Swift compiler, no WebKitGTK, no NuGet download), the
app entry opens the page in the browser, as before.

## Role models

| Where | Role model | Taken | Deviation, and why |
|---|---|---|---|
| Entry after setup | Tailscale, Cisco Secure Client, Mullvad VPN | icon in the menu bar / notification area / panel, always there after login; the app in Launchpad, start menu, app grid | none |
| Icon | Tailscale (macOS), Windows network icon | monochrome shield, filled when connected, outlined otherwise, dimmed when the service is not running; macOS template image (SF Symbol `shield`/`shield.fill`) | none |
| Icon click | macOS: Tailscale (click opens the menu). Windows: Cisco Secure Client, Mullvad (left click opens the window, right click the menu). Linux: AppIndicator (always a menu) | per platform convention | Linux cannot open a window on left click, the panel protocol only knows menus |
| Menu | Tailscale | state line (disabled), Connect/Disconnect, separator, Open Uni VPN, Settings…, separator, Quit Uni VPN (macOS) / Exit (Windows) / Quit (Linux) | none |
| Window | Cisco Secure Client, Mullvad VPN | one small window of fixed size (420 × 660), centered the first time, macOS remembers its place | none |
| Dock | Tailscale (macOS 1.6x), 1Password | Dock icon only while the window is open | none |
| Close | Cisco Secure Client, Tailscale | closing the window keeps the icon; Quit/Exit in the menu ends the app; the VPN service runs on | Linux without a panel icon quits on close, otherwise it could not be reached |
| Shortcuts | macOS standard menus | Cmd+, Settings, Cmd+W close, Cmd+Q quit, Edit menu so that Cmd+C/V work in fields | none |
| Account | macOS System Settings | University and Username are rows with a value and a chevron; a row opens the setup assistant prefilled, back arrow top left returns without saving, success returns to Settings | the assistant asks for the password again: a new account or gateway needs it, and it is tested before saving (Google sign-in pattern already in the assistant) |
| Page in the app | native apps | no page title (the window has one), no browser context menu, no text selection outside fields, links to other sites open in the default browser | none |

## Changing the account

`POST /api/setup` already wrote `config.toml` when one existed. Two gaps are closed: the old
tunnel is disconnected before the new account is saved, and when the university changes, the
profile overrides of the old one (`host`, `authgroup`, ...) are removed from `config.toml`
(`config.remove_keys`), otherwise "Not listed" values would stick to the next university.

## No terminal

A shell that cannot reach the service for two polls starts it again (`launchctl kickstart`,
`schtasks /Run`, `systemctl --user start`), at most once a minute. The page's "not running"
texts no longer name commands.

## Not in this change

Prebuilt, double-click installers (.pkg, .deb, signed .exe): own thread. Automatic updates
replace the page and the service, but rebuild the app only on the next install.
