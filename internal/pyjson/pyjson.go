// Package pyjson reads and writes JSON the way Python's json module does with its defaults,
// so the Go core answers byte for byte like the Python one: json.dumps (", " and ": "
// separators, ASCII only, Python's float repr, keys in insertion order) and json.loads
// (object keys in order, the C scanner's error messages with line, column and char).
package pyjson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf16"
)

// Member is one key and value of an Object.
type Member struct {
	Key   string
	Value any
}

// Object is a JSON object with its keys in order, like a Python dict.
type Object []Member

// Get returns the value of key and whether it is present.
func (o Object) Get(key string) (any, bool) {
	for _, m := range o {
		if m.Key == key {
			return m.Value, true
		}
	}
	return nil, false
}

// set replaces the value of an existing key in place, like assigning to a dict.
func (o Object) set(key string, value any) Object {
	for i := range o {
		if o[i].Key == key {
			o[i].Value = value
			return o
		}
	}
	return append(o, Member{key, value})
}

// Number is a JSON number as written. Python reads it as int or float.
type Number string

// IsInt reports whether Python reads it as an int.
func (n Number) IsInt() bool { return !strings.ContainsAny(string(n), ".eE") }

// Float is the number as float (Python's float() of it).
func (n Number) Float() float64 {
	f, _ := strconv.ParseFloat(string(n), 64)
	return f
}

// Constant numbers Python's json accepts beyond the standard.
const (
	NaN         Number = "NaN"
	Infinity    Number = "Infinity"
	NegInfinity Number = "-Infinity"
)

// Truthy is Python's bool() of a decoded value.
func Truthy(v any) bool {
	switch v := v.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	case Number:
		if v == NaN {
			return true
		}
		return v.Float() != 0
	case []any:
		return len(v) > 0
	case Object:
		return len(v) > 0
	}
	return true
}

// --- Encoding ---------------------------------------------------------------

// Marshal is json.dumps(v). It takes nil, bool, ints, float64, string, []string, []any,
// Object, Number and RawPy; any other value goes through encoding/json first, then is
// written in Python's format.
func Marshal(v any) []byte {
	var b bytes.Buffer
	encode(&b, v)
	return b.Bytes()
}

func encode(b *bytes.Buffer, v any) {
	switch v := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if v {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case int:
		b.WriteString(strconv.Itoa(v))
	case int64:
		b.WriteString(strconv.FormatInt(v, 10))
	case float64:
		b.WriteString(FloatRepr(v))
	case Number:
		if v.IsInt() || v == NaN || v == Infinity || v == NegInfinity {
			b.WriteString(string(v))
		} else {
			b.WriteString(FloatRepr(v.Float()))
		}
	case string:
		encodeString(b, v)
	case []string:
		b.WriteByte('[')
		for i, s := range v {
			if i > 0 {
				b.WriteString(", ")
			}
			encodeString(b, s)
		}
		b.WriteByte(']')
	case []any:
		b.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				b.WriteString(", ")
			}
			encode(b, item)
		}
		b.WriteByte(']')
	case Object:
		b.WriteByte('{')
		for i, m := range v {
			if i > 0 {
				b.WriteString(", ")
			}
			encodeString(b, m.Key)
			b.WriteString(": ")
			encode(b, m.Value)
		}
		b.WriteByte('}')
	default:
		data, err := json.Marshal(v)
		if err != nil {
			panic(fmt.Sprintf("pyjson: %v", err))
		}
		decoded, err := Unmarshal(data)
		if err != nil {
			panic(fmt.Sprintf("pyjson: %v", err))
		}
		encode(b, decoded)
	}
}

// encodeString is json.dumps of a str with ensure_ascii: everything outside space..~ is escaped.
func encodeString(b *bytes.Buffer, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			switch {
			case r >= 0x20 && r < 0x7f:
				b.WriteRune(r)
			case r >= 0x10000:
				r1, r2 := utf16.EncodeRune(r)
				fmt.Fprintf(b, `\u%04x\u%04x`, r1, r2)
			default:
				fmt.Fprintf(b, `\u%04x`, r)
			}
		}
	}
	b.WriteByte('"')
}

