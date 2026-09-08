# uni-vpn: Design

Stand: 2026-09-08. Gilt fuer Etappe 1 (Backend) und Etappe 2 (Proxy-Regel im System). Die
Extension aus der ersten Fassung wurde am 2026-09-08 nach dem E2E-Test gestrichen; die
Rechercheergebnisse dazu in Abschnitt 2 bleiben als Begruendung stehen.

## 1. Ziel

Das Cisco-AnyConnect-VPN der Uni Heidelberg (ASA-Cluster hinter `vpn-ac.uni-heidelberg.de`)
so betreiben, dass

1. der Login unbeaufsichtigt laeuft (Passwort und TOTP-Schluessel im OS-Keyring, keine
   Eingabe im Alltag),
2. nur im Browser und nur fuer eine konfigurierbare Domain-Liste Traffic ueber die Uni geht,
3. die VPN-Session erst beim ersten Aufruf einer gelisteten Domain entsteht und nach
   Leerlauf wieder abgebaut wird,
4. Kommilitonen das Ganze auf Linux und macOS mit einer kurzen Anleitung installieren koennen,
   ohne Geld auszugeben.

Nicht-Ziele: systemweites Routing (SSH, Netzlaufwerke), Windows, Ersatz des Cisco-Clients fuer
Full-Tunnel-Zwecke, Verteilung ueber Chrome Web Store.

## 2. Verifizierte Fakten (Recherche vom 2026-09-07)

Diese Punkte wurden gegen Doku, Quellcode oder direkt am Server geprueft und tragen das Design:

- Der Uni-Server (Tunnel-Group `DefaultWEBVPNGroup`) liefert zuerst ein Login-Formular mit
  `username` und `password` (gemessen mit `openconnect --authenticate --non-inter`). Nach dem
  Passwort verlangt das URZ seit 15.12.2023 ein zeitbasiertes Einmalkennwort (TOTP, RFC 6238,
  6 Ziffern, 30 s), Prompt "Bitte zweiten Faktor eingeben (OTP)". ASA-Session 24 h, Idle 30 min
  (Cisco-Journal vom 2026-09-07).
- openconnect erzeugt TOTP-Codes selbst (`--token-mode=totp --token-secret=@datei`, Format
  `base32:...`, optional `sha256:`-Praefix) und fuellt sie in das Feld `secondary_password`
  oder in ein Formular mit `auth_id="challenge"` (auth.c, 9.12). Es probiert den aktuellen und
  den naechsten Code, danach "Server is rejecting the soft token; switching to manual entry".
  Die Datei wird bei jeder Code-Erzeugung neu gelesen (main.c `lock_token`), muss also bis zum
  fertigen Aufbau existieren. Eine `otpauth://`-URL akzeptiert openconnect nicht.
- Das URZ dokumentiert selbst KeePassXC auf dem PC als Token und empfiehlt zwei Token pro
  Nutzer. Das Self-Service-Portal https://mfa.uni-heidelberg.de (LinOTP, nur aus dem Uni-Netz
  oder per VPN) zeigt unter "Tokendetails einblenden" den Schluessel als Text. Ein Schluessel
  im OS-Keyring entspricht damit dem offiziell beschriebenen Desktop-Token.
- `openconnect --script-tun --script ocproxy` braucht kein Root, kein tun-Device, aendert
  weder Routen noch DNS. ocproxy bindet ohne `-g` nur auf Loopback. Hostnamen werden per
  SOCKS5 im Tunnel aufgeloest (nur erster VPN-DNS-Server, nur IPv4, kein UDP).
- Ubuntu 24.04 hat openconnect 9.12; dessen Default-User-Agent wird von neueren ASAs mit 404
  beantwortet. `--useragent 'AnyConnect Linux_64 5.1.18.314'` ist noetig und funktioniert.
- `--passwd-on-stdin` liest genau eine Zeile; danach muss stdin geschlossen werden, sonst
  blockiert jeder weitere Prompt unbegrenzt. Exit-Codes: Auth-Fehler 1 (bevor der Tunnel
  steht), Cookie abgelehnt 2, Server-Disconnect 1, SIGTERM 0 mit sauberem Logout beim Server,
  SIGHUP 0 ohne Logout. SIGUSR2 erzwingt einen Reconnect.
- Der Uni-ASA meldet: DTLS deaktiviert, Idle Timeout 1800 s, Disconnect Timeout 1800 s,
  Session Timeout 86400 s, DPD 300 s.
- Das Script-Kind (ocproxy) laeuft in eigener Prozessgruppe; ein killpg auf openconnect
  erreicht es nicht. ocproxy beendet sich selbst binnen ~1 s, wenn openconnect weg ist.
