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
