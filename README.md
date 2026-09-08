# uni-vpn

Uni-VPN (Cisco AnyConnect, Uni Heidelberg) nur fuer bestimmte Webseiten im Browser. Der Tunnel
entsteht automatisch beim ersten Aufruf einer gelisteten Seite, Passwort und TOTP-Schluessel
(zweiter Faktor) liegen im Keyring des Betriebssystems, nach 15 Minuten ohne Datenverkehr wird
wieder getrennt. Alles andere auf dem Rechner bleibt unberuehrt.

Unterstuetzt: Ubuntu 24.04 mit Google Chrome und Firefox (Snap). macOS 14+ auf Apple Silicon
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
ein und zeigt zum Schluss eine Selbstdiagnose. Danach die Extension laden:

- Chrome: `chrome://extensions`, Entwicklermodus einschalten, "Entpackte Erweiterung laden",
  Ordner `~/uni-vpn/extension` waehlen.
- Firefox: `about:debugging#/runtime/this-firefox`, "Temporaeres Add-on laden",
  `~/uni-vpn/extension/manifest.json` waehlen. Das haelt bis zum Neustart; eine signierte Version
  folgt. Firefox fragt zweimal nach: beim Speichern der Optionen die Freigabe fuer die gelisteten
  Domains (bestaetigen), und in `about:addons` unter Uni VPN "In privaten Fenstern ausfuehren"
  erlauben.

Fertig. `https://sogo.uni-heidelberg.de` und `https://elearning-med.uni-heidelberg.de` laufen ab
jetzt ueber die Uni, alles andere nicht. Weitere Domains stehen in den Optionen der Extension.

### Zweiter Faktor

Das URZ verlangt beim VPN-Login ein zeitbasiertes Einmalkennwort (TOTP). Damit uni-vpn ohne
Nachfrage verbinden kann, bekommt der Rechner einen eigenen Token, genau wie das URZ es fuer
KeePassXC beschreibt. Die App auf dem Handy bleibt daneben bestehen.

1. Im Uni-Netz oder mit verbundenem Cisco-Client https://mfa.uni-heidelberg.de oeffnen.
2. Unter "Soft-Token (zeitbasiert)" auf "Einrichten" klicken.
3. Unter dem QR-Code "Tokendetails einblenden", den Text zwischen `secret=` und `&issuer=`
   kopieren (oder die ganze `otpauth://`-Zeile).
4. Im Installer einfuegen, oder spaeter mit `uni-vpn totp` bzw. auf der Statusseite. Der
   angezeigte Kontrollcode muss mit dem Code in der App uebereinstimmen.

## Bedienung

| Was | Wie |
|---|---|
| Status | Extension-Icon anklicken, oder `http://127.0.0.1:1081/`, oder `uni-vpn status` |
| Verbinden / Trennen | Knopf im Popup oder auf der Statusseite |
| Passwort aendern | Statusseite, Formular unten, oder `uni-vpn password` |
| TOTP-Schluessel aendern | Statusseite, zweites Formular, oder `uni-vpn totp` |
| Domains aendern | Optionen der Extension (Rechtsklick auf das Icon) |
| Wenn etwas nicht geht | `uni-vpn doctor`, Ausgabe in ein Issue kopieren |
| Aktualisieren | `uni-vpn update`, danach Extension in `chrome://extensions` neu laden |
| Entfernen | `~/uni-vpn/install.sh --uninstall` |

## Wenn etwas nicht geht

| Anzeige | Ursache | Loesung |
|---|---|---|
| Browser: `ERR_PROXY_CONNECTION_FAILED` oder "Proxy verweigert die Verbindung" | Dienst laeuft nicht oder Tunnel in Fehlerzustand | `uni-vpn doctor`, dann `uni-vpn service start` |
| Popup: "Anmeldung abgelehnt" | Passwort falsch oder abgelaufen | `uni-vpn password`; bleibt es dabei, `uni-vpn log` ansehen und Issue eroeffnen |
| Popup: "Einmalcode abgelehnt" | Uhr des Rechners geht falsch oder TOTP-Schluessel stimmt nicht | Automatische Zeit einschalten; sonst Token im MFA-Portal neu anlegen und `uni-vpn totp` |
| Popup: "Kein TOTP-Schluessel hinterlegt" | Zweiter Faktor noch nicht eingetragen | `uni-vpn totp`, siehe "Zweiter Faktor" |
| Popup: "Cisco Secure Client ist verbunden" | Cisco-Client aktiv | Cisco trennen, uni-vpn verbindet dann von selbst |
| Popup: "Schluesselbund gesperrt" | Keyring nach Autologin nicht entsperrt | Abmelden und mit Passwort anmelden |
| Popup: "Kein Netz oder Captive Portal" | WLAN-Anmeldeseite noch nicht bestaetigt | Anmeldeseite oeffnen, danach geht es von selbst weiter |
| Popup: "Freigabe fuer die gelisteten Domains fehlt" | Firefox-Freigabe abgelehnt oder Domains ohne Optionen-Dialog geaendert | Knopf "Freigeben" im Popup klicken |
| Popup: "In privaten Fenstern nicht aktiv" | Firefox erlaubt Add-ons in privaten Fenstern nicht von selbst | `about:addons`, Uni VPN, "In privaten Fenstern ausfuehren" erlauben |
| Erste Seite nach laengerer Pause laedt nicht | Tunnelaufbau dauerte laenger als der Browser wartet | Seite neu laden |

## Was der Rechner davon merkt

- Ein Hintergrundprozess (`uni-vpn daemon`) mit zwei lokalen Ports: 1080 (SOCKS5) und 1081
  (Statusseite). Beide sind nur von diesem Rechner aus erreichbar. Jedes Programm auf dem
  Rechner koennte 127.0.0.1:1080 als Proxy benutzen; das ist gewollt (z.B. `curl
  --socks5-hostname 127.0.0.1:1080`).
- Keine Routen, kein DNS, kein Root nach der Installation. Sudo wird nur fuer `apt install`
  gebraucht.
- Passwort und TOTP-Schluessel liegen im GNOME-Keyring bzw. macOS-Schluesselbund, nirgends
  sonst. Waehrend des Verbindungsaufbaus liest openconnect den Schluessel aus einer nur fuer den
  Nutzer lesbaren Datei, die danach sofort geloescht wird. Der Rechner ist damit der zweite
  Faktor, so wie beim vom URZ dokumentierten KeePassXC-Token.
- macOS zeigt den Dienst unter Systemeinstellungen > Allgemein > Anmeldeobjekte.

## Entwicklung

`python3 -m unittest discover -s tests -t . -v` laeuft ohne echte VPN (Fake-openconnect).
Design: `docs/superpowers/specs/2026-09-07-uni-vpn-design.md`. End-to-End-Test: `docs/e2e.md`.
