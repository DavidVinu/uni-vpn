"""Every text the service shows in the app, in one place.

Written for people who have never opened a terminal: plain words, no commands, and when there is
something to do, the app offers it as a button (the action). Role models: Tailscale and the
macOS and Windows VPN settings, which say what is wrong in one sentence and put the fix on a
button next to it ("Log in", "Try again", "Troubleshoot").

The wording lives in locales/<lang>.json under "msg.<id>" (English in en.json), so the app, the
native menus and a future core read the same texts. Each message is an i18n.Text, a str in English,
so it compares and logs like one, and also carries
- id: a stable name, for tests, independent of the wording; the catalog key is "msg." + id;
- action: the fix the app offers, one of ACTIONS, or None when there is nothing to click.
"""

from __future__ import annotations

from .i18n import Text

KEY_PREFIX = "msg."

# password, totp: open that field in Settings. repair: run the installer again (the operating
# system asks for permission). setup: show the setup assistant again.
ACTIONS = ("password", "totp", "repair", "setup")


class Message(Text):
    id: str
    action: str | None

    def __new__(cls, id: str, action: str | None = None) -> "Message":
        if action is not None and action not in ACTIONS:
            raise ValueError(f"unknown action {action!r}")
        message = super().__new__(cls, KEY_PREFIX + id)
        message.id = id
        message.action = action
        return message

    def __reduce__(self):
        return Message, (self.id, self.action)


def _m(id: str, action: str | None = None) -> Message:
    return Message(id, action)


# --- Normal states -----------------------------------------------------------
NOT_CONNECTED = _m("not_connected")
DISCONNECTED = _m("disconnected")
CONNECTING = _m("connecting")
CONNECTED = _m("connected")
DISCONNECTING = _m("disconnecting")
SETUP_NEEDED = _m("setup_needed")
# The setup assistant shows this one while it waits (message_id "waiting_for_code").
WAITING_FOR_CODE = _m("waiting_for_code")
BLOCKED = _m("blocked")
OFFLINE = _m("offline")

# --- Sign-in -----------------------------------------------------------------
PASSWORD_REJECTED = _m("password_rejected", "password")
TOTP_REJECTED = _m("totp_rejected", "totp")
APPEND_REJECTED = _m("append_rejected", "password")
DUO_REJECTED = _m("duo_rejected", "password")
AUTH_REJECTED = _m("auth_rejected", "password")
TOTP_UNUSABLE = _m("totp_unusable", "totp")
SAML_REQUIRED = _m("saml_required")
HOSTSCAN_REQUIRED = _m("hostscan_required")

# --- Saved password and second factor ------------------------------------------
PASSWORD_MISSING = _m("password_missing", "password")
TOTP_MISSING = _m("totp_missing", "totp")
KEYRING_LOCKED = _m("keyring_locked")
PASSWORD_UNREADABLE = _m("password_unreadable", "password")
TOTP_UNREADABLE = _m("totp_unreadable", "totp")
PASSWORD_UNSUPPORTED = _m("password_unsupported", "password")

# --- Connection ----------------------------------------------------------------
TOO_SLOW = _m("too_slow")
CONNECT_FAILED = _m("connect_failed")
CONNECTION_LOST = _m("connection_lost")
SERVER_UNTRUSTED = _m("server_untrusted")
PORT_IN_USE = _m("port_in_use")

# --- Broken installation ------------------------------------------------------------
PROGRAM_MISSING = _m("program_missing", "repair")
START_FAILED = _m("start_failed", "repair")
NOT_ELEVATED = _m("not_elevated", "repair")
SETTINGS_BROKEN = _m("settings_broken", "setup")
REPAIRING = _m("repairing")
REPAIR_FAILED = _m("repair_failed", "repair")

# --- The app, when the service does not answer ---------------------------------------
NOT_RUNNING = _m("not_running")


def all_messages() -> dict[str, Message]:
    return {value.id: value for value in globals().values() if isinstance(value, Message)}


def action_of(text: str) -> str | None:
    return getattr(text, "action", None)


def id_of(text: str) -> str | None:
    return getattr(text, "id", None)
