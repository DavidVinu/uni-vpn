package universities

import (
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// Python's str semantics where they differ from Go's, so messages and normalization match the
// Python core.

// isSpace is str.isspace: Go's unicode.IsSpace plus the separators \x1c-\x1f.
func isSpace(r rune) bool {
	return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
}

// strip is str.strip() without arguments.
func strip(s string) string { return strings.TrimFunc(s, isSpace) }

// lower is str.lower(): full case mapping turns U+0130 into "i" plus a combining dot.
func lower(s string) string {
	return strings.ToLower(strings.ReplaceAll(s, "\u0130", "i\u0307"))
}

// Repr is Python's repr() of a decoded JSON or TOML value, for messages that quote one.
func Repr(v any) string {
	switch v := v.(type) {
	case nil:
		return "None"
	case string:
		return reprStr(v)
	case bool:
		if v {
			return "True"
		}
		return "False"
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return reprFloat(v)
	case json.Number:
		s := string(v)
		if !strings.ContainsAny(s, ".eE") {
			if s == "-0" {
				return "0"
			}
			return s
		}
		f, _ := strconv.ParseFloat(s, 64)
		return reprFloat(f)
	case []any:
		parts := make([]string, len(v))
		for i, item := range v {
			parts[i] = Repr(item)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = reprStr(k) + ": " + Repr(v[k])
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	return "?"
}

func reprStr(s string) string {
	quote := '\''
	if strings.ContainsRune(s, '\'') && !strings.ContainsRune(s, '"') {
		quote = '"'
	}
	var b strings.Builder
	b.WriteRune(quote)
	for _, r := range s {
		switch {
		case r == quote || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case unicode.IsPrint(r):
			b.WriteRune(r)
		case r < 0x100:
			b.WriteString(`\x` + hex(r, 2))
		case r < 0x10000:
			b.WriteString(`\u` + hex(r, 4))
		default:
			b.WriteString(`\U` + hex(r, 8))
		}
	}
	b.WriteRune(quote)
	return b.String()
}

func hex(r rune, width int) string {
	s := strconv.FormatInt(int64(r), 16)
	return strings.Repeat("0", width-len(s)) + s
}

func reprFloat(f float64) string {
	switch {
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	case math.IsNaN(f):
		return "nan"
	}
	// Python switches to exponent notation outside 1e-4 <= |f| < 1e16.
	e := strconv.FormatFloat(f, 'e', -1, 64)
	exp, _ := strconv.Atoi(e[strings.IndexByte(e, 'e')+1:])
	if f != 0 && (exp < -4 || exp >= 16) {
		return e
	}
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

// Fold is lower case without accents, so "zuerich", "zurich" and "Zürich" find the same entries.
func Fold(text string) string {
	foldOnce.Do(buildFold)
	var b strings.Builder
	for _, r := range text {
		if r < 0x80 {
			b.WriteRune(r)
		} else {
			b.WriteString(foldMap[r])
		}
	}
	plain := strings.ToLower(b.String())
	plain = strings.ReplaceAll(plain, "ue", "u")
	plain = strings.ReplaceAll(plain, "oe", "o")
	return strings.ReplaceAll(plain, "ae", "a")
}

func buildFold() {
	foldMap = make(map[rune]string, 2200)
	for _, run := range foldRuns {
		for i := 0; i < len(run.to); i++ {
			foldMap[run.start+rune(i)] = run.to[i : i+1]
		}
	}
	for r, s := range foldMulti {
		foldMap[r] = s
	}
}
