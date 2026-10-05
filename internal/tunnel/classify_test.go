package tunnel

import (
	"strings"
	"testing"
)

func mustClassify(t *testing.T, line string) Verdict {
	t.Helper()
	v, ok := ClassifyLine(line)
	if !ok {
		t.Fatalf("no verdict for %q", line)
	}
	return v
}

func TestMarkers(t *testing.T) {
	if v := mustClassify(t, "User input required in non-interactive mode"); v.State != "auth_failed" || !strings.Contains(v.Message, "uni-vpn log") {
		t.Fatal(v)
	}
	if !strings.Contains(mustClassify(t, "Failed to complete authentication").Message, "password") {
		t.Fatal()
	}
	if !strings.Contains(mustClassify(t, "Error: Server asked us to run CSD hostscan.").Message, "HostScan") {
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
	for _, s := range []string{"password", "uni-vpn password", "uni-vpn log"} {
		if !strings.Contains(in.Message, s) {
			t.Fatal(s)
		}
	}
}

func TestInvalidSoftTokenStringNamesTheCommand(t *testing.T) {
	// Real openconnect 9.12 with a broken secret file: "Invalid base32 token string",
	// then "Soft token string is invalid", exit code 1 before any network contact.
	v := mustClassify(t, "Soft token string is invalid")
	if v.State != "auth_failed" || !strings.Contains(v.Message, "uni-vpn totp") {
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
	for _, s := range []string{"One-time code", "uni-vpn totp", "clock"} {
		if !strings.Contains(v.Message, s) {
			t.Fatal(s)
		}
	}
}

func TestLoginFailedBeforeOTPMeansPassword(t *testing.T) {
	// Measured on 2026-09-08: the ASA rejects a wrong password before it asks for the OTP.
	seq := NewClassifier("")
	if _, ok := seq.Feed("Bitte geben Sie ihren Benutzernamen und ihr Passwort ein."); ok {
		t.Fatal()
	}
	v, _ := seq.Feed("Login failed.")
	if v.State != "auth_failed" || !strings.Contains(v.Message, "uni-vpn password") || strings.Contains(v.Message, "One-time code") {
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
	if v.State != "auth_failed" || !strings.Contains(v.Message, "One-time code") ||
		!strings.Contains(v.Message, "uni-vpn totp") || strings.Contains(v.Message, "uni-vpn password") {
		t.Fatal(v)
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
		if !strings.Contains(m, "uni-vpn password") {
			t.Fatal(m)
		}
		if strings.Contains(strings.ToLower(m), "totp") {
			t.Fatal("the page would offer the TOTP fix")
		}
	}
}

func TestUnsupportedLoginMethodsSaySoWithoutNamingAUniversity(t *testing.T) {
	for _, line := range []string{"SAML authentication required", "Opening external browser for authentication"} {
		v := mustClassify(t, line)
		if v.State != "auth_failed" || !strings.Contains(v.Message, "browser") || !strings.Contains(v.Message, "not support") {
			t.Fatal(v)
		}
	}
	if !strings.Contains(mustClassify(t, "Error: Server asked us to run CSD hostscan.").Message, "not support") {
		t.Fatal()
	}
	for _, m := range Markers {
		if strings.Contains(m.Message, "Heidelberg") || strings.Contains(m.Message, "needs an update") {
			t.Fatal(m)
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
