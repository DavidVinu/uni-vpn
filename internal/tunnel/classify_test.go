package tunnel

import (
	"strings"
	"testing"

	"github.com/DavidVinu/uni-vpn/internal/messages"
)

// action is the fix the app offers for a verdict's message.
func action(v Verdict) string { return messages.ByText(v.Message).Action }

func mustClassify(t *testing.T, line string) Verdict {
	t.Helper()
	v, ok := ClassifyLine(line)
	if !ok {
		t.Fatalf("no verdict for %q", line)
	}
	return v
}

func TestMarkers(t *testing.T) {
	if v := mustClassify(t, "User input required in non-interactive mode"); v.State != "auth_failed" || v.Message != messages.AuthRejected.Text {
		t.Fatal(v)
	}
	if action(mustClassify(t, "Failed to complete authentication")) != "password" {
		t.Fatal()
	}
	if mustClassify(t, "Error: Server asked us to run CSD hostscan.").Message != messages.HostScanRequired.Text {
		t.Fatal()
	}
	if mustClassify(t, "SAML authentication required").State != "auth_failed" {
		t.Fatal()
	}
	if mustClassify(t, "Server certificate verify failed").State != "error" {
		t.Fatal()
	}
	if _, ok := ClassifyLine("Connected as 10.0.0.2"); ok {
		t.Fatal("unexpected verdict")
	}
}

func TestWrongPasswordAndInputRequiredShareMessage(t *testing.T) {
	// Finding 6: with --non-inter a wrong password first produces
	// "User input required", then "Failed to complete authentication".
	// Both markers must yield the same honest message.
	in := mustClassify(t, "User input required in non-interactive mode")
	af := mustClassify(t, "Failed to complete authentication")
	if in.State != "auth_failed" || af.State != "auth_failed" || in.Message != af.Message {
		t.Fatal(in, af)
	}
	if !strings.Contains(in.Message, "password") || action(in) != "password" {
		t.Fatal(in)
	}
}

func TestInvalidSoftTokenStringOffersTheSecondFactor(t *testing.T) {
	// Real openconnect 9.12 with a broken secret file: "Invalid base32 token string",
	// then "Soft token string is invalid", exit code 1 before any network contact.
	v := mustClassify(t, "Soft token string is invalid")
	if v.State != "auth_failed" || action(v) != "totp" {
		t.Fatal(v)
	}
}

func TestRejectedSoftTokenNamesTheSecondFactor(t *testing.T) {
	// If a server shows the OTP form again, openconnect tries two codes and then
	// reports "switching to manual entry"; "User input required" and
	// "Failed to complete authentication" follow. The first match counts.
	v := mustClassify(t, "Server is rejecting the soft token; switching to manual entry")
	if v.State != "auth_failed" {
		t.Fatal(v)
	}
	if !strings.Contains(v.Message, "one-time code") || !strings.Contains(v.Message, "clock") || action(v) != "totp" {
		t.Fatal(v)
	}
}

func TestLoginFailedBeforeOTPMeansPassword(t *testing.T) {
	// Measured on 2026-09-08: the ASA rejects a wrong password before it asks for the OTP.
	seq := NewClassifier("")
	if _, ok := seq.Feed("Bitte geben Sie ihren Benutzernamen und ihr Passwort ein."); ok {
		t.Fatal()
	}
	v, _ := seq.Feed("Login failed.")
	if v.State != "auth_failed" || !strings.Contains(v.Message, "password") || action(v) != "password" ||
		strings.Contains(v.Message, "one-time code") {
		t.Fatal(v)
	}
}

func TestLoginFailedAfterOTPMeansSecondFactor(t *testing.T) {
	// Measured on 2026-09-08: with a wrong secret the OTP prompt comes first, openconnect
	// generates the code, then "Login failed." and the form starts over.
	seq := NewClassifier("")
	seq.Feed("Bitte zweiten Faktor eingeben (OTP) / Please enter second factor (OTP).")
	if _, ok := seq.Feed("Generating OATH TOTP token code"); ok {
		t.Fatal()
	}
	v, _ := seq.Feed("Login failed.")
	if v.State != "auth_failed" || !strings.Contains(v.Message, "one-time code") || action(v) != "totp" {
		t.Fatal(v)
	}
}

// Real openconnect output with a wrong password (fake gateway, pass 22). A gateway with the
// code field in the password's form (ugr, ethz) gets the code before that form is sent, a
// Heidelberg-style one only in the OTP form after it. With --authgroup the form comes twice.
func TestLoginFailedInTheFormWithTheCodeNamesBoth(t *testing.T) {
	const oneForm = "POST https://vpn.example.edu/\nConnected to 10.0.0.1:443\n" +
		"XML POST enabled\nPlease enter your username and password.\n" +
		"Generating OATH TOTP token code\nPOST https://vpn.example.edu/\nLogin failed."
	const twoSteps = "POST https://vpn.example.edu/\nConnected to 10.0.0.1:443\n" +
		"XML POST enabled\nPlease enter your username and password.\nPOST https://vpn.example.edu/\n" +
		"Please enter second factor (OTP).\nGenerating OATH TOTP token code\n" +
		"POST https://vpn.example.edu/\nLogin failed."
	const group = "XML POST enabled\nPlease enter your username and password.\n" +
		"POST https://vpn.example.edu/\n"
	for _, c := range []struct{ name, out, want string }{
		{"one form", oneForm, AppendRejected},
		{"two steps", twoSteps, TOTPRejected},
		{"one form with a group", strings.Replace(oneForm, "XML", group+"XML", 1), AppendRejected},
		{"two steps with a group", strings.Replace(twoSteps, "XML", group+"XML", 1), TOTPRejected},
	} {
		seq := NewClassifier("totp_field")
		for _, line := range strings.Split(c.out, "\n") {
			seq.Feed(line)
		}
		if v, _ := seq.Verdict(); v != (Verdict{"auth_failed", c.want}) {
			t.Error(c.name, v)
		}
	}
}

