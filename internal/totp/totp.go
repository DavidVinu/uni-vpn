// Package totp validates and normalizes the TOTP secret and computes the check code
// (RFC 6238). It mirrors uni_vpn/totp.py, which knows no 8-digit tokens.
//
// openconnect generates the codes at login (--token-mode=totp). This package only brings the
// user's input into the format openconnect understands ("base32:...") and computes a check
// code the user can compare against their app. 8-digit tokens (Marburg) are marked with
// EightDigits: openconnect makes 6-digit codes only, so they are for totp_append, where the
// core computes the code itself.
package totp

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
	"fmt"
	"hash"
	"regexp"
	"strings"
	"time"

	"github.com/DavidVinu/uni-vpn/internal/i18n"
)

// Algorithms maps the otpauth algorithm names to the token prefixes openconnect knows.
var Algorithms = map[string]string{"SHA1": "sha1", "SHA256": "sha256", "SHA512": "sha512"}

const (
	Step     = 30 // seconds per code
	MinChars = 16 // 80 bits, no portal issues less
)

// EightDigits starts a token whose codes have 8 digits ("digits8:sha512:base32:...").
const EightDigits = "digits8:"

// Digits is the length of a normalized token's codes: 8 or 6.
func Digits(token string) int {
	if strings.HasPrefix(token, EightDigits) {
		return 8
	}
	return 6
}

// Fits says whether a token's codes work with the sign-in mode mfa: openconnect makes the codes
// itself, 6-digit ones only, unless they go after the password (totp_append).
func Fits(mfa, token string) bool {
	return mfa == "totp_append" || Digits(token) == 6
}

var base32RE = regexp.MustCompile(`^[A-Z2-7]+$`)

// Normalize turns an otpauth URL or Base32 text into an openconnect token
// ("base32:..." or "sha256:base32:..."), with EightDigits in front for 8-digit codes.
func Normalize(text string) (string, error) {
	text = strings.TrimFunc(text, pyIsSpace)
	if strings.ContainsAny(text, "\n\r") {
		return "", i18n.T("totp.line_break")
	}
	algorithm := "SHA1"
	digits := "6"
	var secret string
	if strings.HasPrefix(pyLowerASCIIPrefix(text, 8), "otpauth:") {
		netloc, rawQuery, err := splitURL(text)
		if err != nil {
			return "", err
		}
		if strings.ToLower(netloc) != "totp" {
			return "", i18n.T("totp.only_totp")
		}
		query := parseQuery(rawQuery)
		secret = unquote(query["secret"])
		if v, ok := query["algorithm"]; ok {
			algorithm = pyUpper(v)
		}
		if _, ok := Algorithms[algorithm]; !ok {
			return "", i18n.T("totp.algorithm", "algorithm", algorithm)
		}
		digits = getDefault(query, "digits", "6")
		if digits != "6" && digits != "8" || getDefault(query, "period", "30") != "30" {
			return "", i18n.T("totp.digits")
		}
	} else {
		secret = text
	}
	var b strings.Builder
	for _, r := range secret {
		if r == '-' || pyIsSpace(r) {
			continue
		}
		b.WriteRune(r)
	}
	cleaned := strings.TrimRight(pyUpper(b.String()), "=")
	if cleaned == "" {
		return "", i18n.T("totp.empty")
	}
	if !base32RE.MatchString(cleaned) {
		return "", i18n.T("totp.not_secret")
	}
	if len(cleaned) < MinChars {
		return "", i18n.T("totp.incomplete")
	}
	if _, err := decode(cleaned); err != nil {
		return "", err
	}
	token := "base32:" + cleaned
	if algorithm != "SHA1" {
		token = strings.ToLower(algorithm) + ":" + token
	}
	if digits == "8" {
		token = EightDigits + token
	}
	return token, nil
}

func getDefault(m map[string]string, key, def string) string {
	if v, ok := m[key]; ok {
		return v
	}
	return def
}

// pyLowerASCIIPrefix lowercases the first n bytes for the prefix check (text.lower().startswith).
func pyLowerASCIIPrefix(s string, n int) string {
	if len(s) > n {
		s = s[:n]
	}
	return strings.ToLower(s)
}

func decode(cleaned string) ([]byte, error) {
	padded := cleaned + strings.Repeat("=", (8-len(cleaned)%8)%8)
	out, err := b32decode(padded)
	if err != nil {
		return nil, i18n.T("totp.base32")
	}
	return out, nil
}

// Code is the code for a normalized token at the current time.
func Code(token string) (string, error) {
	return CodeAt(token, time.Now())
}

// CodeAt is the code for a normalized token at now, as openconnect generates it (6 digits),
// or with 8 digits for an EightDigits token.
func CodeAt(token string, now time.Time) (string, error) {
	digits := Digits(token)
	token = strings.TrimPrefix(token, EightDigits)
	digest := sha1.New
	for _, d := range []struct {
		name string
		fn   func() hash.Hash
	}{{"sha512", sha512.New}, {"sha256", sha256.New}, {"sha1", sha1.New}} {
		if strings.HasPrefix(token, d.name+":") {
			digest = d.fn
			token = token[len(d.name)+1:]
			break
		}
	}
	if !strings.HasPrefix(token, "base32:") {
		return "", i18n.T("totp.unknown")
	}
	key, err := decode(token[len("base32:"):])
	if err != nil {
		return "", err
	}
	secs := now.Unix()
	counter := secs / Step
	if secs%Step != 0 && secs < 0 {
		counter-- // floor division like Python
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(counter))
	mac := hmac.New(digest, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0F
	number := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7FFFFFFF
	if digits == 8 {
		return fmt.Sprintf("%08d", number%100_000_000), nil
	}
	return fmt.Sprintf("%06d", number%1_000_000), nil
}
