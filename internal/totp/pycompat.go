package totp

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Helpers that reproduce the Python standard library behaviour totp.py relies on, so that
// both cores accept and reject exactly the same input.

// pyIsSpace matches str.isspace() and the \s class of Python's re module.
func pyIsSpace(r rune) bool {
	return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
}

// fullUpper (ß and the Latin ligatures) holds the unconditional SpecialCasing uppercase mappings that produce ASCII
// letters; every other special mapping yields non-ASCII text either way.
var fullUpper = map[rune]string{
	'\u00df': "SS", '\ufb00': "FF", '\ufb01': "FI", '\ufb02': "FL", '\ufb03': "FFI", '\ufb04': "FFL", '\ufb05': "ST", '\ufb06': "ST",
}

// pyUpper approximates str.upper(): simple mappings plus the special ones above.
func pyUpper(s string) string {
	var b strings.Builder
	for _, r := range s {
		if u, ok := fullUpper[r]; ok {
			b.WriteString(u)
		} else {
			b.WriteRune(unicode.ToUpper(r))
		}
	}
	return b.String()
}

// splitURL returns netloc and query the way urllib.parse.urlsplit does for a URL whose
// scheme is known to be "otpauth".
func splitURL(text string) (netloc, query string, err error) {
	rest := text[strings.IndexByte(text, ':')+1:]
	rest = strings.NewReplacer("\t", "", "\r", "", "\n", "").Replace(rest)
	if strings.HasPrefix(rest, "//") {
		rest = rest[2:]
		end := len(rest)
		if i := strings.IndexAny(rest, "/?#"); i >= 0 {
			end = i
		}
		netloc, rest = rest[:end], rest[end:]
		if strings.Contains(netloc, "[") != strings.Contains(netloc, "]") {
			return "", "", errors.New("Invalid IPv6 URL")
		}
	}
	if i := strings.IndexByte(rest, '#'); i >= 0 {
		rest = rest[:i]
	}
	if i := strings.IndexByte(rest, '?'); i >= 0 {
		query = rest[i+1:]
	}
	return netloc, query, nil
}

// parseQuery is parse_qs(query, keep_blank_values=True) reduced to the last value per key.
func parseQuery(query string) map[string]string {
	out := map[string]string{}
	if query == "" {
		return out
	}
	for _, pair := range strings.Split(query, "&") {
		if pair == "" {
			continue
		}
		name, value, _ := strings.Cut(pair, "=")
		name = unquote(strings.ReplaceAll(name, "+", " "))
		out[name] = unquote(strings.ReplaceAll(value, "+", " "))
	}
	return out
}

func unhex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// unquote is urllib.parse.unquote: %XX escapes become bytes, invalid escapes stay as they
// are, and the bytes are decoded as UTF-8 with errors="replace".
func unquote(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var out strings.Builder
	var buf []byte
	flush := func() {
		out.WriteString(decodeReplace(buf))
		buf = buf[:0]
	}
	for i := 0; i < len(s); {
		c := s[i]
		if c >= 0x80 {
			// Non-ASCII text is kept as it is, only ASCII runs are unquoted.
			flush()
			_, size := utf8.DecodeRuneInString(s[i:])
			out.WriteString(s[i : i+size])
			i += size
			continue
		}
		if c == '%' && i+2 < len(s) {
			hi, ok1 := unhex(s[i+1])
			lo, ok2 := unhex(s[i+2])
			if ok1 && ok2 {
				buf = append(buf, hi<<4|lo)
				i += 3
				continue
			}
		}
		buf = append(buf, c)
		i++
	}
	flush()
	return out.String()
}

// decodeReplace decodes UTF-8 like Python with errors="replace": one U+FFFD per maximal
// invalid subpart.
func decodeReplace(b []byte) string {
	var out strings.Builder
	for i := 0; i < len(b); {
		r, size := utf8.DecodeRune(b[i:])
		if r != utf8.RuneError || size > 1 {
			out.WriteRune(r)
			i += size
			continue
		}
		out.WriteRune(utf8.RuneError)
		i += invalidSubpart(b[i:])
	}
	return out.String()
}

// invalidSubpart is the length of the maximal subpart of an ill-formed sequence at b[0].
func invalidSubpart(b []byte) int {
	c := b[0]
	var n int
	lo, hi := byte(0x80), byte(0xBF)
	switch {
	case c >= 0xC2 && c <= 0xDF:
		n = 2
	case c == 0xE0:
		n, lo = 3, 0xA0
	case c >= 0xE1 && c <= 0xEC, c == 0xEE, c == 0xEF:
		n = 3
	case c == 0xED:
		n, hi = 3, 0x9F
	case c == 0xF0:
		n, lo = 4, 0x90
	case c >= 0xF1 && c <= 0xF3:
		n = 4
	case c == 0xF4:
		n, hi = 4, 0x8F
	default:
		return 1
	}
	i := 1
	for ; i < n && i < len(b); i++ {
		if i == 1 {
			if b[i] < lo || b[i] > hi {
				break
			}
		} else if b[i] < 0x80 || b[i] > 0xBF {
			break
		}
	}
	return i
}

const b32alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"

// b32decode is base64.b32decode without casefold, including its padding rules.
func b32decode(s string) ([]byte, error) {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return nil, errors.New("string argument should contain only ASCII characters")
		}
	}
	if len(s)%8 != 0 {
		return nil, errors.New("Incorrect padding")
	}
	l := len(s)
	s = strings.TrimRight(s, "=")
	padchars := l - len(s)
	var decoded []byte
	var acc uint64
	for i := 0; i < len(s); i += 8 {
		end := min(i+8, len(s))
		acc = 0
		for j := i; j < end; j++ {
			v := strings.IndexByte(b32alphabet, s[j])
			if v < 0 {
				return nil, errors.New("Non-base32 digit found")
			}
			acc = acc<<5 + uint64(v)
		}
		var five [8]byte
		putUint40(five[:], acc)
		decoded = append(decoded, five[:5]...)
	}
	switch padchars {
	case 0, 1, 3, 4, 6:
	default:
		return nil, errors.New("Incorrect padding")
	}
	if padchars > 0 && len(decoded) > 0 {
		acc <<= 5 * uint(padchars)
		var last [8]byte
		putUint40(last[:], acc)
		leftover := (43 - 5*padchars) / 8
		decoded = append(decoded[:len(decoded)-5], last[:leftover]...)
	}
	return decoded, nil
}

// putUint40 writes the low 40 bits of v big-endian into b[:5].
func putUint40(b []byte, v uint64) {
	for i := 4; i >= 0; i-- {
		b[i] = byte(v)
		v >>= 8
	}
}