func TestLoginFailedIsWordedByTheSecondFactor(t *testing.T) {
	for _, c := range []struct{ mfa, want string }{
		{"none", PasswordRejected}, {"totp_field", PasswordRejected},
		{"totp_append", AppendRejected}, {"duo_push", DuoRejected},
	} {
		v, ok := NewClassifier(c.mfa).Feed("Login failed.")
		if !ok || v != (Verdict{"auth_failed", c.want}) {
			t.Fatal(c.mfa, v)
		}
	}
	if !strings.Contains(AppendRejected, "clock") || !strings.Contains(DuoRejected, "Duo") {
		t.Fatal()
	}
	for _, m := range []string{AppendRejected, DuoRejected} {
		if messages.ByText(m).Action != "password" {
			t.Fatal(m)
		}
	}
}

func TestUnsupportedLoginMethodsSaySoWithoutNamingAUniversity(t *testing.T) {
	for _, line := range []string{"SAML authentication required", "Opening external browser for authentication"} {
		v := mustClassify(t, line)
		if v.State != "auth_failed" || v.Message != messages.SAMLRequired.Text {
			t.Fatal(v)
		}
	}
	if !strings.Contains(mustClassify(t, "Error: Server asked us to run CSD hostscan.").Message, "cannot do that yet") {
		t.Fatal()
	}
	for _, m := range Markers {
		if strings.Contains(m.Message, "Heidelberg") || strings.Contains(m.Message, "needs an update") {
			t.Fatal(m)
		}
	}
}

// Real openconnect 9.01 at a gateway whose default group signs in on a web page (pass 24): the
// form's message, then openconnect's own lines. Not a wrong password.
func TestSingleSignOnFormIsAWebSignIn(t *testing.T) {
	for _, lines := range [][]string{
		{"XML POST enabled", "Please complete the authentication process in the AnyConnect Login window.",
			"No SSO handler", "Failed to complete authentication"},
		{"XML POST enabled", "Please complete the authentication process in the browser window.",
			"Failed to spawn external browser for https://vpn.example.edu/+CSCOE+/saml/sp/login?ctx=1"},
		{"XML POST enabled", "Please complete the authentication process in the browser window.",
			"Failed to listen on local port 29786: Address family not supported by protocol",
			"Failed to complete authentication"},
		{"XML POST enabled", "No SSO handler", "Failed to complete authentication"}, // a form without a message
	} {
		c := NewClassifier("duo_push")
		for _, line := range lines {
			c.Feed(line)
		}
		if v, _ := c.Verdict(); v != (Verdict{StateAuthFailed, SAMLRequired}) {
			t.Fatal(lines, v)
		}
	}
}

func TestTunnelClassifierFollowsTheProfile(t *testing.T) {
	tn := New(Config{LoginName: "u", MFA: "duo_push"}, "openconnect", "/w", nil, "", t.TempDir())
	if tn.ClassifierMFA() != "duo_push" {
		t.Fatal(tn.ClassifierMFA())
	}
}

func TestClassifierKeepsFirstVerdict(t *testing.T) {
	seq := NewClassifier("")
	seq.Feed("Generating OATH TOTP token code")
	first, _ := seq.Feed("Login failed.")
	if v, _ := seq.Feed("User input required in non-interactive mode"); v != first {
		t.Fatal(v)
	}
	if v, _ := seq.Verdict(); v != first {
		t.Fatal(v)
	}
}

func TestGatewayTroubleIsNotASignInFailure(t *testing.T) {
	// Real openconnect 9.12: the gateway refused the connection, answered the login with 503,
	// or refused the tunnel after the login. "Failed to complete authentication" or exit code
	// 1 follow, but the password was never rejected: try again instead of asking for it.
	for _, lines := range [][]string{
		{"POST https://vpn.example.edu/", "Failed to connect to vpn.example.edu:443: Connection refused",
			"Failed to connect to host vpn.example.edu", "Failed to open HTTPS connection to vpn.example.edu",
			"Failed to complete authentication"},
		{"Connected to HTTPS on vpn.example.edu with ciphersuite (TLS1.3)", "Got HTTP response: HTTP/1.1 503 Service Unavailable",
			"Unexpected 503 result from server", "Failed to complete authentication"},
		{"Got inappropriate HTTP CONNECT response: HTTP/1.1 503 Service Unavailable", "Creating SSL connection failed"},
	} {
		c := NewClassifier("totp_field")
		for _, line := range lines {
			c.Feed(line)
		}
		if v, _ := c.Verdict(); v != (Verdict{StateError, messages.ConnectFailed.Text}) {
			t.Fatal(lines, v)
		}
	}
}

func TestOnlyTheServersOwnCertificateIsUntrusted(t *testing.T) {
	// Gateways that also take device certificates ask for one before the login form.
	seq := NewClassifier("")
	if _, ok := seq.Feed("Server requested SSL client certificate; none was configured"); ok {
		t.Fatal("a normal step taken for a certificate problem")
	}
	if v, _ := seq.Feed("Login failed."); v.State != "auth_failed" || action(v) != "password" {
		t.Fatal(v)
	}
	for _, line := range []string{"Server certificate verify failed: signer not found",
		`Certificate from VPN server "vpn.example.edu" failed verification.`} {
		if v := mustClassify(t, line); v != (Verdict{StateError, messages.ServerUntrusted.Text}) {
			t.Fatal(line, v)
		}
	}
}
