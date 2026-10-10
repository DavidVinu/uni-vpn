package totp

import (
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DavidVinu/uni-vpn/internal/i18n"
	"github.com/DavidVinu/uni-vpn/internal/pyjson"
)

// RFC 6238, Appendix B: secret "12345678901234567890" (SHA1), or 32 bytes for SHA256.
const (
	rfcSHA1   = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	rfcSHA256 = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQGEZA"
)

func mustNormalize(t *testing.T, text, want string) {
	t.Helper()
	got, err := Normalize(text)
	if err != nil || got != want {
		t.Fatalf("Normalize(%q) = %q, %v; want %q", text, got, err, want)
	}
}

func mustFail(t *testing.T, text string) error {
	t.Helper()
	got, err := Normalize(text)
	if err == nil {
		t.Fatalf("Normalize(%q) = %q, want error", text, got)
	}
	return err
}

func TestPlainBase32BecomesOpenconnectToken(t *testing.T) {
	mustNormalize(t, rfcSHA1, "base32:"+rfcSHA1)
}

func TestLowercaseSpacesDashesAndPaddingAreCleaned(t *testing.T) {
	mustNormalize(t, " gezd gnbv-gy3t qojq\tGEZD GNBV GY3T QOJQ== ", "base32:"+rfcSHA1)
}

func TestOtpauthURISecretIsExtracted(t *testing.T) {
	uri := "otpauth://totp/Uni%20Heidelberg:ab123?secret=" + rfcSHA1 + "&issuer=Uni%20Heidelberg&digits=6&period=30"
	mustNormalize(t, uri, "base32:"+rfcSHA1)
}

func TestOtpauthURIWithSHA256KeepsAlgorithm(t *testing.T) {
	mustNormalize(t, "otpauth://totp/x?secret="+rfcSHA256+"&algorithm=SHA256", "sha256:base32:"+rfcSHA256)
}

func TestRejectsHOTPURI(t *testing.T) {
	err := mustFail(t, "otpauth://hotp/x?secret="+rfcSHA1+"&counter=0")
	if !strings.Contains(err.Error(), "time-based") {
		t.Fatal(err)
	}
}

func TestRejectsUnusualDigitsOrPeriod(t *testing.T) {
	for _, q := range []string{"digits=8", "period=60"} {
		mustFail(t, "otpauth://totp/x?secret="+rfcSHA1+"&"+q)
	}
}

func TestRejectsUnknownAlgorithm(t *testing.T) {
	err := mustFail(t, "otpauth://totp/x?secret="+rfcSHA1+"&algorithm=MD5")
	if keyOf(err) != "totp.algorithm" || i18n.Of(err).(pyjson.Object)[1].Value.(pyjson.Object)[0].Value != "MD5" {
		t.Fatal(err)
	}
}

func TestRejectsEmptyURISecret(t *testing.T) {
	mustFail(t, "otpauth://totp/x?issuer=y")
}

func TestRejectsGarbage(t *testing.T) {
	for _, text := range []string{"", "   ", "0189", "GEZD GNBV 1!", "abc"} {
		mustFail(t, text)
	}
}

func TestRejectsNewline(t *testing.T) {
	mustFail(t, rfcSHA1+"\nbase32:AAAA")
}

