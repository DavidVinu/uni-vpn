"""Every text the service shows in the app, in one place.

Written for people who have never opened a terminal: plain words, no commands, and when there is
something to do, the app offers it as a button (the action). Role models: Tailscale and the
macOS and Windows VPN settings, which say what is wrong in one sentence and put the fix on a
button next to it ("Log in", "Try again", "Troubleshoot").

Each message is a str, so it compares and logs like one, and also carries
- id: a stable name, for translations and tests, independent of the English wording;
- action: the fix the app offers, one of ACTIONS, or None when there is nothing to click.
"""

from __future__ import annotations

# password, totp: open that field in Settings. repair: run the installer again (the operating
# system asks for permission). setup: show the setup assistant again.
ACTIONS = ("password", "totp", "repair", "setup")


class Message(str):
    id: str
    action: str | None

    def __new__(cls, id: str, text: str, action: str | None = None) -> "Message":
        if action is not None and action not in ACTIONS:
            raise ValueError(f"unknown action {action!r}")
        message = super().__new__(cls, text)
        message.id = id
        message.action = action
        return message


def _m(id: str, text: str, action: str | None = None) -> Message:
    return Message(id, text, action)


# --- Normal states -----------------------------------------------------------
NOT_CONNECTED = _m("not_connected", "Not connected")
DISCONNECTED = _m("disconnected", "Disconnected")
CONNECTING = _m("connecting", "Connecting")
CONNECTED = _m("connected", "Connected")
DISCONNECTING = _m("disconnecting", "Disconnecting")
SETUP_NEEDED = _m("setup_needed", "Setup needed")
# The app looks for "one-time code" to show this while the setup assistant waits.
WAITING_FOR_CODE = _m("waiting_for_code", "Waiting a few seconds for a new one-time code")
BLOCKED = _m("blocked", "Paused while Cisco Secure Client is connected")
OFFLINE = _m("offline", "No internet. On public Wi-Fi, sign in to the Wi-Fi first.")

# --- Sign-in -----------------------------------------------------------------
PASSWORD_REJECTED = _m("password_rejected", "Your university did not accept the password.", "password")
TOTP_REJECTED = _m("totp_rejected", "The one-time code was not accepted. Check that the computer's clock is "
                   "right, or set up the second factor again.", "totp")
APPEND_REJECTED = _m("append_rejected", "Your university did not accept the password or the one-time code. "
                     "Check the password and the computer's clock.", "password")
DUO_REJECTED = _m("duo_rejected", "Sign-in failed. Check the password, and confirm the Duo request on "
                  "your phone in time.", "password")
AUTH_REJECTED = _m("auth_rejected", "Sign-in failed. Check the password. If it is right, your university "
                   "asks for something Uni VPN cannot answer yet.", "password")
TOTP_UNUSABLE = _m("totp_unusable", "The saved second factor does not work. Set it up again.", "totp")
SAML_REQUIRED = _m("saml_required", "Your university signs in through a web page. Uni VPN cannot do that yet.")
HOSTSCAN_REQUIRED = _m("hostscan_required", "Your university checks the computer before it connects. "
                       "Uni VPN cannot do that yet.")

# --- Saved password and second factor ------------------------------------------
PASSWORD_MISSING = _m("password_missing", "No password saved yet.", "password")
TOTP_MISSING = _m("totp_missing", "The second factor is not set up yet.", "totp")
KEYRING_LOCKED = _m("keyring_locked", "Your saved passwords are locked. Sign out of the computer and back "
                    "in, then click Try again.")
PASSWORD_UNREADABLE = _m("password_unreadable", "The saved password could not be read. Enter it again.",
                         "password")
TOTP_UNREADABLE = _m("totp_unreadable", "The saved second factor could not be read. Set it up again.", "totp")
PASSWORD_UNSUPPORTED = _m("password_unsupported", "This password contains a character Uni VPN cannot send "
                          "on this computer. Change the password at your university, then enter it here.",
                          "password")

# --- Connection ----------------------------------------------------------------
TOO_SLOW = _m("too_slow", "Your university did not answer in time. Uni VPN tries again.")
CONNECT_FAILED = _m("connect_failed", "Could not connect to your university. Uni VPN tries again.")
CONNECTION_LOST = _m("connection_lost", "The connection to your university was lost.")
SERVER_UNTRUSTED = _m("server_untrusted", "Your university's server failed a security check. "
                      "Uni VPN tries again.")
PORT_IN_USE = _m("port_in_use", "Another program is in the way. Uni VPN keeps trying. If this stays, "
                 "restart the computer.")

# --- Broken installation ------------------------------------------------------------
PROGRAM_MISSING = _m("program_missing", "Part of Uni VPN is missing. Click Repair.", "repair")
START_FAILED = _m("start_failed", "Uni VPN could not start the connection. Click Repair.", "repair")
NOT_ELEVATED = _m("not_elevated", "Uni VPN is missing a permission it needs. Click Repair.", "repair")
SETTINGS_BROKEN = _m("settings_broken", "Your settings could not be read. Set up Uni VPN again.", "setup")
REPAIRING = _m("repairing", "Repairing. Allow it if your computer asks.")
REPAIR_FAILED = _m("repair_failed", "The repair did not finish. Click Repair to try again, or restart "
                   "the computer.", "repair")

# --- The app, when the service does not answer ---------------------------------------
NOT_RUNNING = _m("not_running", "Uni VPN is not running. Restart the computer to start it again.")


def all_messages() -> dict[str, Message]:
    return {value.id: value for value in globals().values() if isinstance(value, Message)}


def action_of(text: str) -> str | None:
    return getattr(text, "action", None)


def id_of(text: str) -> str | None:
    return getattr(text, "id", None)
