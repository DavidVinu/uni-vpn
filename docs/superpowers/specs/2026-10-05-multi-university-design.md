# uni-vpn: other Cisco universities

Status: 2026-10-05. Extends `2026-09-07-uni-vpn-design.md`. Research: `/mnt/project-files/uni-support/findings.md`.

## 1. Goal and decision

uni-vpn works for Heidelberg only: host, user agent, second factor, error texts, MFA portal
and default domains are fixed in the code. The owner decided on "university list plus
detection", like eduroam CAT (pick your institution) and Thunderbird account setup (built-in
database, then autodetect, then manual):

1. A curated registry of university profiles ships in the package. Setup shows a searchable
   picker.
2. "My university is not listed" asks for the VPN address and probes the gateway without
   credentials (group list, login fields, SAML). The result prefills the profile fields.
3. Everything stays editable in `config.toml`.

Constraints:

- Heidelberg keeps working unchanged. An existing `config.toml` without `university` means
  Heidelberg, and the openconnect command line for Heidelberg stays byte for byte the same.
- SAML (browser login) is out of scope. Profiles can say `mfa = "saml"`; setup, daemon and
  doctor then say plainly that it is not supported yet.
- Standard library only, Python 3.11+, English only, no em-dashes, minimal GUI text (hints
  behind an info icon).

Not in this round: SAML via openconnect's external browser mode, non-Cisco protocols (F5,
Pulse, GlobalProtect, Fortinet), switching universities on an existing install from the GUI.

## 2. Gateway probes (2026-10-05)

To check the registry values and to get fixtures, the openconnect init request
(`<config-auth type="init">`, AnyConnect user agent, SSO capability advertised, no
credentials) was sent to eleven gateways. Recordings are in the plan (Task 5) and become
`tests/fixtures/detect/*.xml`.

| Gateway | Group list (default first) | Login fields | SAML |
|---|---|---|---|
| vpn-ac.uni-heidelberg.de (302 to vpnsrv3) | none | username, password (OTP comes in a second form) | no |
| vpn.uni-bremen.de (302 to vpn3) | Tunnel-All-Traffic, Tunnel-Uni-Bremen | username, password, secondary_password | no |
| sslvpn.ethz.ch | staff-net, student-net | username, password, secondary_password | no |
| vpn.uni-muenster.de | none | username, password, secondary_password | no |
| vpn.uni-marburg.de | unimr-vpn-staff-Passwort+2FA and 3 more | username, password | no |
| su-vpn.stanford.edu | CardinalKey (SAML), CardinalKey-Full, Stanford, Stanford-Full, ... | default group: sso only; with group `Stanford`: username, password | default group only |
| univpn.uni-kassel.de, unibn-vpn.uni-bonn.de | none | username, password | no |
| vpn.uni-mannheim.de | WebVPN, WebVPN-Split | username, password | no |
| vpn2.zih.tu-dresden.de | A-Tunnel-TU-Networks and 11 more | username, password | no |
| vpn.tik.uni-stuttgart.de | USTUTT | username, password | no |
| vpn.rc.fas.harvard.edu | none | username, password, secondary_password | no |
| vpn.fu-berlin.de, vpn.ox.ac.uk | none | sso only | yes |

What this means for the design:

- Every server answers `<auth-method>single-sign-on-v2</auth-method>` inside `<opaque>` when
  the client advertises SSO, Heidelberg included. SAML is detected only by `<sso-v2-login>`
  or an `<input type="sso">`, never by `auth-method`.
- Load balancers answer the POST with a 302 to a cluster member. urllib turns a redirected
  POST into a GET without a body, so detection re-posts the same body itself.
- Heidelberg shows the OTP field only after the password, so detection cannot see every
  second factor. Its MFA guess is a guess (see 6).
- The group decides the login method at Stanford (default group is SAML, `Stanford` is a
  password form), so detection probes again when the user picks another group.
- Marburg's group is `unimr-vpn-staff-Passwort+2FA` (findings.md says "Password").
- TU Dresden has no group `TUD-vpn-all`; the default is `A-Tunnel-TU-Networks`.

## 3. Profile schema and registry

The registry is `uni_vpn/universities.json`, read with `json` by `uni_vpn/universities.py`.
Contributors add a university by pull request.

