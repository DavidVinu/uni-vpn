# macOS smoke test (checklist for someone with a Mac)

Until this list has been run through completely once, macOS counts as experimental.

Record the environment: macOS version, chip, Homebrew version, Chrome and Firefox version.

1. Fresh user account, or at least no existing `~/.config/uni-vpn`.
2. Do not install anything beforehand. If Homebrew is missing, the installer offers to install it
   itself ("Install Homebrew now? It asks for your Mac password."). Note whether it was offered.
3. `curl -fsSL https://raw.githubusercontent.com/DavidVinu/uni-vpn/main/get.sh | bash`
   (or `./install.sh` in a clone).
   Expectation: installs Homebrew if accepted, then openconnect and ocproxy, sets up the service
   and the proxy rule, and opens the browser assistant at http://127.0.0.1:1081/: university ID
   and password, then the TOTP secret with a live check code, then "All set". `uni-vpn doctor`
   afterwards shows `[OK]` for Python, openconnect, ocproxy, service, ports, keyring, second factor, proxy rule.
   Note every dialog macOS shows (Mac password for Homebrew, Command Line Tools, login item,
   firewall, keychain).
4. `uni-vpn status` shows `idle`. `launchctl print gui/$(id -u)/de.davidvinu.uni-vpn` shows
   `state = running`.
5. `curl -s --socks5-hostname 127.0.0.1:1080 https://ifconfig.me` returns a university address.
   If a keychain dialog appears: choose "Always Allow" and note it.
6. Restart Chrome, open `https://sogo.uni-heidelberg.de`, status page `http://127.0.0.1:1081/` shows `connected`; `https://ifconfig.me` shows the normal address. System Settings > Network > Wi-Fi > Details > Proxies: "Automatic proxy configuration" points to `http://127.0.0.1:1081/proxy.pac`.
7. Restart Firefox (Settings > Network: "Use system proxy settings"), the same.
8. Lock the screen, unlock, reload the page: does it work without a dialog?
9. Restart the Mac, open the browser, load a page: does it connect on its own?
10. Automatic update: `echo 0000000000000000000000000000000000000000 > ~/Library/Application\ Support/uni-vpn/app/.commit`.
    In the app, gear icon, switch "Automatic updates" off and on again (that checks at once).
    Within a minute `uni-vpn log` shows "Updated to ..., restarting" and the app's Version row
    the new commit. `launchctl print gui/$(id -u)/de.davidvinu.uni-vpn` shows the same pid as
    before (the service replaced itself in place) and no new "runs" count.
11. App: the installer ends with a "Uni VPN" window (not a browser tab) and a shield in the
    menu bar. Close the window: the Dock icon goes, the shield stays. Shield menu: Connect,
    Disconnect, "Settings…" opens the window at Settings, "Quit Uni VPN" removes the shield.
    Open "Uni VPN" from Launchpad: the window comes back, with a Dock icon. Cmd+, opens
    Settings, Cmd+V pastes into the password field, the "MFA portal" link opens in Safari or
    Chrome. Settings > University: pick another one, sign in, back in Settings with the new
    name. Log out and in: the shield is back without a window.
12. `./install.sh --uninstall` (in the folder the installer ran from; after the one-liner that is
    `~/Library/Application Support/uni-vpn/app`): service gone (`launchctl print` reports an
    error), files gone.

## The download (uni-vpn.pkg)

On a second fresh user account, or after step 12:

D1. Download [uni-vpn.pkg](https://github.com/DavidVinu/uni-vpn/releases/latest/download/uni-vpn.pkg)
    and double-click it. Until it is signed, macOS says it "cannot be opened": System Settings,
    Privacy & Security, "Open Anyway". Note every dialog.
    Expected: the usual installer pages, one password prompt, then the "Uni VPN" window with the
    assistant. Homebrew is not needed and not installed.
D2. Finish the assistant and open a university page in Chrome: it loads through the VPN.
D3. Log out and in: the shield is back in the menu bar.
D4. Drag "Uni VPN" from Applications to the Trash. Within about 10 minutes the shield is gone,
    the proxy setting is off (System Settings, Network, Details, Proxies) and
    `~/Library/LaunchAgents/de.davidvinu.uni-vpn.plist` no longer exists.

Report the result as an issue or pull request with the table filled in:

| Step | Result | Dialogs |
|---|---|---|