// FloatRepr is Python's repr(float), as json.dumps writes floats.
func FloatRepr(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	if f == 0 {
		if math.Signbit(f) {
			return "-0.0"
		}
		return "0.0"
	}
	e := strconv.FormatFloat(f, 'e', -1, 64) // shortest round trip, d.ddde±XX
	mant, expText, _ := strings.Cut(e, "e")
	exp, _ := strconv.Atoi(expText)
	if exp < -4 || exp >= 16 {
		sign := "+"
		if exp < 0 {
			sign, exp = "-", -exp
		}
		return fmt.Sprintf("%se%s%02d", mant, sign, exp)
	}
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

// --- Decoding ---------------------------------------------------------------

// SyntaxError is json.JSONDecodeError; Error() is its str().
type SyntaxError struct {
	Msg    string
	Pos    int // in characters, like Python
	Line   int
	Column int
}

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("%s: line %d column %d (char %d)", e.Msg, e.Line, e.Column, e.Pos)
}

type decoder struct {
	s []rune
}

func (d *decoder) fail(msg string, pos int) error {
	line := 1
	last := -1
	for i := 0; i < pos && i < len(d.s); i++ {
		if d.s[i] == '\n' {
			line++
			last = i
		}
	}
	return &SyntaxError{Msg: msg, Pos: pos, Line: line, Column: pos - last}
}

// stopIteration is the scanner finding no value at pos ("Expecting value").
type stopIteration struct{ pos int }

func (stopIteration) Error() string { return "stop" }

// Unmarshal is json.loads of UTF-8 text. Objects come back as Object, arrays as []any,
// numbers as Number. A lone surrogate escape becomes U+FFFD (Go strings cannot hold one).
func Unmarshal(data []byte) (any, error) {
	return UnmarshalString(string(data))
}

// UnmarshalString is json.loads(text).
func UnmarshalString(text string) (any, error) {
	d := &decoder{s: []rune(text)}
	if len(d.s) > 0 && d.s[0] == '\ufeff' {
		return nil, d.fail("Unexpected UTF-8 BOM (decode using utf-8-sig)", 0)
	}
	idx := d.ws(0)
	v, end, err := d.scan(idx)
	if err != nil {
		if si, ok := err.(stopIteration); ok {
			return nil, d.fail("Expecting value", si.pos)
		}
		return nil, err
	}
	end = d.ws(end)
	if end != len(d.s) {
		return nil, d.fail("Extra data", end)
	}
	return v, nil
}

