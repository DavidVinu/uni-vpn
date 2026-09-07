# End-to-End-Test auf Linux

Voraussetzungen: `install.sh` ist gelaufen, Cisco Secure Client ist getrennt
(`/opt/cisco/secureclient/bin/vpn state` zeigt `Disconnected`).

1. `uni-vpn doctor`: alles `[OK]`, Daemon `idle`.
2. Tunnel ueber curl anstossen und Uni-Adresse pruefen:
   `curl -s --socks5-hostname 127.0.0.1:1080 https://ifconfig.me` liefert eine Adresse aus
   `129.206.0.0/16` oder `147.142.0.0/16`. Dauer des ersten Aufrufs notieren (`time`).
3. `uni-vpn status` zeigt `connected`, `uni-vpn log` enthaelt "Tunnel bereit nach X s".
4. Statusseite `http://127.0.0.1:1081/` zeigt gruen, Knopf "Trennen" funktioniert, Zustand `idle`.
5. Leerlauf: in `config.toml` voruebergehend `idle_minutes = 1` setzen, `uni-vpn service restart`,
   Schritt 2 wiederholen, nach etwa 60 s ohne Verkehr zeigt `uni-vpn status` wieder `idle`.
   Wert zuruecksetzen, Dienst neu starten.
6. Chrome: Extension geladen, `https://sogo.uni-heidelberg.de/SOGo/so/` oeffnen. Popup zeigt
   `connected`. `https://ifconfig.me` im selben Browser zeigt die normale Adresse (DIRECT).
7. Firefox: dasselbe mit temporaer geladenem Add-on; in `about:addons` "In privaten Fenstern
   ausfuehren" erlauben und pruefen, dass das Popup keine fehlenden Freigaben meldet.
8. Falsches Passwort: `uni-vpn password` mit Unsinn, dann Seite laden: Popup zeigt "Anmeldung
   abgelehnt", `uni-vpn log` zeigt genau einen Loginversuch. Richtiges Passwort setzen.
9. Cisco: Cisco-Client verbinden, Seite laden: Popup zeigt "Cisco Secure Client ist verbunden".
   Cisco trennen, Seite neu laden: verbindet von selbst.
10. Suspend/Resume: Laptop 1 Minute zuklappen, oeffnen, Seite laden. `uni-vpn log` zeigt
    "Resume erkannt".

Ergebnisse mit Datum, openconnect-Version und Chrome/Firefox-Version unten eintragen.

## Protokoll

| Datum | Schritt | Ergebnis |
|---|---|---|
| 2026-09-07 | Vorabtest ohne Login (Daemon von Hand mit entpackten Paketen openconnect 9.12, ocproxy 1.60, libsecret-tools 0.21.4; Cisco-Client verbunden) | Daemon startet, Statusseite und `/status.json` antworten, SOCKS-Verbindung fuehrt zu `blocked: Cisco Secure Client ist verbunden`, curl bekommt sofort EOF. POST ohne Header, mit `Origin: null` und GET mit fremdem `Host` liefern 403. `uni-vpn doctor` meldet Ports gebunden, Daemon von Hand gestartet, Keyring ohne Passwort, Cisco verbunden. Log-Datei 0600. |
| 2026-09-07 | CI (GitHub Actions, ubuntu-latest und macos-latest) | 145 Unit-Tests gruen auf beiden, darunter der echte macOS-Keychain-Roundtrip (`security` anlegen, lesen, ueberschreiben, loeschen), `plutil -lint` fuer das Plist, Installer-Trockenlauf, 17 Node-Tests. |
