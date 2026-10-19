//go:build !windows

package daemon

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/DavidVinu/uni-vpn/internal/httpapi"
	"github.com/DavidVinu/uni-vpn/internal/i18n"
	"github.com/DavidVinu/uni-vpn/internal/messages"
	unis "github.com/DavidVinu/uni-vpn/internal/universities"
)

// An 8-digit HMAC-SHA512 token, as Marburg's portal issues for apps.
var eightDigits = "digits8:sha512:base32:" + strings.Repeat("GEZDGNBVGY3TQOJQ", 8)

func TestEightDigitCodesGoAfterThePassword(t *testing.T) {
	h, _ := profileHarness(t)
	h.cfg.MFA = "totp_append"
	h.totp = []byte(eightDigits)
	d := h.start(nil)
	d.RequestConnect()
	waitState(t, d, Connected)
	lines := h.pwLines()
	if len(lines) != 1 || !regexp.MustCompile(`^pw-s3cret[0-9]{8}$`).MatchString(lines[0]) {
		t.Fatal(lines)
	}
}

// openconnect makes 6-digit codes only, and it would take the marked token for a raw key.
func TestEightDigitTokenIsNotGivenToOpenconnect(t *testing.T) {
	h, args := profileHarness(t)
	h.cfg.MFA = "totp_field"
	h.totp = []byte(eightDigits)
	d := h.start(nil)
	d.RequestConnect()
	waitState(t, d, AuthFailed)
	if _, m := d.snapshot(); m != messages.TOTPUnusable || h.pwLines() != nil {
		t.Fatal(m, h.pwLines())
	}
	if _, err := os.Stat(args); err == nil {
		t.Fatal("openconnect started")
	}
}

func TestEightDigitKeysAreRefusedWhereOpenconnectMakesTheCodes(t *testing.T) {
	h := newHarness(t)
	d := h.start(nil) // Heidelberg: totp_field
	digits := fmt.Sprint(i18n.T("totp.digits").JSON())
	err := d.CompleteSetup("cd456", "pw", eightDigits, "heidelberg", nil)
	var fe *unis.FieldError
	if !errors.As(err, &fe) || fe.Field != "totp" || fmt.Sprint(i18n.Of(err)) != digits {
		t.Fatalf("setup: %v", err)
	}
	err = d.SetTOTP(eightDigits)
	var ve *httpapi.ValueError
	if !errors.As(err, &ve) || fmt.Sprint(i18n.Of(err)) != digits {
		t.Fatalf("settings: %v", err)
	}
	if len(h.storedTOTP) != 0 {
		t.Fatal("stored:", h.storedTOTP)
	}
}

// The setup assistant's check refuses such a key before it shows a code, so the user never
// registers it in the portal; after the password, or without a mode, it gives the code.
func TestKeyCheckRefusesCodesTheSignInCannotUse(t *testing.T) {
	h := newHarness(t)
	h.start(nil)
	check := func(mfa string) reply {
		body := `{"secret": "otpauth://totp/x?secret=GEZDGNBVGY3TQOJQ&algorithm=SHA512&digits=8"`
		if mfa != "" {
			body += fmt.Sprintf(`, "mfa": %q`, mfa)
		}
		return h.post("/api/totp/check", body+"}")
	}
	r := check("totp_field")
	if r.status != 400 || r.json(t)["error_t"].(map[string]any)["key"] != "totp.digits" {
		t.Fatal(r.status, string(r.body))
	}
	for _, mfa := range []string{"totp_append", ""} {
		if r := check(mfa); r.status != 200 || len(r.json(t)["code"].(string)) != 8 {
			t.Fatal(mfa, r.status, string(r.body))
		}
	}
	r = h.post("/api/totp/check", `{"secret": "GEZDGNBVGY3TQOJQ", "mfa": "totp_field"}`)
	if r.status != 200 || len(r.json(t)["code"].(string)) != 6 {
		t.Fatal(r.status, string(r.body))
	}
}