// Edge cases checked against the Python implementation.
func TestPythonCompatibility(t *testing.T) {
	ok := map[string]string{
		// Python's str.upper() maps ß to SS.
		"gezdgnbvgy3tqojqgezdgnbvgy3tqo\u00df": "base32:GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOSS",
		// parse_qs plus the second unquote: %253D decodes twice.
		"otpauth://totp/x?secret=GEZDGNBVGY3TQOJQ%253D": "base32:GEZDGNBVGY3TQOJQ",
		// \x1c is whitespace for Python.
		"\x1c" + rfcSHA1: "base32:" + rfcSHA1,
		"OTPAUTH://TOTP/x?secret=" + rfcSHA1 + "#frag":             "base32:" + rfcSHA1,
		"otpauth://to\ttp/x?secret=" + rfcSHA1:                     "base32:" + rfcSHA1,
		"otpauth://totp/x?secret=a&secret=" + rfcSHA1:              "base32:" + rfcSHA1,
		"otpauth://totp/x?secret=" + rfcSHA1 + "&algorithm=sha512": "sha512:base32:" + rfcSHA1,
	}
	for in, want := range ok {
		mustNormalize(t, in, want)
	}
	msgs := map[string]string{
		"AAAAAAAAAAAAAAAAA":                                 "totp.base32", // 17 chars: bad padding
		"AAAAAAAAAAAAAAA":                                   "totp.incomplete",
		"otpauth:totp/x?secret=" + rfcSHA1:                  "totp.only_totp",
		"otpauth://[totp/x":                                 "Invalid IPv6 URL",
		"otpauth://totp/x?secret=" + rfcSHA1 + "&digits=06": "totp.digits",
		"otpauth://totp/x?algorithm=%E2%82":                 "totp.algorithm",
		"otpauth://totp/x":                                  "totp.empty",
		"a\rb":                                              "totp.line_break",
	}
	for in, want := range msgs {
		if err := mustFail(t, in); keyOf(err) != want {
			t.Errorf("Normalize(%q): %q, want %q", in, err, want)
		}
	}
}

func TestRFC6238SHA1Vectors(t *testing.T) {
	token := "base32:" + rfcSHA1
	for now, want := range map[int64]string{59: "287082", 1111111109: "081804", 1234567890: "005924"} {
		got, err := CodeAt(token, time.Unix(now, 0))
		if err != nil || got != want {
			t.Errorf("CodeAt(%d) = %q, %v; want %q", now, got, err, want)
		}
	}
}

func TestRFC6238SHA256Vector(t *testing.T) {
	got, err := CodeAt("sha256:base32:"+rfcSHA256, time.Unix(59, 0))
	if err != nil || got != "119246" {
		t.Fatal(got, err)
	}
}

// Value computed with uni_vpn/totp.py.
func TestSHA512MatchesPython(t *testing.T) {
	secret := strings.Repeat("GEZDGNBVGY3TQOJQ", 8)
	got, err := CodeAt("sha512:base32:"+secret, time.Unix(59, 0))
	if err != nil || got != "697601" {
		t.Fatal(got, err)
	}
}

func TestCodeUsesCurrentTimeByDefault(t *testing.T) {
	got, err := Code("base32:" + rfcSHA1)
	if err != nil || !regexp.MustCompile(`^[0-9]{6}$`).MatchString(got) {
		t.Fatal(got, err)
	}
}

func TestCodeRejectsUnknownFormat(t *testing.T) {
	for token, want := range map[string]string{
		"hex:3132":              "totp.unknown",
		"sha1:sha256:base32:AA": "totp.unknown",
		"base32:gezdgnbv":       "totp.base32",
		"base32:A\nAAAAAAA":     "totp.base32",
	} {
		if _, err := Code(token); err == nil || keyOf(err) != want {
			t.Errorf("Code(%q): %v, want %q", token, err, want)
		}
	}
}

func TestB32DecodeMatchesPython(t *testing.T) {
	// Lengths whose padding Python accepts.
	for n := 1; n <= 8; n++ {
		s := strings.Repeat("A", n)
		_, err := b32decode(s + strings.Repeat("=", (8-n%8)%8))
		want := n == 1 || n == 3 || n == 6
		if (err != nil) != want {
			t.Errorf("length %d: %v", n, err)
		}
	}
	got, err := b32decode("GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ")
	if err != nil || string(got) != "12345678901234567890" {
		t.Fatal(got, err)
	}
	got, err = b32decode("MZXW6===")
	if err != nil || string(got) != "foo" {
		t.Fatal(got, err)
	}
}

// keyOf is the catalog key of an error, its text when it has none.
func keyOf(err error) string {
	var text i18n.Text
	if errors.As(err, &text) {
		return text.Key
	}
	return err.Error()
}
