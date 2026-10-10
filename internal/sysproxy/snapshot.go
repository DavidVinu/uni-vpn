package sysproxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf16"
)

// Snapshot is a proxy setting as stored in proxy-backup.json. The file is written exactly like
// Python's json.dumps of the dict uni_vpn/sysproxy.py builds, and read as leniently:
//
//	Linux:   {"mode": "none", "url": "", "kde": {"type": "0", "url": ""}}  (either part optional)
//	macOS:   {"services": {"Wi-Fi": {"url": "", "enabled": false}}}
//	Windows: {"windows": {"url": ""}}
type Snapshot struct {
	GNOME   *GNOME   // keys "mode" and "url"
	KDE     *KDE     // key "kde"
	Mac     *Mac     // key "services"
	Windows *Windows // key "windows"
}

type GNOME struct{ Mode, URL string }
type KDE struct{ Type, URL string }
type Windows struct{ URL string }

// Mac keeps the services in the order networksetup lists them.
type Mac struct{ Services []Service }

type Service struct {
	Name, URL string
	Enabled   bool
}

// MarshalJSON writes Python's json.dumps format: ", " and ": " separators, ASCII only.
func (s Snapshot) MarshalJSON() ([]byte, error) {
	var b strings.Builder
	var fields []string
	if s.GNOME != nil {
		fields = append(fields, pyString("mode")+": "+pyString(s.GNOME.Mode), pyString("url")+": "+pyString(s.GNOME.URL))
	}
	if s.KDE != nil {
		fields = append(fields, pyString("kde")+": {"+pyString("type")+": "+pyString(s.KDE.Type)+", "+
			pyString("url")+": "+pyString(s.KDE.URL)+"}")
	}
	if s.Mac != nil {
		var services []string
		for _, e := range s.Mac.Services {
			services = append(services, pyString(e.Name)+": {"+pyString("url")+": "+pyString(e.URL)+", "+
				pyString("enabled")+": "+fmt.Sprint(e.Enabled)+"}")
		}
		fields = append(fields, pyString("services")+": {"+strings.Join(services, ", ")+"}")
	}
	if s.Windows != nil {
		fields = append(fields, pyString("windows")+": {"+pyString("url")+": "+pyString(s.Windows.URL)+"}")
	}
	b.WriteString("{" + strings.Join(fields, ", ") + "}")
	return []byte(b.String()), nil
}

// pyString encodes like json.dumps with ensure_ascii: everything outside space..~ is escaped.
func pyString(s string) string {
	var b strings.Builder
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
			case r >= ' ' && r <= '~':
				b.WriteRune(r)
			case r > 0xFFFF:
				r1, r2 := utf16.EncodeRune(r)
				fmt.Fprintf(&b, `\u%04x\u%04x`, r1, r2)
			default:
				fmt.Fprintf(&b, `\u%04x`, r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

var errNotObject = errors.New("proxy backup: not a JSON object")

// UnmarshalJSON reads what either core wrote, with Python's leniency for missing or empty values.
func (s *Snapshot) UnmarshalJSON(data []byte) error {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return err
	}
	if top == nil {
		return errNotObject
	}
	*s = Snapshot{}
	if raw, ok := top["mode"]; ok {
		s.GNOME = &GNOME{Mode: rawString(raw), URL: rawString(top["url"])}
	}
	if obj := rawObject(top["kde"]); obj != nil {
		s.KDE = &KDE{Type: rawScalar(obj["type"]), URL: rawString(obj["url"])}
	}
	if raw, ok := top["services"]; ok {
		services, err := orderedObject(raw)
		if err != nil {
			return err
		}
		s.Mac = &Mac{Services: []Service{}}
		for _, kv := range services {
			e := rawObject(kv.value)
			s.Mac.Services = append(s.Mac.Services, Service{Name: kv.key, URL: rawString(e["url"]),
				Enabled: truthy(e["enabled"])})
		}
	}
	if obj := rawObject(top["windows"]); obj != nil {
		s.Windows = &Windows{URL: rawString(obj["url"])}
	}
	return nil
}

// rawString is the value when it is a string, else "" (Python's `value or ""`).
func rawString(raw json.RawMessage) string {
	var v string
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	return v
}

// rawScalar also takes a number as its text, like str() in Python; falsy values give "".
func rawScalar(raw json.RawMessage) string {
	if v := rawString(raw); v != "" {
		return v
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil && truthy(raw) {
		return n.String()
	}
	return ""
}

func rawObject(raw json.RawMessage) map[string]json.RawMessage {
	var obj map[string]json.RawMessage
	if raw == nil || json.Unmarshal(raw, &obj) != nil {
		return nil
	}
	return obj
}

// truthy follows Python's truth value of a decoded JSON value.
func truthy(raw json.RawMessage) bool {
	var v any
	if raw == nil || json.Unmarshal(raw, &v) != nil {
		return false
	}
	switch v := v.(type) {
	case nil:
		return false
	case bool:
		return v
	case float64:
		return v != 0
	case string:
		return v != ""
	case []any:
		return len(v) > 0
	case map[string]any:
		return len(v) > 0
	}
	return true
}

type keyValue struct {
	key   string
	value json.RawMessage
}

// orderedObject decodes an object keeping the key order, which Python iterates in.
// null counts as empty (Python's `or {}`).
func orderedObject(raw json.RawMessage) ([]keyValue, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if tok != json.Delim('{') {
		return nil, errNotObject
	}
	var out []keyValue
	seen := map[string]int{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, _ := tok.(string)
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, err
		}
		// A repeated key keeps its first position and the last value, as in a Python dict.
		if i, ok := seen[key]; ok {
			out[i].value = value
			continue
		}
		seen[key] = len(out)
		out = append(out, keyValue{key, value})
	}
	return out, nil
}