- Chrome bricht einen SOCKS5-Handshake nach 30 s ab. Chrome schickt bei `SOCKS5 host:port`
  in der PAC immer den Hostnamen an den Proxy. Ein fehlgeschlagener Proxy wird 5 min als
  "bad" markiert, bleibt bei nur einem Proxy aber in Benutzung.
- `chrome.proxy.settings.set` mit `pac_script` uebersteht das Ende des Service Workers und
  ersetzt die gesamte Proxy-Konfiguration des Profils. Chrome hat kein `proxy.onRequest`.
- Firefox hat keinen Background Service Worker (Pref gesperrt); ein gemeinsames MV3-Manifest
  gibt `background.scripts` und `background.service_worker` beide an. `proxy.onRequest` mit
  `proxyDNS: true` braucht die Permission `proxy` plus Host-Permissions fuer die Domains;
  Host-Permissions sind in MV3 entziehbar. `proxy.settings.set` verlangt Freigabe fuer private
  Fenster, `onRequest` nicht. AMO verlangt fuer neue Extensions
  `data_collection_permissions`, `gecko.id`, und wegen `proxy` `strict_min_version >= 91.1`.
- Chrome zeigt seit Version 121 keinen Start-Hinweis mehr fuer entpackt geladene Extensions,
  auf keiner Plattform. Selbst gehostete `.crx` funktionieren auf macOS ohne MDM nicht
  (InstallVerifier), auf Linux nur mit Policy oder External-Extensions-JSON; jede Policy
  erzeugt "Von deiner Organisation verwaltet". `defaults write` setzt auf macOS nur
  Recommended-Policies, die fuer Extension-Policies wirkungslos sind.
- Firefox-Snap auf Ubuntu 24.04 startet Native-Messaging-Hosts ueber ein xdg-desktop-portal
  (Ubuntu-Patch), mit einmaligem Dialog; Chromium-Snap kann es gar nicht. Das Design
  verzichtet deshalb auf Native Messaging.
- GNOME-Keyring wird beim GDM-Login per PAM entsperrt; bei Autologin bleibt er gesperrt.
  `secret-tool lookup` blockiert dann mit GUI-Prompt. `secret-tool lookup` ohne Treffer:
  Exit 1, keine Ausgabe. Paket `libsecret-tools`.
- systemd --user hat `DBUS_SESSION_BUS_ADDRESS` automatisch. Enable per Symlink ins Home ist
  bei separatem Home unzuverlaessig, daher Unit als Kopie.
- macOS: `/usr/bin/python3` ist ohne Xcode-CLT ein Stub (GUI-Dialog) und mit CLT 3.9.6.
  Homebrew liefert openconnect 9.21 und ocproxy 1.60 (Apple Silicon, macOS 14+).
  `security find-generic-password -w` liest aus einem LaunchAgent ohne Dialog, wenn das Item
  von `security` selbst angelegt wurde. LaunchAgents haben minimalen PATH. Ein Listener nur
  auf 127.0.0.1 loest keinen Firewall-Dialog aus.
- Der Cisco Secure Client setzt bei aktivem Tunnel eine Default-Route durch `cscotun0`;
  der Cluster-Director ist dann nicht erreichbar. Getrennt stoert er nicht.

## 3. Architektur

```
Browser (Chrome oder Firefox), liest die Systemeinstellung "automatische Proxy-Konfiguration"
      |  holt http://127.0.0.1:1081/proxy.pac: gelistete Domain -> SOCKS5 127.0.0.1:1080, sonst DIRECT
      |  Status/Steuerung: Statusseite http://127.0.0.1:1081/
      v
uni-vpn Daemon (Python, ein Prozess, User-Dienst)
  SOCKS-Forwarder 1080 ---> ocproxy (dynamischer Port) <--socketpair-- openconnect ---TLS---> Uni-ASA
  HTTP 1081: /proxy.pac, /status.json, /api/connect, /api/disconnect, /api/password, /api/totp, /api/domains, Statusseite
  Zustandsautomat, Leerlauf-Timer, Keyring-Zugriff, Log
```

Keine Browser-Extension (Entscheidung vom 2026-09-08 nach dem E2E-Test: Entwicklermodus, temporaere
Add-ons, Signierung und Freigabe-Dialoge waren die groesste Reibung; die Proxy-Regel kommt
jetzt als PAC ueber die Systemeinstellung, die beide Browser lesen).

Alles laeuft ohne Root. Das System sieht keinen Tunnel, keine Routen, kein DNS. Nur Prozesse,
die 127.0.0.1:1080 als SOCKS5-Proxy benutzen, gehen ueber die Uni.

## 4. Backend

### 4.1 Aufbau

Python >= 3.11, nur Standardbibliothek (asyncio, tomllib, json, http.server via asyncio,
subprocess, socket, logging, struct, fcntl). Paket `uni_vpn/` mit klar getrennten Modulen:

