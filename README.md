# uni-vpn

Uni-VPN (Cisco AnyConnect, Uni Heidelberg) nur fuer bestimmte Webseiten im Browser. Der Tunnel
entsteht automatisch beim ersten Aufruf einer gelisteten Seite, Passwort und TOTP-Schluessel
(zweiter Faktor) liegen im Keyring des Betriebssystems, nach 15 Minuten ohne Datenverkehr wird
wieder getrennt. Alles andere auf dem Rechner bleibt unberuehrt.

Unterstuetzt: Ubuntu 24.04 (GNOME) mit Google Chrome und Firefox. macOS 14+ auf Apple Silicon
ist vorbereitet, aber noch nicht auf einem echten Mac getestet (siehe `docs/macos-test.md`).
Alles andere kann funktionieren, kein Support.

## Installation

Linux:

```
sudo apt install git
git clone https://github.com/DavidVinu/uni-vpn.git ~/uni-vpn
~/uni-vpn/install.sh
```

macOS: zuerst [Homebrew](https://brew.sh) installieren (Admin-Passwort, ein paar Minuten), dann
dieselben drei Zeilen ohne `sudo apt`.

Der Installer fragt nach Uni-ID, Passwort und dem TOTP-Schluessel, richtet den Hintergrunddienst
ein, traegt die Proxy-Regel im System ein und zeigt zum Schluss eine Selbstdiagnose. Danach
offene Browser einmal neu starten.

Fertig. `https://sogo.uni-heidelberg.de` und `https://elearning-med.uni-heidelberg.de` laufen ab
jetzt ueber die Uni (dazu `cip.dmed.uni-heidelberg.de`, das elearning-med fuer seine Statistik
einbindet), alles andere nicht. Weitere Domains stehen auf der Statusseite
`http://127.0.0.1:1081/`.

### Zweiter Faktor

Das URZ verlangt beim VPN-Login ein zeitbasiertes Einmalkennwort (TOTP). Damit uni-vpn ohne
Nachfrage verbinden kann, bekommt der Rechner einen eigenen Token, genau wie das URZ es fuer
KeePassXC beschreibt. Die App auf dem Handy bleibt daneben bestehen.

1. Im Uni-Netz oder mit verbundenem Cisco-Client https://mfa.uni-heidelberg.de oeffnen.
2. Unter "Soft-Token (zeitbasiert)" auf "Einrichten" klicken, einen Namen vergeben, "Weiter".
3. Unter dem QR-Code "Tokendetails einblenden", den Text zwischen `secret=` und `&issuer=`
   kopieren (oder die ganze `otpauth://`-Zeile).
4. Im Installer einfuegen, oder spaeter mit `uni-vpn totp` bzw. auf der Statusseite. Der
   angezeigte Kontrollcode ist das Einmalkennwort fuer den Schritt "Testen" im Portal.

### Wie die Proxy-Regel in den Browser kommt

Der Dienst liefert unter `http://127.0.0.1:1081/proxy.pac` eine Regel "gelistete Domains ueber
127.0.0.1:1080, alles andere direkt". Der Installer traegt diese Adresse als automatische
Proxy-Konfiguration im System ein (Linux: GNOME-Einstellungen, macOS: Netzwerkeinstellungen).
Chrome und Firefox lesen sie von dort. Andere Desktops (KDE, Xfce): die Adresse in den
Browser-Einstellungen unter Netzwerk/Proxy als PAC-URL eintragen, `uni-vpn doctor` nennt sie.

## Bedienung

| Was | Wie |
|---|---|
| Status | `http://127.0.0.1:1081/` (als Lesezeichen) oder `uni-vpn status` |
| Verbinden / Trennen | Knopf auf der Statusseite |
| Passwort aendern | Statusseite, Formular unten, oder `uni-vpn password` |
| TOTP-Schluessel aendern | Statusseite, zweites Formular, oder `uni-vpn totp` |
| Domains aendern | Statusseite, Feld "Domains", oder `~/.config/uni-vpn/domains.txt` (danach Browser neu starten) |
| Wenn etwas nicht geht | `uni-vpn doctor`, Ausgabe in ein Issue kopieren |
| Aktualisieren | `uni-vpn update` |
| Entfernen | `~/uni-vpn/install.sh --uninstall` (setzt die Proxy-Einstellung zurueck) |

## Wenn etwas nicht geht

| Anzeige | Ursache | Loesung |
|---|---|---|
| Browser: `ERR_PROXY_CONNECTION_FAILED` oder "Proxy verweigert die Verbindung" | Dienst laeuft nicht oder Tunnel in Fehlerzustand | `uni-vpn doctor`, dann `uni-vpn service start` |
| Uni-Seite laedt ohne VPN (z.B. Login-Seite der Uni statt Inhalt) | Browser hat die Proxy-Regel nicht gelesen | Browser neu starten; `uni-vpn doctor` zeigt, ob die Regel im System steht |
| Statusseite: "Anmeldung abgelehnt" | Passwort falsch oder abgelaufen | `uni-vpn password`; bleibt es dabei, `uni-vpn log` ansehen und Issue eroeffnen |
| Statusseite: "Einmalcode abgelehnt" | Uhr des Rechners geht falsch oder TOTP-Schluessel stimmt nicht | Automatische Zeit einschalten; sonst Token im MFA-Portal neu anlegen und `uni-vpn totp` |
| Statusseite: "Kein TOTP-Schluessel hinterlegt" | Zweiter Faktor noch nicht eingetragen | `uni-vpn totp`, siehe "Zweiter Faktor" |
| Statusseite: "Warte auf den naechsten Einmalcode" | Innerhalb von 30 s nach dem letzten Login darf derselbe Code nicht noch einmal benutzt werden | Nichts tun, geht von selbst weiter |
| Statusseite: "Cisco Secure Client ist verbunden" | Cisco-Client aktiv | Cisco trennen, uni-vpn verbindet dann von selbst |
| Statusseite: "Schluesselbund gesperrt" | Keyring nach Autologin nicht entsperrt | Abmelden und mit Passwort anmelden |
| Statusseite: "Kein Netz oder Captive Portal" | WLAN-Anmeldeseite noch nicht bestaetigt | Anmeldeseite oeffnen, danach geht es von selbst weiter |
| Erste Seite nach laengerer Pause laedt nicht | Tunnelaufbau dauerte laenger als der Browser wartet | Seite neu laden |
| Seite ist da, aber der Tab laedt minutenlang weiter | Die Seite bindet etwas von einem weiteren Uni-Host ein, der nicht in der Liste steht | In den Browser-Entwicklerwerkzeugen (Netzwerk) den Host mit `ERR_CONNECTION_TIMED_OUT` suchen und auf der Statusseite ergaenzen |
| Firefox: Uni-Seite laedt kurz nach einer Stoerung ohne VPN | Firefox schickt Anfragen nach einem gescheiterten Proxy-Versuch 10 s lang direkt | Seite nach ein paar Sekunden neu laden |

## Was der Rechner davon merkt

- Ein Hintergrundprozess (`uni-vpn daemon`) mit zwei lokalen Ports: 1080 (SOCKS5) und 1081
  (Statusseite und Proxy-Regel). Beide sind nur von diesem Rechner aus erreichbar. Jedes
  Programm auf dem Rechner koennte 127.0.0.1:1080 als Proxy benutzen; das ist gewollt (z.B.
  `curl --socks5-hostname 127.0.0.1:1080`).
- Die Systemeinstellung "automatische Proxy-Konfiguration" zeigt auf den Dienst. Programme, die
  sie beachten (Browser, manche Mail-Programme), schicken nur die gelisteten Domains ueber die
  Uni; alles andere geht wie bisher direkt. Kommandozeilenwerkzeuge ignorieren die Einstellung.
- Keine Routen, kein DNS, kein Root nach der Installation. Sudo wird nur fuer `apt install`
  gebraucht.
- Der Tunnel laeuft komplett im Nutzerkontext und schafft etwa 0,5 MB/s je Verbindung
  (ocproxy, festes 64-KB-Fenster). Fuer Mail und Moodle reicht das, fuer grosse Downloads
  ist der Cisco-Client schneller.
- Passwort und TOTP-Schluessel liegen im GNOME-Keyring bzw. macOS-Schluesselbund, nirgends
  sonst. Waehrend des Verbindungsaufbaus liest openconnect den Schluessel aus einer nur fuer den
  Nutzer lesbaren Datei, die danach sofort geloescht wird. Der Rechner ist damit der zweite
  Faktor, so wie beim vom URZ dokumentierten KeePassXC-Token.
- macOS zeigt den Dienst unter Systemeinstellungen > Allgemein > Anmeldeobjekte.

## Entwicklung

`python3 -m unittest discover -s tests -t . -v` laeuft ohne echte VPN (Fake-openconnect).
Design: `docs/superpowers/specs/2026-09-07-uni-vpn-design.md`. End-to-End-Test: `docs/e2e.md`.
