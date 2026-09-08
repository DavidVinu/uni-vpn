# End-to-End-Test auf Linux

Voraussetzungen: `install.sh` ist gelaufen (Passwort und TOTP-Schluessel im Keyring), Cisco
Secure Client ist getrennt (`/opt/cisco/secureclient/bin/vpn state` zeigt `Disconnected`).

1. `uni-vpn doctor`: alles `[OK]` einschliesslich "Zweiter Faktor", Daemon `idle`.
2. Tunnel ueber curl anstossen und Uni-Adresse pruefen:
   `curl -s --socks5-hostname 127.0.0.1:1080 https://ifconfig.me` liefert eine Adresse aus
   `129.206.0.0/16` oder `147.142.0.0/16`. Dauer des ersten Aufrufs notieren (`time`).
3. `uni-vpn status` zeigt `connected`, `uni-vpn log` enthaelt "Tunnel bereit nach X s".
4. Statusseite `http://127.0.0.1:1081/` zeigt gruen, Knopf "Trennen" funktioniert, Zustand `idle`.
4a. Direkt nach dem Trennen Schritt 2 wiederholen (im selben 30-s-Fenster wie der Login):
    `uni-vpn status` zeigt kurz "Warte auf den naechsten Einmalcode", danach `connected`.
5. Leerlauf: in `config.toml` voruebergehend `idle_minutes = 1` setzen, `uni-vpn service restart`,
   Schritt 2 wiederholen, nach etwa 60 s ohne Verkehr zeigt `uni-vpn status` wieder `idle`.
   Wert zuruecksetzen, Dienst neu starten.
6. Chrome: Extension geladen, `https://sogo.uni-heidelberg.de/SOGo/so/` oeffnen. Popup zeigt
   `connected`. `https://ifconfig.me` im selben Browser zeigt die normale Adresse (DIRECT).
7. Firefox: dasselbe mit temporaer geladenem Add-on; in `about:addons` "In privaten Fenstern
   ausfuehren" erlauben und pruefen, dass das Popup keine fehlenden Freigaben meldet.
8. Falsches Passwort: `uni-vpn password` mit Unsinn, dann Seite laden: Popup zeigt "Anmeldung
   abgelehnt", `uni-vpn log` zeigt genau einen Loginversuch. Richtiges Passwort setzen.
