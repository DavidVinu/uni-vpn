# macOS-Smoke-Test (Checkliste fuer eine Person mit Mac)

Bis diese Liste einmal komplett durchlaufen wurde, gilt macOS als experimentell.

Umgebung notieren: macOS-Version, Chip, Homebrew-Version, Chrome- und Firefox-Version.

1. Frisches Benutzerkonto oder zumindest kein vorhandenes `~/.config/uni-vpn`.
2. Homebrew nach https://brew.sh installieren.
3. `git clone https://github.com/DavidVinu/uni-vpn.git ~/uni-vpn && ~/uni-vpn/install.sh`
   Erwartung: fragt Uni-ID und Passwort, zeigt `[OK]` fuer Python, openconnect, ocproxy, Dienst,
   Ports, Keyring. Notieren, ob macOS einen Dialog zeigt (Anmeldeobjekt, Firewall, Schluesselbund).
4. `uni-vpn status` zeigt `idle`. `launchctl print gui/$(id -u)/de.davidvinu.uni-vpn` zeigt
   `state = running`.
5. `curl -s --socks5-hostname 127.0.0.1:1080 https://ifconfig.me` liefert eine Uni-Adresse.
   Falls ein Schluesselbund-Dialog erscheint: "Immer erlauben" waehlen und notieren.
6. Chrome: Extension entpackt laden, `https://sogo.uni-heidelberg.de` oeffnen, Popup gruen.
7. Firefox: Add-on temporaer laden, dasselbe.
8. Bildschirm sperren, entsperren, Seite neu laden: geht es ohne Dialog?
9. Neustart des Macs, Browser oeffnen, Seite laden: verbindet von selbst?
10. `~/uni-vpn/install.sh --uninstall`: Dienst weg (`launchctl print` meldet Fehler), Dateien weg.

Ergebnis als Issue oder Pull Request mit ausgefuellter Tabelle:

| Schritt | Ergebnis | Dialoge |
|---|---|---|