func isWS(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' }

func (d *decoder) ws(i int) int {
	for i < len(d.s) && isWS(d.s[i]) {
		i++
	}
	return i
}

func (d *decoder) has(i int, word string) bool {
	w := []rune(word)
	if i+len(w) > len(d.s) {
		return false
	}
	for k, r := range w {
		if d.s[i+k] != r {
			return false
		}
	}
	return true
}

func (d *decoder) scan(idx int) (any, int, error) {
	if idx >= len(d.s) {
		return nil, 0, stopIteration{idx}
	}
	switch c := d.s[idx]; {
	case c == '"':
		return d.str(idx + 1)
	case c == '{':
		return d.object(idx + 1)
	case c == '[':
		return d.array(idx + 1)
	case c == 'n' && d.has(idx, "null"):
		return nil, idx + 4, nil
	case c == 't' && d.has(idx, "true"):
		return true, idx + 4, nil
	case c == 'f' && d.has(idx, "false"):
		return false, idx + 5, nil
	case c == 'N' && d.has(idx, "NaN"):
		return NaN, idx + 3, nil
	case c == 'I' && d.has(idx, "Infinity"):
		return Infinity, idx + 8, nil
	case c == '-' && d.has(idx, "-Infinity"):
		return NegInfinity, idx + 9, nil
	}
	return d.number(idx)
}

func isDigit(r rune) bool { return r >= '0' && r <= '9' }

func (d *decoder) number(start int) (any, int, error) {
	i, n := start, len(d.s)
	if d.s[i] == '-' {
		i++
		if i >= n {
			return nil, 0, stopIteration{start}
		}
	}
	switch {
	case d.s[i] >= '1' && d.s[i] <= '9':
		i++
		for i < n && isDigit(d.s[i]) {
			i++
		}
	case d.s[i] == '0':
		i++
	default:
		return nil, 0, stopIteration{start}
	}
	if i < n-1 && d.s[i] == '.' && isDigit(d.s[i+1]) {
		i += 2
		for i < n && isDigit(d.s[i]) {
			i++
		}
	}
	if i < n-1 && (d.s[i] == 'e' || d.s[i] == 'E') {
		e := i + 1
		if e < n-1 && (d.s[e] == '-' || d.s[e] == '+') {
			e++
		}
		if e < n && isDigit(d.s[e]) {
			for e < n && isDigit(d.s[e]) {
				e++
			}
			i = e
		}
	}
	return Number(string(d.s[start:i])), i, nil
}

func hexValue(r rune) (rune, bool) {
	switch {
	case r >= '0' && r <= '9':
		return r - '0', true
	case r >= 'a' && r <= 'f':
		return r - 'a' + 10, true
	case r >= 'A' && r <= 'F':
		return r - 'A' + 10, true
	}
	return 0, false
}

// str is scanstring: end is just after the opening quote.
func (d *decoder) str(end int) (any, int, error) {
	begin := end - 1
	n := len(d.s)
	var out []rune
	for {
		next := end
		var c rune
		for ; next < n; next++ {
			c = d.s[next]
			if c == '"' || c == '\\' {
				break
			}
			if c <= 0x1f {
				return nil, 0, d.fail("Invalid control character at", next)
			}
		}
		if next >= n || (c != '"' && c != '\\') {
			return nil, 0, d.fail("Unterminated string starting at", begin)
		}
		out = append(out, d.s[end:next]...)
		next++
		if c == '"' {
			return string(out), next, nil
		}
		if next == n {
			return nil, 0, d.fail("Unterminated string starting at", begin)
		}
		c = d.s[next]
		if c != 'u' {
			end = next + 1
			switch c {
			case '"', '\\', '/':
			case 'b':
				c = '\b'
			case 'f':
				c = '\f'
			case 'n':
				c = '\n'
			case 'r':
				c = '\r'
			case 't':
				c = '\t'
			default:
				return nil, 0, d.fail("Invalid \\escape", end-2)
			}
			out = append(out, c)
			continue
		}
		next++
		end = next + 4
		if end >= n {
			return nil, 0, d.fail("Invalid \\uXXXX escape", next-1)
		}
		c = 0
		for ; next < end; next++ {
			h, ok := hexValue(d.s[next])
			if !ok {
				return nil, 0, d.fail("Invalid \\uXXXX escape", end-5)
			}
			c = c<<4 | h
		}
		if c >= 0xd800 && c <= 0xdbff && end+6 < n && d.s[next] == '\\' && d.s[next+1] == 'u' {
			next += 2
			end += 6
			var c2 rune
			for ; next < end; next++ {
				h, ok := hexValue(d.s[next])
				if !ok {
					return nil, 0, d.fail("Invalid \\uXXXX escape", end-5)
				}
				c2 = c2<<4 | h
			}
			if c2 >= 0xdc00 && c2 <= 0xdfff {
				c = utf16.DecodeRune(c, c2)
			} else {
				end -= 6
			}
		}
		if c >= 0xd800 && c <= 0xdfff {
			c = '�'
		}
		out = append(out, c)
	}
}

func (d *decoder) object(idx int) (any, int, error) {
	n := len(d.s)
	obj := Object{}
	idx = d.ws(idx)
	if idx < n && d.s[idx] == '}' {
		return obj, idx + 1, nil
	}
	for {
		if idx >= n || d.s[idx] != '"' {
			return nil, 0, d.fail("Expecting property name enclosed in double quotes", idx)
		}
		key, next, err := d.str(idx + 1)
		if err != nil {
			return nil, 0, err
		}
		idx = d.ws(next)
		if idx >= n || d.s[idx] != ':' {
			return nil, 0, d.fail("Expecting ':' delimiter", idx)
		}
		idx = d.ws(idx + 1)
		val, next, err := d.scan(idx)
		if err != nil {
			return nil, 0, err
		}
		obj = obj.set(key.(string), val)
		idx = d.ws(next)
		if idx < n && d.s[idx] == '}' {
			return obj, idx + 1, nil
		}
		if idx >= n || d.s[idx] != ',' {
			return nil, 0, d.fail("Expecting ',' delimiter", idx)
		}
		idx = d.ws(idx + 1)
	}
}

func (d *decoder) array(idx int) (any, int, error) {
	n := len(d.s)
	arr := []any{}
	idx = d.ws(idx)
	if idx < n && d.s[idx] == ']' {
		return arr, idx + 1, nil
	}
	for {
		val, next, err := d.scan(idx)
		if err != nil {
			return nil, 0, err
		}
		arr = append(arr, val)
		idx = d.ws(next)
		if idx < n && d.s[idx] == ']' {
			return arr, idx + 1, nil
		}
		if idx >= n || d.s[idx] != ',' {
			return nil, 0, d.fail("Expecting ',' delimiter", idx)
		}
		idx = d.ws(idx + 1)
	}
}

// O builds an Object from key, value pairs: O("ok", true, "state", "idle").
func O(kv ...any) Object {
	o := make(Object, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		o = append(o, Member{Key: kv[i].(string), Value: kv[i+1]})
	}
	return o
}