| Modul | Aufgabe |
|---|---|
| `config.py` | `config.toml` lesen, Defaults, Validierung mit Zeilenangabe |
| `platform.py` | OS-Erkennung, Pfade, Keyring-Kommandos, Dienst-Kommandos, Cisco-Erkennung |
| `credentials.py` | Passwort holen/setzen mit Timeout, Fehlerklassen |
| `tunnel.py` | openconnect-Prozess: Start, Bereitschaftsprobe, Stopp, stderr-Auswertung |
| `forwarder.py` | SOCKS-Passthrough 1080 -> ocproxy, Verbindungs- und Byte-Zaehler |
| `daemon.py` | Zustandsautomat, Bedarfssteuerung, Leerlauf, Resume-Erkennung |
| `httpapi.py` | Statusseite und JSON-API auf 1081 |
| `cli.py` | Subkommandos |
| `doctor.py` | Selbstdiagnose |

Einstieg: `bin/uni-vpn` (Shebang `/usr/bin/env python3` fuer die Shell; Dienst-Dateien
tragen den absoluten Interpreter-Pfad).

### 4.2 Konfiguration

`~/.config/uni-vpn/config.toml` auf beiden Systemen (`$XDG_CONFIG_HOME` wird beachtet):

```toml
host = "vpn-ac.uni-heidelberg.de"
user = "ab123"                 # Uni-ID
idle_minutes = 15
socks_port = 1080
http_port = 1081
useragent = "AnyConnect Linux_64 5.1.18.314"
# openconnect = "/usr/sbin/openconnect"   # optional, sonst Suche
# ocproxy = "/usr/bin/ocproxy"
```

Der Daemon schreibt nie in die Config. Bei Fehlern in der Config startet er trotzdem, oeffnet
1081 und meldet dort `error: config.toml Zeile N`.

Log: Linux `~/.local/state/uni-vpn/daemon.log`, macOS `~/Library/Logs/uni-vpn/daemon.log`,
RotatingFileHandler 1 MB x 3, Datei 0600. Lock-Datei `~/.config/uni-vpn/daemon.lock`
(`fcntl.flock`, Doppelstart endet mit Exit 0 und Meldung).

### 4.3 Zustandsautomat

| Zustand | Bedeutung | SOCKS-Verbindungen | Automatik |
|---|---|---|---|
| `idle` | kein Tunnel, nichts gebraucht | loesen Aufbau aus, warten max 25 s | keine |
| `offline` | TLS-Probe zum Host fehlgeschlagen (kein Netz, Captive Portal) | sofort schliessen | Probe alle 30 s solange Bedarf |
| `blocked` | Cisco Secure Client ist verbunden | sofort schliessen | Pruefung alle 30 s solange Bedarf; ohne Bedarf bleibt `blocked` stehen, bis der Ticker Cisco als getrennt sieht |
| `connecting` | openconnect laeuft, ocproxy-Port noch zu | warten max 25 s | Abbruch nach 45 s -> `error` |
| `connected` | Tunnel steht | durchreichen | Leerlauf-Timer |
| `disconnecting` | Abbau laeuft | warten, dann neuer Aufbau bei Bedarf | |
| `auth_failed` | Server hat Passwort oder Einmalcode abgelehnt oder etwas Unbekanntes verlangt | sofort schliessen | keine, bis `password`/`totp` gesetzt oder `connect` ausgeloest |
| `keyring` | Passwort oder TOTP-Schluessel fehlt oder Keyring gesperrt | sofort schliessen | keine, bis `password`/`totp` oder `connect` |
| `error` | Netz-/Serverfehler nach vorherigem Erfolg | sofort schliessen | Backoff 5, 10, 20, 40, 80 s, dann 5 min, mit Jitter, nur bei Bedarf |

Bedarf = mindestens eine offene SOCKS-Verbindung oder Daten in den letzten 60 s oder ein
expliziter `connect`. Ohne Bedarf wird nach Prozessende nicht neu aufgebaut, sondern `idle`.

Aufbau, Schritt fuer Schritt:

1. Cisco-Pruefung: `cscotun0` vorhanden (Linux) oder `/opt/cisco/secureclient/bin/vpn state`
   meldet `Connected` -> `blocked`. Dieselbe Pruefung laeuft alle 30 s auch bei stehendem
   Tunnel (im Executor, weil `vpn state` auf macOS 2 s braucht): verbindet sich Cisco spaeter,
   werden Browserverbindungen geschlossen, der Tunnel abgebaut und `blocked` gemeldet
   (gemessen 2026-09-08: vorher lief uni-vpn einfach weiter).