8a. Falscher TOTP-Schluessel: `uni-vpn totp` mit einem beliebigen gueltigen Base32-Wert, dann
    Seite laden: Popup zeigt "Einmalcode abgelehnt", `uni-vpn log` zeigt "Generating OATH TOTP
    token code" und danach "Login failed.", genau ein openconnect-Lauf. Richtigen Schluessel
    setzen. Im Zustandsordner (`~/.local/state/uni-vpn/`) liegt danach keine `totp-*`-Datei.
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
| 2026-09-08 | 1 Doctor nach `install.sh` (openconnect 9.12-1ubuntu1.24.04.1, ocproxy 1.60, Ubuntu 24.04, Uni-ID bd346) | Alle Zeilen `[OK]`, darunter "Zweiter Faktor: TOTP-Schluessel hinterlegt". Der Installer hatte nach dem Passwort bereits verbunden. |
| 2026-09-08 | Erster echter Login | Log: Formular Benutzername/Passwort, dann Challenge "Bitte zweiten Faktor eingeben (OTP)", "Generating OATH TOTP token code", CSTP connected. Tunnel bereit nach 1,5 s. Session-Ablauf laut Server nach 24 h. Zustandsordner enthaelt danach nur `daemon.log`; in der Prozessliste steht nur der Pfad der (bereits geloeschten) Schluesseldatei. |
| 2026-09-08 | 2 curl ueber SOCKS | `https://ifconfig.me` liefert 147.142.12.203 (direkt: 212.47.181.7). `sogo.uni-heidelberg.de/SOGo/so/` und `elearning-med.uni-heidelberg.de/` antworten HTTP 200 in 0,2 s bzw. 0,4 s. |
| 2026-09-08 | 4 Trennen per API, danach Aufbau bei Bedarf | `disconnecting` -> `idle` in 1 s (openconnect Exit 0 mit Logout). Erste SOCKS-Verbindung danach: Tunnel bereit nach 1,8 s, curl gesamt 6,5 s. Davon 2,2 s `vpn state` des Cisco-Clients; seitdem prueft Linux nur `cscotun0`. |
| 2026-09-08 | 4a Neuaufbau im selben 30-s-Fenster | Zuerst gescheitert: Server lehnt den bereits benutzten Einmalcode mit "Login failed" ab. Nach dem Fix: "Warte auf den naechsten Einmalcode", 4 s spaeter Aufbau, curl gesamt 7,2 s, Adresse 147.142.45.220. |
| 2026-09-08 | 5 Leerlauf (`idle_minutes = 1`) | "Leerlauf seit 63 s, Tunnel wird abgebaut", Zustand `idle`. Wert zurueckgesetzt. |
| 2026-09-08 | 8 Falsches Passwort (Testkonto `e2etest` mit Unsinn, echte Eintraege unberuehrt) | Log: "Login failed." vor jeder OTP-Abfrage, Formular erneut, "User input required", genau ein openconnect-Lauf, Zustand `auth_failed`. Meldung nannte zunaechst beide Faktoren, seit dem Fix "Passwort pruefen (uni-vpn password)". |
| 2026-09-08 | 8a Falscher TOTP-Schluessel | Log: OTP-Abfrage, "Generating OATH TOTP token code", dann "Login failed." und Formular von vorn; kein zweites OTP-Formular, also nie "Server is rejecting the soft token". Genau ein Lauf, keine `totp-*`-Datei. Nach dem Fix lautet die Meldung "Einmalcode abgelehnt ... (uni-vpn totp)". `uni-vpn connect` nach dem richtigen Schluessel verbindet wieder. |
| 2026-09-08 | Prozessliste | Vor dem Fix blieb `/bin/sh -c .../uni-vpn-ocproxy <port>` neben ocproxy stehen (dash fuehrt den letzten Befehl nicht per exec aus). Mit `--script=exec ...` nur noch openconnect und ocproxy. |
| 2026-09-08 | 6 Chrome (Chrome for Testing 152 headless in einem Wegwerf-Profil, Extension per `--load-extension`, Steuerung ueber das DevTools-Protokoll; Google Chrome Stable ignoriert `--load-extension` seit Version 137) | Service Worker der Extension geladen, Proxy-Modus `pac_script`, `levelOfControl: controlled_by_this_extension`. `ifconfig.me` vor und nach den Uni-Seiten 212.47.181.7 (direkt). `sogo.uni-heidelberg.de/SOGo/so/` liefert die SOGo-Anmeldeseite in 6,3 s, der Daemon zaehlt die Verbindung und 2,2 MB. `elearning-med.uni-heidelberg.de/` liefert die Moodle-Startseite (12,8 MB in 33 Requests, Bilder bis 4,7 MB je 13 s). Popup zeigt "Verbunden", "Proxy-Regel aktiv". |
| 2026-09-08 | 6 Befund elearning-med | Ladeende erst nach 140 s: `cip.dmed.uni-heidelberg.de/matomo2/matomo.js` ging DIRECT und lief in `ERR_CONNECTION_TIMED_OUT` (136 s). Host in die Vorbelegung aufgenommen, danach Ladeende nach 11,6 s (35 Requests, 12,9 MB, groesstes Bild 4,7 MB in 11 s). |
| 2026-09-08 | Durchsatz | 10 MB von speed.cloudflare.com: durch den Tunnel 455 KB/s, direkt 7,4 MB/s (RTT zum VPN-Server 22 bis 113 ms, Mittel 48 ms). ocproxy: `TCP_WND` 64 KB, `TCP_MSS` 1024, keine Laufzeitoption. Als Grenze im Readme dokumentiert. |
| 2026-09-08 | 7 Firefox (Firefox 155 headless in einem Wegwerf-Profil, temporaeres Add-on ueber Marionette `Addon:Install`, Host-Freigaben per `ExtensionPermissions.add` erteilt) | Nach dem temporaeren Laden hat das Add-on nur `http://127.0.0.1/*` und den eigenen Origin; die Domain-Freigaben muss der Nutzer erteilen (wie im Readme beschrieben), im Test programmatisch. Danach: `ifconfig.me` vor und nach den Uni-Seiten 212.47.181.7 (direkt); sogo-Anmeldeseite in 11 s inklusive Tunnelaufbau bei Bedarf; elearning-med komplett durch den Tunnel (13 MB in 28 s); Popup zeigt "Proxy-Regel aktiv". Der Statustext im Popup blieb nach 2,5 s bei "wird geladen", siehe naechste Zeile. |
| 2026-09-08 | 7 Ausreisser | Ein erster Firefox-Lauf um 17:53 brauchte fuer elearning-med 96 s bei nur 117 KB durch den Tunnel. Ursache laut Daemon-Log: genau dann verband sich der Cisco-Client (Schritt 9), openconnect meldete "Network is unreachable" und versuchte 60 s lang neu zu verbinden. Firefox weicht nach einem gescheiterten Proxy-Verbindungsversuch fuer 10 s (`failoverTimeout` der Extension-Proxys) auf Direktverbindung aus: elearning-med ist oeffentlich erreichbar und lud direkt, `cip.dmed` nicht und hing 90 s. Kein Fehler von uni-vpn, im Readme als Firefox-Eigenheit vermerkt. |
| 2026-09-08 | 9 Cisco parallel (David, GUI) | Zuerst durchgefallen: Cisco bei stehendem Tunnel verbunden, uni-vpn lief unveraendert weiter, weil Cisco nur beim Aufbau geprueft wurde. Fix: Pruefung alle 30 s auch im Zustand `connected`, dann Abbau und `blocked`. Wiederholung steht aus. |
| 2026-09-08 | Unbedenklich | openconnect meldet einmalig "Failed to set vring #0 RX backend: Socket operation on non-socket" (vhost-net-Versuch bei `--script-tun`), ohne Auswirkung. |