```json
{
  "version": 1,
  "universities": [
    {
      "id": "heidelberg",
      "name": "Heidelberg University",
      "country": "DE",
      "host": "vpn-ac.uni-heidelberg.de",
      "usergroup": "",
      "authgroup": "",
      "username_suffix": "",
      "useragent": "AnyConnect Linux_64 5.1.18.314",
      "os": "",
      "no_external_auth": false,
      "mfa": "totp_field",
      "totp_separator": "",
      "mfa_portal_url": "https://mfa.uni-heidelberg.de/",
      "mfa_steps": ["Open the {portal}. It only opens on campus or with Cisco VPN.", "..."],
      "default_domains": ["sogo.uni-heidelberg.de", "..."],
      "aliases": ["Universitaet Heidelberg", "Ruprecht-Karls-Universitaet", "URZ"],
      "verified": true,
      "notes": ""
    }
  ]
}
```

| Field | Type, default | Meaning |
|---|---|---|
| `id` | string, required | `[a-z0-9-]`, unique; `other` is reserved |
| `name`, `country` | string, required / `""` | shown in the picker; country is ISO 3166 alpha-2 |
| `host` | string, required | gateway, optionally `:port`; a path is allowed for old configs but new entries use `usergroup` |
| `usergroup` | string, `""` | URL path group, `--usergroup` |
| `authgroup` | string, `""` | group-select value, `--authgroup`; set only when it changes the login method or realm (ETH, Marburg, Stanford), not for split vs full tunnel, which does not matter behind ocproxy |
| `username_suffix` | string, `""` | appended to the user name on the command line unless the user name already contains `@` |
| `useragent` | string, `AnyConnect Linux_64 5.1.18.314` | `--useragent` |
| `os` | `""`, `linux`, `linux-64`, `win`, `mac-intel`, `android`, `apple-ios` | `--os` when not empty |
| `no_external_auth` | bool, `true` | `--no-external-auth` (openconnect 9.10+) |
| `mfa` | `none`, `totp_field`, `totp_append`, `duo_push`, `saml` | second factor, see 5 |
| `totp_separator` | string, `""` | with `totp_append`: between password and code |
| `mfa_portal_url` | `https://` URL or `""` | link in the assistant and the terminal hint |
| `mfa_steps` | list of strings, `[]` | the assistant's TOTP steps; `{portal}` becomes the portal link. Empty: generic steps |
| `default_domains` | list of host names, `[]` | domains when `domains.txt` does not exist |
| `aliases` | list of strings, `[]` | extra search terms |
| `verified` | bool, `false` | someone logged in end to end with this profile |
| `notes` | string, `""` | for contributors (sources, student variants); not served to the page |

`mfa_steps` is not in the owner's field list. It is added so that Heidelberg's step list
(LinOTP button names) moves from `index.html` into its profile instead of being lost.

Heidelberg sets `no_external_auth` to `false`, against the schema default: today's command
does not pass the flag, Debian 12 ships openconnect 9.01 without it, and Heidelberg answers
with a password form either way.

Initial entries (values from findings.md, corrected by the probes in 2):

| id | host | authgroup | suffix | ua, os | mfa | verified |
|---|---|---|---|---|---|---|
| heidelberg | vpn-ac.uni-heidelberg.de | | | default | totp_field | yes |
| ethz | sslvpn.ethz.ch | staff-net | @staff-net.ethz.ch | AnyConnect | totp_field | no |
| bremen | vpn.uni-bremen.de | | | AnyConnect | totp_field | no |
| muenster | vpn.uni-muenster.de | | | default | totp_field | no |
| marburg | vpn.uni-marburg.de | unimr-vpn-staff-Passwort+2FA | | default | totp_append | no |
| stanford | su-vpn.stanford.edu | Stanford | | default | duo_push | no |
| harvard-fasrc | vpn.rc.fas.harvard.edu | | @fasrc | default | totp_field | no |
| stuttgart | vpn.tik.uni-stuttgart.de | | @uni-stuttgart.de | default | none | no |
| bonn | unibn-vpn.uni-bonn.de | | | AnyConnect | none | no |
| mannheim | vpn.uni-mannheim.de | | | default | none | no |
| kassel | univpn.uni-kassel.de | | | AnyConnect Windows 5.1.18.314, win | none | no |
| tu-dresden | vpn2.zih.tu-dresden.de | | | default | none | no |
| fu-berlin | vpn.fu-berlin.de | | | AnyConnectLinux | saml | no |
| oxford | vpn.ox.ac.uk | | | default | saml | no |

