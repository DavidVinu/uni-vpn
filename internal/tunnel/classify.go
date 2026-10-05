package tunnel

import "strings"

// Measured on 2026-09-08 against Heidelberg's ASA: it rejects a wrong password with "Login failed."
// before it asks for the OTP. With a wrong one-time code the OTP prompt comes first
// ("Generating OATH TOTP token code"), then "Login failed.". In both cases it shows the form
// again, stdin is closed, "User input required", then "Failed to complete authentication".
// So the order decides which factor was wrong; without a separate OTP step the profile's
// second factor decides how to word it.
const (
	PasswordRejected = "Login rejected: check your password (uni-vpn password)"
	TOTPRejected     = "One-time code rejected: check the computer's clock, otherwise re-enter the TOTP secret (uni-vpn totp)"
	AppendRejected   = "Login rejected: check your password, then the computer's clock (uni-vpn password)"
	DuoRejected      = "Login rejected: check your password, or the Duo request was denied or not answered in time " +
		"(uni-vpn password)"
	// Without a preceding "Login failed." the server asked for something uni-vpn cannot fill in.
	AuthRejected = "Login rejected: check your password (uni-vpn password). " +
		"If it is correct, the server asked for something uni-vpn does not know, see uni-vpn log"
	SAMLRequired     = "This university signs in through a browser (SAML), uni-vpn does not support that yet"
	HostScanRequired = "The server requires HostScan (CSD), uni-vpn does not support that"
	TOTPUnusable     = "TOTP secret unusable, enter it again (uni-vpn totp)"
	OTPGenerated     = "Generating OATH TOTP token code"
	LoginFailed      = "Login failed"
	TokenPrefix      = "totp-"
	// MaxLine cuts longer output lines (the Python core's asyncio readline limit).
	MaxLine = 64 * 1024
)

// States a verdict can carry; the daemon uses them as its state names.
const (
	StateAuthFailed = "auth_failed"
	StateError      = "error"
)

// LoginRejected words "Login failed." by the profile's second factor.
var LoginRejected = map[string]string{"totp_append": AppendRejected, "duo_push": DuoRejected}

// Verdict is a classification of the openconnect output.
type Verdict struct {
	State   string
	Message string
}

// Marker maps a substring of the openconnect output to a verdict.
type Marker struct {
	Needle  string
	State   string
	Message string
}

// Markers are checked in order; the first match wins.
var Markers = []Marker{
	{"Server is rejecting the soft token", StateAuthFailed, TOTPRejected},
	{"Soft token string is invalid", StateAuthFailed, TOTPUnusable},
	{"User input required in non-interactive mode", StateAuthFailed, AuthRejected},
	{"Server asked us to run CSD", StateAuthFailed, HostScanRequired},
	{"Cisco Secure Desktop", StateAuthFailed, HostScanRequired},
	{"SAML", StateAuthFailed, SAMLRequired},
	{"external browser", StateAuthFailed, SAMLRequired},
	{"Failed to complete authentication", StateAuthFailed, AuthRejected},
	{"certificate", StateError, "Certificate problem on the server"},
}

// ClassifyLine returns the verdict of the first marker found in line.
func ClassifyLine(line string) (Verdict, bool) {
	lowered := strings.ToLower(line)
	for _, m := range Markers {
		if strings.Contains(lowered, strings.ToLower(m.Needle)) {
			return Verdict{m.State, m.Message}, true
		}
	}
	return Verdict{}, false
}

// Classifier classifies the openconnect output line by line; the first verdict sticks.
type Classifier struct {
	MFA          string
	OTPGenerated bool
	verdict      Verdict
	has          bool
}

func NewClassifier(mfa string) *Classifier {
	if mfa == "" {
		mfa = "totp_field"
	}
	return &Classifier{MFA: mfa}
}

// Verdict returns the sticky verdict, if any.
func (c *Classifier) Verdict() (Verdict, bool) { return c.verdict, c.has }

func (c *Classifier) Feed(line string) (Verdict, bool) {
	if c.has {
		return c.verdict, true
	}
	lowered := strings.ToLower(line)
	if strings.Contains(lowered, strings.ToLower(OTPGenerated)) {
		c.OTPGenerated = true
		return Verdict{}, false
	}
	if strings.Contains(lowered, strings.ToLower(LoginFailed)) {
		message := PasswordRejected
		if c.OTPGenerated {
			message = TOTPRejected
		} else if m, ok := LoginRejected[c.MFA]; ok {
			message = m
		}
		c.verdict, c.has = Verdict{StateAuthFailed, message}, true
	} else {
		c.verdict, c.has = ClassifyLine(line)
	}
	return c.verdict, c.has
}
