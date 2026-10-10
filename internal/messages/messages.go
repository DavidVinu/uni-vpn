// Package messages holds every text the service shows in the app. It mirrors
// uni_vpn/messages.py: same ids and actions, so the app works with either core. The wording
// lives in uni_vpn/locales/<lang>.json under "msg.<id>" (English in en.json), shared with the
// Python core, the app page and the native menus.
//
// Written for people who have never opened a terminal: plain words, no commands, and when
// there is something to do, the app offers it as a button (the action).
package messages

import (
	"github.com/DavidVinu/uni-vpn/internal/i18n"
)

// Actions the app offers. password, totp: open that field in Settings. repair: run the
// installer again. setup: show the setup assistant again.
var Actions = []string{"password", "totp", "repair", "setup"}

// Message is a text with a stable id and the fix the app offers ("" for none). A Message
// with an empty ID is a plain text, like a str in Python that is not a messages.Message.
type Message struct {
	ID     string
	Text   string
	Action string
}

// Plain wraps a text that is not one of the messages: it has no id and no action.
func Plain(text string) Message { return Message{Text: text} }

func (m Message) String() string { return m.Text }

// KeyPrefix: a message's catalog key is KeyPrefix + ID.
const KeyPrefix = "msg."

// JSON is message_t in status.json: catalog key and arguments, nil for a plain text.
func (m Message) JSON() any {
	if m.ID == "" {
		return nil
	}
	return i18n.T(KeyPrefix + m.ID).JSON()
}

var all []Message

func m(id string, action ...string) Message {
	msg := Message{ID: id, Text: i18n.T(KeyPrefix + id).String()}
	if len(action) > 0 {
		msg.Action = action[0]
	}
	all = append(all, msg)
	return msg
}

// --- Normal states ---
var (
	NotConnected  = m("not_connected")
	Disconnected  = m("disconnected")
	Connecting    = m("connecting")
	Connected     = m("connected")
	Disconnecting = m("disconnecting")
	SetupNeeded   = m("setup_needed")
	// The setup assistant shows this one while it waits (message_id "waiting_for_code").
	WaitingForCode = m("waiting_for_code")
	Blocked        = m("blocked")
	Offline        = m("offline")
)

// --- Sign-in ---
var (
	PasswordRejected = m("password_rejected", "password")
	TOTPRejected     = m("totp_rejected", "totp")
	AppendRejected   = m("append_rejected", "password")
	DuoRejected      = m("duo_rejected", "password")
	AuthRejected     = m("auth_rejected", "password")
	TOTPUnusable     = m("totp_unusable", "totp")
	SAMLRequired     = m("saml_required")
	HostScanRequired = m("hostscan_required")
)

// --- Saved password and second factor ---
var (
	PasswordMissing     = m("password_missing", "password")
	TOTPMissing         = m("totp_missing", "totp")
	KeyringLocked       = m("keyring_locked")
	PasswordUnreadable  = m("password_unreadable", "password")
	TOTPUnreadable      = m("totp_unreadable", "totp")
	PasswordUnsupported = m("password_unsupported", "password")
)

// --- Connection ---
var (
	TooSlow         = m("too_slow")
	ConnectFailed   = m("connect_failed")
	ConnectionLost  = m("connection_lost")
	ServerUntrusted = m("server_untrusted")
	PortInUse       = m("port_in_use")
)

// --- Broken installation ---
var (
	ProgramMissing = m("program_missing", "repair")
	StartFailed    = m("start_failed", "repair")
	NotElevated    = m("not_elevated", "repair")
	SettingsBroken = m("settings_broken", "setup")
	Repairing      = m("repairing")
	RepairFailed   = m("repair_failed", "repair")
)

// --- The app, when the service does not answer ---
var NotRunning = m("not_running")

// All returns every message by id.
func All() map[string]Message {
	out := make(map[string]Message, len(all))
	for _, msg := range all {
		out[msg.ID] = msg
	}
	return out
}

// ByText finds the message with this text. Other packages (the tunnel) hand over plain
// strings; the daemon turns them back into messages so their id and action reach the app.
func ByText(text string) Message {
	for _, msg := range all {
		if msg.Text == text {
			return msg
		}
	}
	return Plain(text)
}
