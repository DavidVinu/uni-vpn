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