2. TLS-Probe: `ssl.create_default_context()`-Handshake auf `host:443`, 5 s -> sonst `offline`.
3. Passwort: Keyring-Lookup mit 20 s Timeout. Exit 1 ohne Ausgabe -> `keyring` ("kein Passwort
   hinterlegt"), Timeout -> `keyring` ("Schluesselbund gesperrt"). Danach genauso der
   TOTP-Schluessel (eigener Keyring-Eintrag `uni-vpn-totp`), fehlend -> `keyring` ("kein
   TOTP-Schluessel hinterlegt: uni-vpn totp"). Ohne beides startet openconnect nicht.
4. Freien ocproxy-Port waehlen (bind 127.0.0.1:0, getsockname, close).
4a. Einmalcode-Fenster: der Server nimmt jeden Code nur einmal an (gemessen 2026-09-08: ein
    Neuaufbau 3 s nach dem Login scheiterte mit "Login failed"). Der Daemon merkt sich das
    30-s-Fenster, in dem openconnect zuletzt einen Code erzeugt hat, und wartet im selben
    Fenster bis zum naechsten (Zustand `connecting`, "Warte auf den naechsten Einmalcode").
5. Start: TOTP-Schluessel in eine Datei `totp-*` (mkstemp, 0600) im Zustandsordner schreiben,
   dann
   ```
   openconnect --protocol=anyconnect --useragent <ua> --user <user>
     --passwd-on-stdin --non-inter --no-dtls --force-dpd 30 --reconnect-timeout 60
     --script-tun --script "<abs>/uni-vpn-ocproxy <port>"
     --token-mode=totp --token-secret=@<datei> <host>
   ```
   mit `start_new_session=True`, stdin=PIPE, stdout+stderr=PIPE. Passwort als bytes plus
   `\n` schreiben, stdin schliessen, Referenz loeschen. `uni-vpn-ocproxy` ist ein Wrapper mit
   `exec ocproxy -D 127.0.0.1:<port> -k 30`; der `--script`-Wert beginnt mit `exec`, weil
   openconnect ihn per `/bin/sh -c` startet und dash sonst ein `sh` daneben stehen liesse.
   Der Schluessel steht nie in der Kommandozeile. Die Datei wird geloescht, sobald der Port
   offen ist, der Prozess endet oder der Daemon ihn stoppt; beim Daemon-Start werden
   uebrig gebliebene `totp-*`-Dateien entfernt.
6. Bereitschaft: TCP-Connect-Probe auf den ocproxy-Port alle 250 ms, max 45 s. Sobald offen:
   `connected`. Wartende Client-Verbindungen werden durchgereicht; die Auth-Dauer wird geloggt.
7. stderr zeilenweise lesen und bewerten (erstes Urteil bleibt). Gemessen am 2026-09-08:
   der ASA meldet ein falsches Passwort mit `Login failed.` vor der OTP-Abfrage, einen
   falschen Einmalcode mit `Login failed.` nach `Generating OATH TOTP token code`; danach
   zeigt er in beiden Faellen das Formular erneut, es folgen `User input required` und
   `Failed to complete authentication`. Daher: `Login failed` ohne vorherige Code-Erzeugung
   -> `auth_failed` ("Passwort pruefen, uni-vpn password"), mit -> `auth_failed`
   ("Einmalcode abgelehnt: Uhrzeit pruefen, sonst uni-vpn totp"). Weitere Marker:
   `Server is rejecting the soft token` (Server zeigt das OTP-Formular erneut) -> dieselbe
   Einmalcode-Meldung; `Soft token string is invalid` -> `auth_failed` ("uni-vpn totp");
   `User input required in non-interactive mode` ohne vorheriges `Login failed` ->
   `auth_failed` ("Passwort pruefen; stimmt es, hat der Server etwas Unbekanntes verlangt,
   siehe Log"); `Failed to complete authentication` -> dieselbe Meldung; `Server asked us to
   run CSD` oder `Cisco Secure Desktop` ->
   `auth_failed` ("HostScan verlangt, Update noetig"); `SAML` oder `external browser` ->
   `auth_failed` ("Login-Verfahren geaendert"); `certificate` -> `error` ("Zertifikat").
   Die letzten 20 stderr-Zeilen werden im Status mitgefuehrt.
8. Prozessende: Exit 0 nach eigenem SIGTERM -> sauber. Exit 1 bevor der Port je annahm ->
   `auth_failed`. Exit 2 oder Exit nach erfolgreicher Verbindung -> bei Bedarf Neuaufbau mit
   Backoff (`error`), sonst `idle`.

Abbau: SIGTERM an openconnect, bis 15 s `wait()`, dann SIGKILL. Danach pruefen, dass der
ocproxy-Port nicht mehr annimmt; nimmt er nach 5 s noch an, PID ueber den Wrapper-Pfad
ermitteln und SIGKILL. Alle Client-Verbindungen werden beim Abbau geschlossen.

Leerlauf: `last_activity` wird bei jedem weitergeleiteten Chunk in beide Richtungen gesetzt.
Abbau nach `idle_minutes` ohne Bytes, unabhaengig von offenen Verbindungen (WebSockets und
Long-Polling halten sonst ewig). Halb geschlossene Verbindungen: EOF einer Seite wird als
`write_eof` weitergegeben, nach 60 s Nachfrist ohne Daten wird geschlossen.

Resume-Erkennung: alle 5 s `time.monotonic()` gegen `time.time()`; Sprung > 30 s -> alle
Forwarder-Verbindungen schliessen, Tunnel sauber beenden (`disconnecting` -> `idle`) und bei
Bedarf ueber den normalen Zustandsautomaten neu aufbauen.

### 4.4 SOCKS-Forwarder

Reines Byte-Passthrough auf 127.0.0.1:`socks_port` (IPv4-Literal, nicht `localhost`). Kein
eigener SOCKS5-Handshake; Chrome und Firefox unterscheiden Reply-Codes nicht sinnvoll,
sofortiges Schliessen ergibt dieselbe Fehlerseite. Der Forwarder zaehlt aktive Verbindungen und
Bytes je Richtung fuer Status und Log. Kein Auth-Token (Chrome kann bei SOCKS5 keins senden),
keine Peer-UID-Pruefung (auf macOS unmoeglich, auf Einzelnutzer-Laptops ohne Nutzen).

### 4.5 HTTP-Status und API (127.0.0.1:`http_port`)

| Route | Methode | Inhalt |
|---|---|---|
| `/` | GET | Statusseite: Zustand in Klartext, Knoepfe Verbinden/Trennen, Formulare "Passwort setzen", "TOTP-Schluessel setzen" (mit Link zum MFA-Portal) und "Domains", letzte Logzeilen, Versions- und Portangaben. Deutsch, so kurz wie moeglich. |
| `/status.json` | GET | `{"protocol": 1, "version", "state", "message", "since", "host", "user", "socks_port", "active_connections", "bytes_in", "bytes_out", "last_error", "domains", "pac_url", "pac_refresh", "log_tail": [...]}` |
| `/api/connect` | POST | Bedarf setzen, Aufbau starten (auch aus `auth_failed`, `keyring`, `error`) |
| `/api/disconnect` | POST | Tunnel abbauen, Bedarf loeschen |
| `/api/password` | POST | JSON `{"password": ...}` -> Keyring, danach Aufbau |
| `/api/totp` | POST | JSON `{"secret": ...}` (otpauth-URL oder Base32) -> normalisiert in den Keyring, danach Aufbau; Antwort enthaelt den aktuellen Kontrollcode zum Vergleich mit der App |
| `/proxy.pac` | GET | PAC-Datei aus der Domainliste (Abschnitt 5) |
| `/api/domains` | POST | JSON `{"text": ...}` (eine Domain je Zeile) -> `domains.txt`, danach Nachladen der Regel im System; 400 mit Zeilennummern bei Fehlern |

CSRF-Schutz: POST nur mit Header `X-Uni-VPN: 1` und Origin leer oder `http://127.0.0.1:<port>`.
Antworten tragen `Access-Control-Allow-Origin` nur fuer diesen Origin. Passwort und Schluessel werden nie geloggt und nie in `/status.json`
ausgegeben.

### 4.6 CLI

`uni-vpn <kommando>`:

| Kommando | Wirkung |
|---|---|
| `status` | Zustand in einer Zeile; `--json` gibt `/status.json` aus |
| `connect`, `disconnect` | wie die API |
| `password` | fragt interaktiv (getpass), legt im Keyring ab: Linux `secret-tool store --label 'Uni VPN' service uni-vpn user <user>` (Passwort per stdin ohne Newline), macOS `security add-generic-password -a <user> -s uni-vpn -T /usr/bin/security -w` (vorher loeschen statt `-U`, das haengt ohne GUI) |
| `totp` | fragt interaktiv nach otpauth-URL oder Base32, prueft und normalisiert (`uni_vpn/totp.py`), legt unter `service uni-vpn-totp` ab, zeigt den Kontrollcode |
| `log` | letzte 200 Logzeilen |
| `doctor` | Selbstdiagnose, siehe 4.7 |
| `daemon` | Vordergrundprozess fuer den Dienst |
| `service start|stop|restart|enable|disable` | Wrapper um systemctl --user bzw. launchctl |

Steuerung laeuft ueber die HTTP-API, kein zusaetzlicher Steuer-Socket.

### 4.7 Doctor

Prueft und meldet, ohne Geheimnisse auszugeben: Python-Version; openconnect und ocproxy
gefunden (Pfad, Version); Config gueltig; Dienst geladen und aktiv; Ports 1080 und 1081
gebunden (bei Belegung: welcher Prozess, per `ss -ltnp` bzw. `lsof`); Keyring-Roundtrip
(Passwort und TOTP-Schluessel hinterlegt: ja/nein/gesperrt); Cisco-Client installiert und verbunden; Secret
Service erreichbar (Linux); Proxy-Regel im System (Abschnitt 5.3); Browser gefunden (Chrome, Firefox).
Ausgabe ist zum Einfuegen in ein GitHub-Issue gedacht.

### 4.8 Plattformen

| | Linux (Ubuntu 24.04, GNOME) | macOS 14+, Apple Silicon (experimentell) |
|---|---|---|
| Pakete | `sudo apt install openconnect ocproxy libsecret-tools` | `brew install openconnect ocproxy python` (Homebrew muss vorher installiert sein) |
| Interpreter | `/usr/bin/python3` | `$(brew --prefix)/bin/python3` |
| Keyring | secret-tool (GNOME-Keyring oder KDE ksecretd ueber Secret Service) | security (Login-Schluesselbund) |
| Autostart | `~/.config/systemd/user/uni-vpn.service`, `WantedBy=graphical-session.target`, `PartOf=graphical-session.target`, `Restart=always`, `RestartSec=5`, `StartLimitIntervalSec=0`, `LimitCORE=0` | `~/Library/LaunchAgents/de.davidvinu.uni-vpn.plist`, `RunAtLoad`, `KeepAlive`, `EnvironmentVariables.PATH` mit Brew-Prefix, Log nach `~/Library/Logs/uni-vpn/` |
| Laden | `systemctl --user daemon-reload && systemctl --user enable --now uni-vpn` | `launchctl bootout gui/$UID <plist>; launchctl bootstrap gui/$UID <plist>` |
| Cisco-Erkennung | nur `/sys/class/net/cscotun0` (`vpn state` braucht 2,2 s je Aufruf) | `vpn state` |
| Log | `~/.local/state/uni-vpn/` | `~/Library/Logs/uni-vpn/` |

Kein Linger: der Dienst startet mit der grafischen Sitzung, dann ist der Keyring entsperrt.

macOS gilt als experimentell, bis eine Person mit Mac die Checkliste in `docs/macos-test.md`
durchlaufen hat. CI laeuft auf einem GitHub-Actions-macOS-Runner (Unit-Tests, `plutil -lint`,
`brew install`, Installer im Trockenlauf).

### 4.9 Sicherheit

- Passwort und TOTP-Schluessel nur im OS-Keyring; im Daemon nur kurz im Speicher, nie in
  Umgebungsvariablen, Argumenten oder Logs. Der Schluessel liegt waehrend des Aufbaus in einer
  0600-Datei im 0700-Zustandsordner und wird danach geloescht. Nie `--dump-http-traffic`,
  hoechstens ein `-v`, nie `ocproxy -T`.
- Im Daemon `RLIMIT_CORE=0` und `PR_SET_DUMPABLE=0` (Linux, ctypes). `install.sh` traegt
  `/usr/sbin/openconnect` in `~/.apport-ignore.xml` ein.
- 1080 und 1081 nur auf 127.0.0.1. Jeder lokale Prozess kann den SOCKS-Port nutzen; das ist
  enger als der Cisco-Client (Full Tunnel fuer alle Prozesse) und wird im Readme genannt.
- Kein Zertifikats-Pinning; Systemtruststore (GEANT ist in ca-certificates).

## 5. Proxy-Regel im System (PAC)

### 5.1 Regel

`uni_vpn/pac.py` erzeugt aus der Domainliste eine PAC-Datei: `host == d || host.endsWith("." + d)`
-> `SOCKS5 127.0.0.1:<socks_port>` (Chrome und Firefox loesen den Hostnamen dann ueber den
Proxy auf), sonst `DIRECT`. Kein DIRECT-Fallback fuer gelistete Hosts. Der Daemon liefert sie
unter `GET /proxy.pac` (`application/x-ns-proxy-autoconfig`, `Cache-Control: no-store`) und
liest die Liste bei jeder Anfrage neu.

### 5.2 Domainliste

`~/.config/uni-vpn/domains.txt`, eine Domain je Zeile, `#` leitet Kommentare ein, fuehrendes
`*.` wird entfernt, Kleinschreibung, Duplikate fallen weg, Hostnamen werden geprueft (keine
Schemata, Pfade, IPv6-Literale, mindestens zwei Labels). Fehlt die Datei, gilt die Vorbelegung
`sogo.uni-heidelberg.de`, `elearning-med.uni-heidelberg.de`, `cip.dmed.uni-heidelberg.de`
(Matomo-Skript von elearning-med, sonst wartet der Browser 136 s auf den Timeout). Aenderung
ueber die Statusseite (`POST /api/domains`, Fehler mit Zeilennummer) oder mit einem Editor.

### 5.3 Eintrag im System

`uni_vpn/sysproxy.py`:

| | Linux (GNOME) | macOS |
|---|---|---|
| Lesen | `gsettings get org.gnome.system.proxy mode` und `autoconfig-url` | `networksetup -listallnetworkservices`, je Dienst `-getautoproxyurl` |
| Setzen | `autoconfig-url` auf die PAC-URL, `mode` auf `auto` | `-setautoproxyurl <Dienst> <URL>` fuer jeden aktiven Dienst (verlangt Admin-Rechte) |
| Nachladen nach Listenaenderung | `autoconfig-url` mit `?v=<Zeit>` neu setzen; GNOME meldet die Aenderung, Chrome und Firefox holen die PAC neu | nicht automatisch, Statusseite sagt "Browser neu starten" |
| Zuruecksetzen | gesicherte Werte aus `~/.config/uni-vpn/proxy-backup.json` | dito, `-setautoproxystate off` wenn vorher aus |

Der Installer sichert den vorherigen Zustand einmalig (ein zweiter Lauf ueberschreibt die
Sicherung nicht), traegt die Regel ein und meldet, wenn er eine fremde Proxy-Einstellung ersetzt
hat. Ohne GNOME- oder macOS-Proxyverwaltung (KDE, Xfce) nennt er die PAC-URL zum Eintragen im
Browser; `uni-vpn doctor` prueft den Eintrag ("Proxy-Regel": ok, nicht gesetzt, fremd, keine
Verwaltung).

Chrome liest die GNOME-Einstellung ueber GSettings, Firefox mit `network.proxy.type = 5`
(Voreinstellung "Systemeinstellungen") ebenfalls. Beide holen die PAC beim Start und bei
Aenderung der Einstellung (gemessen 2026-09-08: Chrome sofort, Firefox nach etwa 10 s).
Firefox loest Hostnamen bei PAC-SOCKS lokal auf (`network.proxy.socks_remote_dns` ist in
Firefox 155 `false`); Hosts, die nur im Uni-DNS existieren, brauchen dort die Umstellung auf
`true`, das Readme nennt sie. Chrome loest bei `SOCKS5` immer ueber den Proxy auf. Firefox weicht nach einem gescheiterten Proxy-Verbindungsversuch fuer
10 s auf DIRECT aus (`network.proxy.failover_direct`), Chrome meldet
`ERR_PROXY_CONNECTION_FAILED`; beides steht im Readme.

## 6. Installer und Lebenszyklus

`install.sh` im Repo-Root, idempotent, drei Modi:

- `./install.sh` (install): OS erkennen; Linux: `sudo apt install` der drei Pakete; macOS:
  Homebrew finden (`/opt/homebrew/bin/brew`, `/usr/local/bin/brew`), sonst Abbruch mit
  Hinweis, `brew install openconnect ocproxy python`; Python-Version pruefen; Uni-ID abfragen
  und `config.toml` schreiben, falls nicht vorhanden; `~/.local/bin/uni-vpn` verlinken;
  Dienst-Datei mit absoluten Pfaden schreiben und laden; Cisco-Client erkennen und
  Hinweis geben (nichts ungefragt aendern); Proxy-Regel im System eintragen (Abschnitt 5.3);
  Passwort und TOTP-Schluessel abfragen; `uni-vpn doctor` ausfuehren; auf die Statusseite und
  den Browser-Neustart hinweisen. Jede angelegte Datei kommt in
  `~/.config/uni-vpn/installed-files.txt`.
- `./install.sh --update` bzw. `uni-vpn update`: `git pull` im Repo, Dienst neu starten.
- `./install.sh --uninstall`: Proxy-Einstellung zuruecksetzen, Dienst stoppen und entfernen,
  Dateien aus der Liste loeschen, Keyring-Eintraege mit Rueckfrage loeschen, sagen, was bleibt
  (apt/brew-Pakete, Repo-Ordner, Log).

Support-Matrix im Readme: Ubuntu 24.04 (GNOME) mit Google Chrome und Firefox, macOS 14+
Apple Silicon mit Chrome und Firefox. Alles andere "kann funktionieren, kein Support".
Erwartbare Dialoge (sudo, Keyring, macOS Anmeldeobjekte und Admin-Passwort fuer die
Proxy-Einstellung) werden im Readme vorweggenommen. Fehlermeldungstabelle "Anzeige -> Ursache
-> Loesung".

## 7. Tests

Backend (`unittest`, `IsolatedAsyncioTestCase`, alle Timeouts aus der Config in
Zehntelsekunden, `tests/fake_openconnect.py` als Ersatzprozess, der das Passwort von stdin
liest, nach Verzoegerung auf dem per Argument uebergebenen Port lauscht und Bytes zurueckspiegelt):

1. Erste SOCKS-Verbindung startet den Prozess, Passwort kommt genau einmal ueber stdin an,
   Bytes gehen nach Bereitschaft 1:1 in beide Richtungen durch.
2. Zweite Verbindung waehrend `connecting` startet keinen zweiten Prozess.
3. Wartezeit ueberschritten -> Client-Socket geschlossen, Aufbau laeuft weiter.
4. Leerlauf ohne Daten trotz offener Verbindung -> SIGTERM nach `idle_minutes`, Clients zu.
5. Prozess ignoriert SIGTERM -> SIGKILL nach Frist.
6. Exit 1 vor Bereitschaft -> `auth_failed`, kein Neustart; `connect` startet erneut.
7. Prozessende nach Erfolg ohne Bedarf -> `idle`; mit Bedarf -> Backoff und Neustart.
8. stderr-Marker -> richtige Zustaende und Meldungen.
9. Keyring-Lookup: fehlend, Timeout, vorhanden (gemockter Befehl); Passwort und TOTP unter
   getrennten Dienstnamen.
9a. TOTP: Normalisierung (otpauth-URL, Base32 mit Leerzeichen und Kleinbuchstaben, Ablehnung
    von HOTP, fremden Ziffern/Perioden, Unsinn), RFC-6238-Testvektoren; Schluesseldatei
    0600, Inhalt kommt beim Fake an, Datei ist nach Bereitschaft, Exit und Stopp weg;
    fehlender Schluessel -> `keyring`, abgelehnter Code -> `auth_failed` ohne Wiederholung.
10. Cisco-Erkennung und TLS-Probe (gemockt) -> `blocked` bzw. `offline`, keine Fehlversuche.
11. HTTP-API: Status, connect, disconnect, password, totp (Kontrollcode, Ablehnung von
    Unsinn), CSRF-Ablehnung ohne Header oder mit fremdem Origin.
12. Config-Fehler -> Daemon laeuft, Status meldet Zeile.
13. Doppelstart -> Exit 0.

PAC und Systemproxy: Domainliste (Normalisierung, Fehler mit Zeilennummer, Datei-Roundtrip,
Vorbelegung), PAC-Text und, wo `node` vorhanden ist, die Auswertung der PAC-Funktion gegen eine
Hostliste; `sysproxy` mit abgefangenen `gsettings`- bzw. `networksetup`-Aufrufen (lesen,
setzen, sichern, zuruecksetzen, Nachladen, Zustaende fuer den Doctor); HTTP `/proxy.pac` und
`/api/domains`.

End-to-End auf Linux (manuell, dokumentiert in `docs/e2e.md`): Cisco getrennt, `curl
--socks5-hostname 127.0.0.1:1080 https://ifconfig.me` liefert eine Uni-Adresse, Statusseite
zeigt `connected`, nach `idle_minutes` wieder `idle`, Chrome und Firefox laden ueber die
Systemeinstellung `sogo.uni-heidelberg.de` durch den Tunnel und `ifconfig.me` direkt.

CI (GitHub Actions): `ubuntu-latest` und `macos-latest`: Unit-Tests, `python -m compileall`,
`plutil -lint` fuer das Plist, `bash -n install.sh`, Installer-Trockenlauf (`--dry-run`).

## 8. Etappen

1. Backend, Installer, Tests, Readme (Linux getestet, macOS ueber CI abgesichert).
2. Proxy-Regel im System (PAC), E2E in beiden Browsern.
3. macOS-Smoke-Test durch eine Person mit Mac.

## 9. Offene Punkte

- Das OTP-Formular des URZ kommt als Challenge nach dem Passwort ("Bitte zweiten Faktor
  eingeben (OTP)"), openconnect fuellt es selbst (bestaetigt am 2026-09-08, Tunnel nach
  1,5 s). Aendert das URZ das Formular, meldet der Daemon `auth_failed` mit Hinweis auf das
  Log, in dem der Prompt-Text steht.
- Gleichzeitige Sessions desselben Kontos (Cisco-Client plus uni-vpn) sind nicht dokumentiert.
  Empfehlung im Readme: Cisco-Client nicht parallel verbinden, `AutoConnectOnStart` abschalten.
- macOS ist bis zum Smoke-Test experimentell.
- Durchsatz: etwa 450 KB/s je Verbindung durch ocproxy (lwIP, `TCP_WND` 64 KB, `TCP_MSS`
  1024, gemessen 2026-09-08 bei 48 ms RTT; direkt 7,4 MB/s). Ohne eigenen ocproxy-Build nicht
  aenderbar, fuer Mail und Moodle ausreichend, im Readme als Grenze genannt.
- Seiten auf gelisteten Domains binden Ressourcen weiterer interner Uni-Hosts ein; ist so ein
  Host nicht gelistet, wartet der Browser bis zum Verbindungs-Timeout (Chrome 136 s), bevor
  die Seite als geladen gilt. Bekannte Faelle stehen in der Vorbelegung, das Readme erklaert
  die Suche nach weiteren.
