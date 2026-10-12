# Syncing between devices

A user's devices share uni-vpn's settings: university, user name, profile overrides, password,
one-time-code secret, websites, language and automatic updates. Ports, program paths and the
log stay per device. Role models: Signal and WhatsApp linked devices for adding a device (one
device shows a QR code, the other scans it), Brave Sync for syncing without an account.

Everything travels end-to-end encrypted through a small relay (`relay/`, a Cloudflare Worker
with one D1 database). The relay stores random ids and ciphertext; it never sees a key or a
setting. The client is `internal/devsync`; the daemon wires it to
config.toml, the website list and the keyring (`internal/daemon/sync.go`).

## For users

Settings, Devices, Add a device shows a QR code with a 12-character code under it. On the new
device, the setup assistant's "Use settings from another device" shows a code too; either
device can scan or type the other's code ("Enter a code instead"). The new device is then set
up completely, including the password and the second factor, so it needs no second trip to
the university's MFA portal. Changes on any device reach the others when it starts, every 30
minutes, and whenever Settings is opened. Remove takes a device out of the list; the others
switch to a new key the removed device never sees.

## Keys

A sync group is one random 32-byte root key `R`, held by each of its devices in the keyring
(service `uni-vpn-sync`, account `device`, the value is base64 of
`{"root": R, "device": id, "priv": X25519 private key}` with byte fields in base64).

From `R`, with HKDF-SHA256 (no salt, info as given):

| Value | Info | Length |
|---|---|---|
| group id (hex, relay path) | `uni-vpn group id` | 16 |
| bearer token (hex) | `uni-vpn group token` | 32 |
| encryption key | `uni-vpn group key` | 32 |

The relay stores only SHA-256 of the token, so a database leak does not allow writes.

## Blob format

Every blob the relay holds:

| Bytes | Content |
|---|---|
| 1 | format version, `0x01` |
| 24 | random nonce |
| rest | XChaCha20-Poly1305 ciphertext with its 16-byte tag |

The additional data is the ASCII string `uni-vpn ` followed by a context, so a blob cannot be
moved to another place: `group <group id>` for a group, `pairing a` and `pairing b` for the
two pairing slots, `device box` inside a sealed key (below).

## Group document

The plaintext of a group blob is JSON:

```json
{"format": 1, "entries": {"<key>": {"v": <value>, "t": <unix ms>, "d": "<device id>"}}}
```

| Key | Value |
|---|---|
| `account` | `{"university": id, "user": name, "overrides": [[field, value], ...]}` in config.toml's order |
| `password` | string |
| `totp` | string, `""` without a second factor |
| `language` | string, `""` follows the system |
| `auto_update` | boolean |
| `domain/<host>` | `true` listed, `false` removed |
| `device/<id>` | `{"name", "platform", "pub": X25519 public key (base64)}`, `null` once removed |

Merging is per key: the larger `t` wins, the larger `d` breaks a tie. `t` is when the change
was made, not when it was sent, so an edit made offline loses against a later one made
elsewhere. One key per website means a website added on one device and another removed
elsewhere both survive. A device keeps `sync.json` next to config.toml with a hash, time and
device of every key as last synced, so it notices its own changes without storing a secret on
disk, and `sync.json.pending` with the time of a change not yet sent and whether a secret
changed; secrets are read from the keyring only then. A value this device cannot take over (a
newer version's setting, say) is logged and skipped; the rest of the round goes through.

## Removing a device

The removing device makes a new root key `R'`, writes the group document with
`device/<removed>: null` to the new group, then replaces the old group with

```json
{"format": 1, "moved": {"keys": {"<device id>": "<sealed R' (base64)>", ...}}}
```

listing every remaining device, and closes the old group with that write (`Sync-Final`), so
the removed device, which still knows the old token, cannot replace the note. A sealed key is the 32-byte X25519 public key of a fresh
ephemeral key pair, followed by a blob (format above, context `device box`) whose key is
HKDF-SHA256 of `shared secret || ephemeral public key || recipient public key` with info
`uni-vpn device box`. A device that finds itself in `moved` switches to `R'`; one that does not
turns syncing off. Its settings stay.

## Pairing

The code is 12 characters of Crockford's base32 (`0-9`, `A-Z` without `I L O U`, 60 bits),
shown as `XXXX-XXXX-XXXX`. The QR code holds `UNIVPN:` and the 12 characters (alphanumeric
mode, error correction M). Typed input ignores case, spaces and dashes and reads `O` as `0`,
`I` and `L` as `1`.

`S = scrypt(code, salt "uni-vpn pairing", N=32768, r=8, p=1, 32 bytes)`, then with HKDF-SHA256:
slot A id (`uni-vpn pairing slot a`, 16 bytes, hex), slot B id (`uni-vpn pairing slot b`),
key (`uni-vpn pairing key`, 32 bytes). Both scanning and typing use the same code, so one
slot pair serves both.

| Device showing the code | Slot A message | Device entering it | Slot B answer |
|---|---|---|---|
| has the settings ("Add a device") | `{"kind": "offer", "root": R}` | joins, takes all settings | `{"kind": "joined"}` |
| is new ("Use settings from another device") | `{"kind": "request"}` | must be set up, joins or starts a group | `{"kind": "root", "root": R}` |

The showing device polls slot B every 2 seconds for 10 minutes. A slot can be read once.
Guessing a code means scrypt per guess against an id space of 2^60 within ten minutes.

## Relay

Base URL in `SyncRelay` (`internal/daemon/sync.go`); `UNI_VPN_SYNC_RELAY` replaces it. All
bodies are `application/octet-stream`.

| Request | Answer |
|---|---|
| `PUT /v1/pair/<id>` (up to 8 KiB) | 201; 409 while the slot holds something; 413 too large or empty |
| `GET /v1/pair/<id>` | 200 with the blob, which is deleted with this answer; 404 |
| `GET /v1/group/<id>`, `Authorization: Bearer <token>` | 200 with the blob and `ETag: "<version>"`; 404; 403 wrong token |
| `PUT /v1/group/<id>` with `If-None-Match: *` (create) or `If-Match: "<version>"` (up to 256 KiB), optionally `Sync-Final: 1` | 200 with the new `ETag`; 412 changed meanwhile; 410 closed; 403; 413 |

Pairing slots expire after 10 minutes, groups 400 days after they were last read or written
(reads count once a day; a daily cron deletes). Taking a code off the screen reads its slot,
which empties it, so a code someone saw stops working at once.
`node --test relay/` tests the Worker against an in-memory database; `node relay/serve.mjs`
runs it locally, and `UNI_VPN_TEST_RELAY=<its URL> go test ./internal/devsync` runs the Go
tests against it instead of the in-memory copy.

### Deploying

`.github/workflows/relay.yml` deploys on every push to main that changes `relay/`, and by
hand. It needs two repository secrets: `CLOUDFLARE_API_TOKEN` (a token with the "Edit
Cloudflare Workers" template plus D1 Edit) and `CLOUDFLARE_ACCOUNT_ID`. It creates the D1
database on first run and prints the Worker's URL, which goes into `SyncRelay`. The free plan
covers 100,000 requests a day, about 2,000 devices at their usual pace.
