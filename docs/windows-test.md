# Windows smoke test (checklist for someone with a Windows PC)

Until this list has been run through once, Windows counts as experimental. CI covers the unit
tests, Credential Manager, the registry proxy setting, Task Scheduler XML, Ctrl+C delivery and
the vpnc script in dry-run mode, but never a real Wintun adapter or a real login.

Note: Windows version and build, CPU, Python version, Chrome, Edge and Firefox versions.

1. Account with administrator rights, no existing `%LOCALAPPDATA%\uni-vpn`. Cisco Secure
   Client disconnected.
2. PowerShell: `irm https://raw.githubusercontent.com/DavidVinu/uni-vpn/main/get.ps1 | iex`
   (or download the repository and double-click `install.cmd`).
   Expected: one UAC prompt, Python and OpenConnect are installed if missing, the elevated
   window ends with `[OK]` lines and "Press Enter to close", then the setup assistant opens
   at `http://127.0.0.1:1081/`. Note every dialog (UAC, SmartScreen, Defender, firewall).
3. Assistant: university ID and password, then TOTP secret (the check code must match the
   phone app or the MFA portal), then "All set".
4. Task Scheduler: task `uni-vpn` exists, runs with highest privileges, state Running, and
   starts `pythonw.exe -I "C:\Program Files\uni-vpn\bin\uni-vpn" daemon`. As the normal
   user, creating a file in `C:\Program Files\uni-vpn` is refused.
   `uni-vpn status` in a new terminal shows `connected` or `idle`.
5. `curl.exe -s --socks5-hostname 127.0.0.1:1080 https://ifconfig.me` returns a university
   address (`129.206.x.x` or `147.142.x.x`). `curl.exe -s https://ifconfig.me` returns the
   normal address.
6. While connected: `Get-NetAdapter uni-vpn` shows the adapter; `Get-NetRoute -InterfaceAlias
   uni-vpn` shows `0.0.0.0/0` with metric 9000; `Get-DnsClientServerAddress -InterfaceAlias
   uni-vpn` shows no servers; `Test-NetConnection example.com` still goes over the normal
   adapter. Normal browsing and Teams or Zoom keep working.
7. Restart Edge or Chrome, open `https://sogo.uni-heidelberg.de`: loads through the university.
   Settings, Network, Proxy shows "Use setup script" with `http://127.0.0.1:1081/proxy.pac`.
8. Firefox with "Use system proxy settings": the same.
9. In the app, Disconnect: the adapter disappears within a few seconds, the log shows a
   clean logout (no "killed").
10. Sleep for one minute, wake up, reload the page: connects again by itself.
11. Reboot, sign in, open a university page: connects by itself, no UAC prompt.
12. `uni-vpn update` in a normal terminal: downloads, restarts the task (note whether it
    needs elevation).
13. Automatic update: in an administrator PowerShell, `Set-Content "$env:ProgramFiles\uni-vpn\.commit" ("0" * 40)`.
    In the app, gear icon, switch "Automatic updates" off and on again (that checks at once).
    Within a minute `uni-vpn log` shows "Updated to ..., restarting" and the app's Version row
    the new commit. `Get-ScheduledTask uni-vpn` stays "Running", Task Manager shows two
    `pythonw.exe` (the old one waits for the new one). No UAC prompt.
14. `uni-vpn service stop`: both `pythonw.exe` are gone.
15. App: the installer ends with a "Uni VPN" window (not a browser tab) with its own taskbar
    button, and a shield in the notification area (maybe behind the ^ arrow). Left click on the
    shield opens the window, right click shows Connect/Disconnect, Settings, Exit. Closing the
    window keeps the shield. Start menu "Uni VPN" brings the window back. Task Manager: "Uni
    VPN.exe" runs without "Elevated". Settings > University: pick another one, sign in, back in
    Settings with the new name. Sign out and in: the shield is back without a window.
16. `install.ps1 -Uninstall` (one UAC prompt): task gone, proxy setting reset,
    `uni-vpn` no longer on the PATH in new terminals, Start menu entry gone. Answer "y" to the
    keyring question: Control Panel, Credential Manager, Windows Credentials then shows no
    `uni-vpn` entries.

Report the result as an issue or pull request with this table filled in:

| Step | Result | Dialogs |
|---|---|---|
