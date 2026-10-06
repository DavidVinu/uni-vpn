package detect

// Local copies of the uni_vpn/universities.py pieces detect needs: FieldError, check_field for
// host, usergroup and authgroup, DEFAULT_USERAGENT. internal/universities ports the same; the
// integrator should switch to that package and delete this file.

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// DefaultUserAgent mirrors universities.DEFAULT_USERAGENT.
const DefaultUserAgent = "AnyConnect Linux_64 5.1.18.314"

// FieldError is a profile value that is not allowed; Field names it for the setup assistant.
type FieldError struct {
	Field   string
	Message string
}

func (e *FieldError) Error() string { return e.Message }

const label = `[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?`

var (
	// HOST_RE without its lookahead (RE2 has none); checkHost enforces the 253 limit on the name.
	hostRE      = regexp.MustCompile(`^` + label + `(?:\.` + label + `)+(?::[0-9]{1,5})?$`)
	usergroupRE = regexp.MustCompile(`^[A-Za-z0-9._~+-]{0,64}(?:/[A-Za-z0-9._~+-]{1,64}){0,3}$`)
	// Printable, no quotes or backslashes: these go into argv and config.toml as they are.
	textRE = regexp.MustCompile(`^[^\x00-\x1f\x7f"\\]{0,128}$`)
)

// pyIsSpace matches Python's str.isspace, which also counts the separators \x1c-\x1f.
func pyIsSpace(r rune) bool { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) }

func pyStrip(s string) string { return strings.TrimFunc(s, pyIsSpace) }

// pyLower matches str.lower for anything that can pass hostRE: Go maps U+0130 to "i" where
// Python gives "i̇", so keep it and let the regex refuse it.
func pyLower(s string) string {
	return strings.Map(func(r rune) rune {
		if r == 0x130 {
			return r
		}
		return unicode.ToLower(r)
	}, s)
}

func checkHost(value string) (string, error) {
	value = strings.TrimRight(pyLower(pyStrip(value)), ".")
	name, _, _ := strings.Cut(value, ":")
	if len(name) > 253 || !hostRE.MatchString(value) {
		return "", &FieldError{"host", "Enter the VPN address, for example vpn.example.edu"}
	}
	return value, nil
}

func checkUsergroup(value string) (string, error) {
	value = strings.Trim(pyStrip(value), "/")
	if !usergroupRE.MatchString(value) {
		return "", &FieldError{"usergroup", "The path after the address may only contain letters, digits and . _ ~ + -"}
	}
	return value, nil
}

func checkText(name, value string) (string, error) {
	// Python strings are always valid Unicode; RE2 would read bad bytes as U+FFFD and let them in.
	if !utf8.ValidString(value) || !textRE.MatchString(value) {
		return "", &FieldError{name, "'" + name + "' must not contain quotes, backslashes or line breaks"}
	}
	return value, nil
}
