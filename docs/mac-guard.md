# Mac: the guard of the proxy rule

Chosen by David on 2026-11-24 ("Add an admin helper on Macs"), for SV-1 in BUG_BURN_DOWN.md.

## Why

On a Mac the proxy rule (the automatic proxy setting of each network service) is the
computer's, not the account's, and it points at `http://127.0.0.1:1081/proxy.pac`, a port any
account may take. Every account's browsers take their rule from whatever answers there. So far
the account at hand changed the rule itself with `networksetup`: on, off while another account
holds the port (FA-8), a new address when the websites change. macOS lets only an
administrator do that, so on a standard account the core cannot switch the rule off while
another account's program holds the port (SV-1), cannot cover a service added since, and
setup, Repair and removal need an administrator's password (SX-2). While the Uni VPN user is
signed out, the rule stays on for the other accounts too. FA-8, FV-1, SX-1 and SV-1 were all
this one design gap.

## What

A small part of the core, `uni-vpn-core guard`, runs as root (a LaunchDaemon) and is the only
one that switches our rule on and off. Accounts tell it that they want the rule; it decides
from what it sees, not from what it is told.

**The rule is on** for port P and version V exactly when the account at the screen (the owner
of `/dev/console`, not root) asked for the rule with port P and the program that listens on
127.0.0.1:P belongs to that account (the system's table of sockets, as in SX-1). Otherwise our
rule is off.

- On: every enabled service whose rule is ours (any version) or unset gets
  `http://127.0.0.1:P/proxy.pac?v=V`.
- Off: every service with our rule on gets it switched off (the address stays, as Withdraw
  left it).
- A setting that is not ours is never touched by the guard. Replacing one stays the user's
  step (setup, with an administrator's password where macOS asks), and uninstall puts it back.

So another account's program on the port never gets the browsers of the account at the screen,
whether that account is an administrator or not, and while nobody with Uni VPN is at the screen
the rule is off.

## Talking to it

A Unix socket, `/var/run/de.davidvinu.uni-vpn.guard.sock`, mode 0666, one request line per
connection; the guard names the account by the socket's credentials (`LOCAL_PEERCRED`), never
by what the request says.

| Request | Meaning | Answer |
|---|---|---|
| `want P` | this account wants the rule at port P (1024 to 65535) | `on V`, `off` or `held` |
| `refresh` | a new address for this account's rule (the websites changed) | as `want` |
| `unwant` | this account no longer wants it (uninstall) | `off` |
| `state` | what it does for this account | as `want` |
| `version` | the guard's version (`sysproxy.GuardVersion`) | a number |

Each account has one entry, kept in `/Library/Application Support/Uni VPN/guard.json` (root,
0600) so the rule is right from boot. A request is at most 64 bytes, read within 2 s (before
it waits for the guard's networksetup), at most 32 connections at once and 4 of one account
(also while the guard is busy). Answers are for the asking account only.

Our rule is an address `http://127.0.0.1:P/proxy.pac` (with or without `?v=`) at 1081, at the
port of any account's entry, or at one of the last 16 ports an entry had. The last list may
forget a port (any account can name many); 1081 and the entries' ports are never forgotten.

## When it looks

Every 2 s it reads the owner of `/dev/console` and the listener of that account's port (no
programs run). `networksetup` runs when the rule should change (also a new address from
`refresh`), at a `want` (setup may just have put our address over another setting), at most
once a second for that, and once a minute (a service added since, a change by someone else).
Settings it could not read are read again at the next look.

## Who uses it

- Setup and Repair (`sysproxy.Install`): back up the current setting as before, replace a
  setting that is not ours as before (directly, or with an administrator's password), then
  `want`.
- Uninstall (`sysproxy.Uninstall`): `unwant`, then put back only the settings that were not
  ours; the guard switches ours off. The backup never holds one of our rules (any account's,
  or ours that the guard switched on for the core just started): it records them as unset, so
  a removal after the guard has gone switches ours off and never puts it back.
- The core: `want` when it starts and again now and then (Cover: every few minutes and after a
  network change), `refresh` when the websites change. Covering new services, Withdraw and
  putting the rule back are the guard's; the core does nothing there.
- Without a guard (it is not installed, or does not answer), everything works as before.

## Installing and removing it

- The .pkg's postinstall (root) copies the core to
  `/Library/PrivilegedHelperTools/de.davidvinu.uni-vpn.guard`, writes
  `/Library/LaunchDaemons/de.davidvinu.uni-vpn.guard.plist` and starts it, before it sets up the
  user. The installer already asks for an administrator, so a new install never asks again.
- Over a running guard (a .pkg over this one, Repair replacing an older guard), `guard
  install` boots the old one out and starts the new one, again for up to 25 s while the old
  one is still going away (one busy with networksetup ends only after it).
- Each install also adopts the accounts set up before it: every home folder in `/Users` with
  its own `~/Library/LaunchAgents/de.davidvinu.uni-vpn.plist` gets an entry for the port in
  its `~/.config/uni-vpn/config.toml` (1081 without one), unless it has one. Their cores may be
  older than the guard and never say `want` (with automatic updates off they stay so); without
  an entry the guard would keep their rule off.
- A Mac set up before, or with a guard older than the core's (`version` below
  `sysproxy.GuardVersion`, which is raised with every change to the guard that must reach every
  Mac): setup and Repair install it before the service starts, with one administrator's
  password (the same macOS dialog as SX-2): `uni-vpn-core guard install` of the account's own
  copy, as root. There a standard account, which cannot switch the rule off itself, connects
  only after that Repair: the app says so (`msg.guard_missing`) and offers the button. An
  administrator's account keeps working as before (it can switch the rule off) until its next
  Repair or .pkg.
- Switched off under System Settings, General, Login Items ("Allow in the Background"), the
  guard cannot start, and Repair cannot help: the Mac app says so (`menu.guard_off`) and opens
  that page for an administrator.
- When the app has gone to the Trash for 10 minutes, the guard switches our rule off and
  removes itself and its log; each account's core removes itself as before, and puts back only
  what differs from its backup (so a standard account is asked for no password there). A guard set up without the app
  (`get.sh`, a clone) has no Trash to watch: it removes itself once the last account that wanted
  the rule said `unwant`.

## Limits

- While another account is at the screen, the browsers left open in the background of a
  switched-away account follow the rule of the account at the screen.
- The guard is updated with the .pkg or by Repair, not by the automatic updates: it runs as
  root, and the account's own copy of the core, which the automatic updates replace, is the
  account's to change. A newer `GuardVersion` makes a standard account ask for Repair.

## Tests

- Unit (any system, fakes): the policy table (console, wanted, listener), the commands for on
  and off, foreign settings untouched, the protocol (credentials, limits, bad lines), the file.
- `sysproxy` with a fake guard: Install, Uninstall, Refresh, Cover and Withdraw.
- CI on the Mac (macos-pkg): after the install the guard runs and the rule is on; with the core
  stopped and a program of `nobody` on 127.0.0.1:1081 the rule goes off within seconds; with
  the core back it is on again; after the Trash, the guard and its files are gone.