Only Heidelberg has default domains and an MFA portal; the others start with empty lists
until someone contributes them.

## 4. Configuration

`config.toml` gains `university = "<id>"`. The ten connection fields (`host`, `usergroup`,
`authgroup`, `username_suffix`, `useragent`, `os`, `no_external_auth`, `mfa`,
`totp_separator`, `mfa_portal_url`) may appear as top-level keys and override the profile:

```toml
university = "ethz"
user = "jdoe"
username_suffix = "@student-net.ethz.ch"
authgroup = "student-net"
```

Loading: read `university` (missing means `heidelberg`), copy the profile into `Config`,
then apply the keys in the file, each checked with the same validator as the registry. An
unknown id is a `ConfigError` with its line. `university = "other"` starts from an empty
profile (`mfa = "none"`, `no_external_auth = true`) and needs `host`.

`Config()` defaults equal the Heidelberg profile, so code paths that build a `Config` without
a file (broken config, setup assistant) keep today's values. Existing files written by the old
template contain `host = "vpn-ac.uni-heidelberg.de"`, which is the profile value anyway.

New files: `write_initial(path, user, university, overrides)` writes `university`, `user`
and the overrides; `host` only appears when it is an override, so registry fixes reach
existing installs. `set_values(path, {...})` generalizes `set_user`.

User names may now carry a realm: `ab123` or `ab123@stud.uni-stuttgart.de`
(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}(@[A-Za-z0-9][A-Za-z0-9.-]{0,252})?$`). The keyring entry
stays keyed by the user name as typed.

## 5. openconnect command per profile

`Tunnel.auth_args()` builds the login part for both `Tunnel` and `WindowsTunnel`:

```
--protocol=anyconnect --useragent=<useragent> --user=<user><suffix> --passwd-on-stdin
[--non-inter]                     all modes except duo_push
[--authgroup=<authgroup>]         when set
[--usergroup=<usergroup>]         when set
[--os=<os>]                       when set
[--no-external-auth]              when no_external_auth
```

followed by the unchanged transport flags and `--script...`, then the token flags, then the
host.

| mfa | TOTP secret | token flags | stdin |
|---|---|---|---|
| `none` | not used, optional everywhere | none | `password\n` |
| `totp_field` | required | `--token-mode=totp --token-secret=@<0600 file>` | `password\n` |
| `totp_append` | required | none | `password + separator + code\n`; uni-vpn computes the code with `totp.code()` at start and records the time as `otp_generated_at` |
| `duo_push` | not used | none | `password\npush\n` |
| `saml` | not used | openconnect is never started | |

`duo_push` drops `--non-inter`: openconnect reads only the first line for
`--passwd-on-stdin` and asks for the second password through its prompt, which `--non-inter`
refuses. stdin is still closed after writing, so any further prompt ends at EOF instead of
blocking. This is unverified until someone tests it against Stanford.

The 30-second one-time-code wait (`OTP_STEP`) applies only to `totp_field` and
`totp_append`.

## 6. Error texts

The classifier gets the MFA mode. "Login failed." after "Generating OATH TOTP token code"
still means the code was wrong. Without it:

| mfa | message |
|---|---|
| `none`, `totp_field` | Login rejected: check your password (uni-vpn password) |
| `totp_append` | Login rejected: check your password, then the computer's clock (uni-vpn password) |
| `duo_push` | Login rejected: check your password, or the Duo request was denied or not answered in time (uni-vpn password) |

SAML and "external browser" markers say "This university signs in through a browser (SAML),
uni-vpn does not support that yet"; CSD markers say "The server requires HostScan (CSD),
uni-vpn does not support that". No message names Heidelberg.

## 7. Detection

`uni_vpn/detect.py`, stdlib `urllib.request` and `xml.etree.ElementTree`:

- `split_address(text) -> (host, usergroup)` accepts `vpn.x.edu`, `https://vpn.x.edu/group`.
- `probe(host, usergroup="", group="", opener=...) -> Detection` POSTs openconnect's init
  request to `https://host/usergroup` with the AnyConnect user agent and
  `X-Transcend-Version: 1`, `X-Aggregate-Auth: 1`. A redirect handler re-posts to `https://`
  locations (at most 3). Replies over 256 KB or with a DOCTYPE are refused.
