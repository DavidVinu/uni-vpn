// Package messages holds every text the service shows in the app. It mirrors
// uni_vpn/messages.py: same ids, texts and actions, so the app and translations work with
// either core.
//
// Written for people who have never opened a terminal: plain words, no commands, and when
// there is something to do, the app offers it as a button (the action).
package messages

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

var all []Message

func m(id, text string, action ...string) Message {
	msg := Message{ID: id, Text: text}
	if len(action) > 0 {
		msg.Action = action[0]
	}
	all = append(all, msg)
	return msg
}

// --- Normal states ---
var (
	NotConnected  = m("not_connected", "Not connected")
	Disconnected  = m("disconnected", "Disconnected")
	Connecting    = m("connecting", "Connecting")
	Connected     = m("connected", "Connected")
	Disconnecting = m("disconnecting", "Disconnecting")
	SetupNeeded   = m("setup_needed", "Setup needed")
	// The app looks for "one-time code" to show this while the setup assistant waits.
	WaitingForCode = m("waiting_for_code", "Waiting a few seconds for a new one-time code")
	Blocked        = m("blocked", "Paused while Cisco Secure Client is connected")
	Offline        = m("offline", "No internet. On public Wi-Fi, sign in to the Wi-Fi first.")
)

// --- Sign-in ---
var (
	PasswordRejected = m("password_rejected", "Your university did not accept the password.", "password")
	TOTPRejected     = m("totp_rejected", "The one-time code was not accepted. Check that the computer's clock is "+
		"right, or set up the second factor again.", "totp")
	AppendRejected = m("append_rejected", "Your university did not accept the password or the one-time code. "+
		"Check the password and the computer's clock.", "password")
	DuoRejected = m("duo_rejected", "Sign-in failed. Check the password, and confirm the Duo request on "+
		"your phone in time.", "password")
	AuthRejected = m("auth_rejected", "Sign-in failed. Check the password. If it is right, your university "+
		"asks for something Uni VPN cannot answer yet.", "password")
	TOTPUnusable     = m("totp_unusable", "The saved second factor does not work. Set it up again.", "totp")
	SAMLRequired     = m("saml_required", "Your university signs in through a web page. Uni VPN cannot do that yet.")
	HostScanRequired = m("hostscan_required", "Your university checks the computer before it connects. "+
		"Uni VPN cannot do that yet.")
)

// --- Saved password and second factor ---
var (
	PasswordMissing = m("password_missing", "No password saved yet.", "password")
	TOTPMissing     = m("totp_missing", "The second factor is not set up yet.", "totp")
	KeyringLocked   = m("keyring_locked", "Your saved passwords are locked. Sign out of the computer and back "+
		"in, then click Try again.")
	PasswordUnreadable = m("password_unreadable", "The saved password could not be read. Enter it again.",
		"password")
	TOTPUnreadable      = m("totp_unreadable", "The saved second factor could not be read. Set it up again.", "totp")
	PasswordUnsupported = m("password_unsupported", "This password contains a character Uni VPN cannot send "+
		"on this computer. Change the password at your university, then enter it here.", "password")
)

// --- Connection ---
var (
	TooSlow         = m("too_slow", "Your university did not answer in time. Uni VPN tries again.")
	ConnectFailed   = m("connect_failed", "Could not connect to your university. Uni VPN tries again.")
	ConnectionLost  = m("connection_lost", "The connection to your university was lost.")
	ServerUntrusted = m("server_untrusted", "Your university's server failed a security check. "+
		"Uni VPN tries again.")
	PortInUse = m("port_in_use", "Another program is in the way. Uni VPN keeps trying. If this stays, "+
		"restart the computer.")
)

// --- Broken installation ---
var (
	ProgramMissing = m("program_missing", "Part of Uni VPN is missing. Click Repair.", "repair")
	StartFailed    = m("start_failed", "Uni VPN could not start the connection. Click Repair.", "repair")
	NotElevated    = m("not_elevated", "Uni VPN is missing a permission it needs. Click Repair.", "repair")
	SettingsBroken = m("settings_broken", "Your settings could not be read. Set up Uni VPN again.", "setup")
	Repairing      = m("repairing", "Repairing. Allow it if your computer asks.")
	RepairFailed   = m("repair_failed", "The repair did not finish. Click Repair to try again, or restart "+
		"the computer.", "repair")
)

// --- The app, when the service does not answer ---
var NotRunning = m("not_running", "Uni VPN is not running. Restart the computer to start it again.")

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