- `parse_reply(data, host, usergroup) -> Detection`: `cisco` (root is `config-auth`), `saml`
  (`sso-v2-login` or an `sso` input), `groups` and `group` (from `group_list`, else
  `opaque/group-alias`), `fields` (`name`, `type`, `label` of text and password inputs),
  `second_password` (`secondary_password` present), `message`, `error`.
- `Detection.suggestion()`: `mfa` is `saml`, else `totp_field` with a second password field,
  else `none`; `authgroup` is the selected group when there is more than one.

It cannot tell `totp_append`, `duo_push` or a second form (Heidelberg) from a plain
password, nor know the MFA portal or useful domains, so the assistant shows the guess in an
editable field.

## 8. Daemon, API, doctor, CLI

- Daemon: no TOTP read unless the mode needs it; `mfa = "saml"` ends in `auth_failed` with
  the SAML message before anything starts; domains default to `cfg.default_domains`.
  `/status.json` adds `university`, `university_name`, `mfa`, `mfa_portal_url`, `mfa_steps`.
- `GET /universities.json`: the registry without `notes`. `POST /api/detect` with
  `{host, group}` runs the probe in an executor (CSRF header and origin as for every POST).
- `POST /api/setup` takes `university` (default `heidelberg`), optional `profile` overrides
  for `other`, and `secret` only when the mode needs TOTP. Errors name the field (`university`,
  `host`, `usergroup`, `authgroup`, `mfa`, `user`, `password`, `totp`) so the assistant opens
  the right step.
- Doctor: the second-factor check follows the mode (`saml` fails, `none`/`duo_push` are ok
  without a keyring probe); openconnect older than 9.10 with `no_external_auth` warns.
- CLI: `uni-vpn totp` says when the university uses no TOTP. `setup --university <id>`
  (also `install.sh --university`, `install.ps1 -University`) skips the question and is
  passed to the assistant as `?university=`.
- Terminal setup without a desktop asks "Your university" (name, part of it, id, Enter for
  Heidelberg, `other` to enter an address and probe it).

## 9. Setup assistant

Role models: eduroam CAT (institution search), Thunderbird account setup (list, then
detect, then manual), Google sign-in (one question per card).

1. University: one search field (name, alias, host, id; accents ignored) with a listbox of
   matches and "Not listed" at the end. "Not listed" shows a VPN address field and a
   Detect button; after detection a group select (when there are several) and a second-factor
   select appear, prefilled. Picking another group detects again. A SAML profile or group
   shows one error line and blocks Next. Help text sits behind the info icon.
2. Sign in: Username and password (label "Username" instead of "Uni-ID").
3. Second factor: only for `totp_field` and `totp_append`. The step list comes from
   `mfa_steps` with the portal link from `mfa_portal_url`, else three generic steps.
4. Connecting, then done. With an empty domain list the done card points to Settings,
   Websites.

Step numbers adapt ("Step 1 of 3" without TOTP). Settings show the university and hide
"Second factor" when the mode has none.

## 10. Tests

- Registry: loads, ids unique, every value valid, only Heidelberg verified, SAML entries
  present, validator errors carry the field name, README lists every university.
- Config: missing `university` is Heidelberg, overrides win, unknown id and bad values report
  the line, `Config()` equals the Heidelberg profile, `write_initial` and `set_values` round
  trip.
- Tunnel: Heidelberg's command line is compared as a whole list against today's; flags,
  suffix and stdin per mode; the Windows command uses the same login flags.
- Detection: parsing against the recorded fixtures, request format, redirect re-post,
  unreachable and non-Cisco servers.
- Daemon, API, setup, doctor, CLI: each mode end to end with the fake openconnect (which now
  records every stdin line and its arguments).

## 11. Open points

- `duo_push` without `--non-inter` and `totp_append` are untested against real servers.
- Bremen without 2FA leaves the second field empty; `mfa = "none"` will then hit "User input
  required". Needs a `totp_optional` mode or `--form-entry` if someone reports it.
- Kassel's Windows user agent string is a guess.
- Registry entries go stale; `verified` and the probes make that visible but do not fix it.
