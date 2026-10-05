// Package config reads and writes config.toml. It mirrors uni_vpn/config.py: same keys,
// defaults, checks and messages, so both cores share one file.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/BurntSushi/toml"

	"github.com/DavidVinu/uni-vpn/internal/platform"
	unis "github.com/DavidVinu/uni-vpn/internal/universities"
)

const (
	DefaultHost      = "vpn-ac.uni-heidelberg.de"
	DefaultUseragent = unis.DefaultUseragent
)

// ConfigError is a config.toml problem; Line is 0 when it is not known.
type ConfigError struct {
	Message string
	Line    int
}

func (e *ConfigError) Error() string {
	if e.Line != 0 {
		return fmt.Sprintf("config.toml line %d: %s", e.Line, e.Message)
	}
	return "config.toml: " + e.Message
}

type Config struct {
	Host        string
	User        string
	IdleMinutes float64
	// IdleMinutesInt: config.toml wrote idle_minutes as an integer, so Python reports it as
	// an int (15), not as the default float (15.0).
	IdleMinutesInt bool
	SocksPort      int
	HTTPPort       int
	Useragent      string
	// Profile (universities.json), each field can be overridden in config.toml
	University     string
	UniversityName string
	Usergroup      string
	Authgroup      string
	UsernameSuffix string
	OS             string
	NoExternalAuth bool
	MFA            string
	TOTPSeparator  string
	MFAPortalURL   string
	MFASteps       []string
	DefaultDomains []string
	OpenConnect    string // "" when not set
	OCProxy        string // "" when not set
	AutoUpdate     bool
	// [timing], all in seconds
	ReadyTimeout   float64
	ClientWait     float64
	StopGrace      float64
	ProbeTimeout   float64
	KeyringTimeout float64
	HalfcloseGrace float64
	DemandWindow   float64
	RetryInterval  float64
	Tick           float64
	Backoff        []float64
	Path           string // "" when not loaded from a file
}

// Default is Config() in Python. Without "university" in config.toml everything is as
// before: Heidelberg.
func Default() Config {
	cfg := Config{
		IdleMinutes: 15, SocksPort: 1080, HTTPPort: 1081, AutoUpdate: true,
		ReadyTimeout: 45, ClientWait: 25, StopGrace: 15, ProbeTimeout: 5, KeyringTimeout: 20,
		HalfcloseGrace: 60, DemandWindow: 60, RetryInterval: 30, Tick: 5,
		Backoff: []float64{5, 10, 20, 40, 80, 300},
	}
	p, _ := unis.Get(unis.DefaultID)
	ApplyProfile(&cfg, p)
	return cfg
}

func (c *Config) NeedsTOTP() bool { return slices.Contains(unis.TOTPModes, c.MFA) }

// LoginName is the user name openconnect sends: with the profile's realm unless the user typed one.
func (c *Config) LoginName() string {
	if strings.Contains(c.User, "@") || c.UsernameSuffix == "" {
		return c.User
	}
	return c.User + c.UsernameSuffix
}

// setProfileField sets one of unis.ProfileFields to a value CheckField returned.
func (c *Config) setProfileField(name string, value any) {
	if unis.IsBoolField(name) {
		c.NoExternalAuth = value.(bool)
		return
	}
	s := value.(string)
	switch name {
	case "host":
		c.Host = s
	case "usergroup":
		c.Usergroup = s
	case "authgroup":
		c.Authgroup = s
	case "username_suffix":
		c.UsernameSuffix = s
	case "useragent":
		c.Useragent = s
	case "os":
		c.OS = s
	case "mfa":
		c.MFA = s
	case "totp_separator":
		c.TOTPSeparator = s
	case "mfa_portal_url":
		c.MFAPortalURL = s
	default:
		panic("config: unknown profile field " + name)
	}
}

func ApplyProfile(cfg *Config, profile unis.Profile) {
	cfg.University = profile.ID
	cfg.UniversityName = profile.Name
	for _, name := range unis.ProfileFields {
		cfg.setProfileField(name, profile.Field(name))
	}
	cfg.MFASteps = slices.Clone(profile.MFASteps)
	cfg.DefaultDomains = slices.Clone(profile.DefaultDomains)
}

// CopyProfile takes over university and profile fields, for example after the setup assistant
// wrote them.
func CopyProfile(target, source *Config) {
	target.University = source.University
	target.UniversityName = source.UniversityName
	target.Host = source.Host
	target.Usergroup = source.Usergroup
	target.Authgroup = source.Authgroup
	target.UsernameSuffix = source.UsernameSuffix
	target.Useragent = source.Useragent
	target.OS = source.OS
	target.NoExternalAuth = source.NoExternalAuth
	target.MFA = source.MFA
	target.TOTPSeparator = source.TOTPSeparator
	target.MFAPortalURL = source.MFAPortalURL
	target.MFASteps = slices.Clone(source.MFASteps)
	target.DefaultDomains = slices.Clone(source.DefaultDomains)
}

// Setting is one key and value, kept in order like a Python dict.
type Setting struct {
	Key   string
	Value any
}

// CheckOverrides normalizes profile fields from the setup assistant or config.toml.
// The error is a *unis.FieldError.
func CheckOverrides(values []Setting) ([]Setting, error) {
	out := make([]Setting, 0, len(values))
	for _, s := range values {
		v, err := unis.CheckField(s.Key, s.Value, true)
		if err != nil {
			return nil, err
		}
		out = append(out, Setting{s.Key, v})
	}
	return out, nil
}

// ProfileConfig is the profile part of a Config for a university id plus overrides.
// The error is a *unis.FieldError.
func ProfileConfig(university string, overrides []Setting) (Config, error) {
	profile, ok := unis.Get(university)
	if !ok {
		return Config{}, &unis.FieldError{Field: "university", Message: "Unknown university " + unis.Repr(university)}
	}
	cfg := Default()
	ApplyProfile(&cfg, profile)
	checked, err := CheckOverrides(overrides)
	if err != nil {
		return Config{}, err
	}
	for _, s := range checked {
		cfg.setProfileField(s.Key, s.Value)
	}
	if cfg.Host == "" {
		return Config{}, &unis.FieldError{Field: "host", Message: "Enter the VPN address, for example vpn.example.edu"}
	}
	return cfg, nil
}

// kind is the Python type tuple a key accepts; its name goes into the message.
type kind string

const (
	kStr    kind = "str"
	kNumber kind = "int/float"
	kInt    kind = "int"
	kBool   kind = "bool"
	kList   kind = "list"
)

var topKeys = map[string]kind{
	"host": kStr, "user": kStr, "idle_minutes": kNumber, "socks_port": kInt, "http_port": kInt,
	"useragent": kStr, "openconnect": kStr, "ocproxy": kStr, "auto_update": kBool, "university": kStr,
	// unis.ProfileFields
	"usergroup": kStr, "authgroup": kStr, "username_suffix": kStr, "os": kStr, "no_external_auth": kBool,
	"mfa": kStr, "totp_separator": kStr, "mfa_portal_url": kStr,
}

var timingKeys = map[string]kind{
	"ready_timeout": kNumber, "client_wait": kNumber, "stop_grace": kNumber, "probe_timeout": kNumber,
	"keyring_timeout": kNumber, "halfclose_grace": kNumber, "demand_window": kNumber,
	"retry_interval": kNumber, "tick": kNumber, "backoff": kList,
}

func (k kind) accepts(v any) bool {
	switch v.(type) {
	case string:
		return k == kStr
	case int64:
		return k == kInt || k == kNumber
	case float64:
		return k == kNumber
	case bool:
		return k == kBool
	case []any, []map[string]any:
		return k == kList
	}
	return false
}

// number is Python's float(v) for an int or float; a bool counts as 0 or 1, as in Python.
func number(v any) (float64, bool) {
	switch v := v.(type) {
	case int: // values handed to SetValues
		return float64(v), true
	case int64:
		return float64(v), true
	case float64:
		return v, true
	case bool:
		if v {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

// numbers converts a list whose items are all numbers.
func numbers(v any) ([]float64, bool) {
	items, ok := v.([]any)
	if !ok {
		return nil, false // an array of tables
	}
	out := make([]float64, 0, len(items))
	for _, item := range items {
		if _, isBool := item.(bool); isBool {
			return nil, false
		}
		f, ok := number(item)
		if !ok {
			return nil, false
		}
		out = append(out, f)
	}
	return out, true
}

func DefaultPath() string { return filepath.Join(platform.ConfigDir(), "config.toml") }

// Python's str.isspace, re's \s for str patterns.
const ws = `[\t\n\v\f\r\x1c-\x1f \x85\xa0\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}]`

func isSpace(r rune) bool { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) }

// splitLines is str.splitlines().
func splitLines(text string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		switch r {
		case '\n', '\r', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
			lines = append(lines, text[start:i])
			if r == '\r' && strings.HasPrefix(text[i+size:], "\n") {
				size++
			}
			start = i + size
		}
		i += size
	}
	if start < len(text) {
		lines = append(lines, text[start:])
	}
	return lines
}

// lineOfKey finds the line of a key; table "" is the top level (None in Python, hence "[None]").
func lineOfKey(text, key, table string) int {
	inTable := table == ""
	header := "[None]"
	if table != "" {
		header = "[" + table + "]"
	}
	re := regexp.MustCompile(`\A` + ws + `*` + regexp.QuoteMeta(key) + ws + `*=`)
	for number, line := range splitLines(text) {
		stripped := strings.TrimFunc(line, isSpace)
		if strings.HasPrefix(stripped, "[") {
			inTable = stripped == header
			continue
		}
		if inTable && re.MatchString(line) {
			return number + 1
		}
	}
	return 0
}

func check(key string, value any, k kind, text, table string) error {
	if !k.accepts(value) {
		return &ConfigError{fmt.Sprintf("'%s' must be %s", key, k), lineOfKey(text, key, table)}
	}
	return nil
}

// readText is Path.read_text(encoding="utf-8"): universal newlines.
func readText(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(data) {
		return "", fmt.Errorf("%s: 'utf-8' codec can't decode the file", path)
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	return strings.ReplaceAll(text, "\r", "\n"), nil
}

// writeText is Path.write_text: text mode writes CRLF on Windows.
func writeText(path, text string) error {
	if runtime.GOOS == "windows" {
		text = strings.ReplaceAll(text, "\n", "\r\n")
	}
	return os.WriteFile(path, []byte(text), 0o666)
}

// decode parses TOML like tomllib, returning the top-level keys and those of each table in
// document order.
func decode(text string) (map[string]any, []toml.Key, error) {
	if strings.HasPrefix(text, "\ufeff") {
		// tomllib refuses a BOM, BurntSushi skips it.
		return nil, nil, &ConfigError{"Invalid statement (at line 1, column 1)", 1}
	}
	data := map[string]any{}
	md, err := toml.Decode(text, &data)
	if err != nil {
		var pe toml.ParseError
		if errors.As(err, &pe) {
			msg := fmt.Sprintf("%s (at line %d, column %d)", pe.Message, pe.Position.Line, pe.Position.Col)
			return nil, nil, &ConfigError{msg, pe.Position.Line}
		}
		return nil, nil, &ConfigError{err.Error(), 0}
	}
	return data, md.Keys(), nil
}

// orderedKeys lists the keys directly below prefix in the order they first appear.
func orderedKeys(keys []toml.Key, prefix ...string) []string {
	var out []string
	for _, k := range keys {
		if len(k) <= len(prefix) || !slices.Equal(k[:len(prefix)], prefix) {
			continue
		}
		if name := k[len(prefix)]; !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}

// Load reads config.toml; path "" is DefaultPath. A problem in the file is a *ConfigError.
func Load(path string) (*Config, error) {
	if path == "" {
		path = DefaultPath()
	}
	text, err := readText(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, &ConfigError{Message: path + " is missing, please run install.sh"}
	}
	if err != nil {
		return nil, err
	}
	data, keys, err := decode(text)
	if err != nil {
		return nil, err
	}

	cfg := Default()
	cfg.Path = path
	university, ok := data["university"]
	if !ok {
		university = unis.DefaultID
	}
	if err := check("university", university, kStr, text, ""); err != nil {
		return nil, err
	}
	profile, ok := unis.Get(university.(string))
	if !ok {
		return nil, &ConfigError{fmt.Sprintf("unknown university '%s', see uni_vpn/universities.json", university),
			lineOfKey(text, "university", "")}
	}
	ApplyProfile(&cfg, profile)
	for _, key := range orderedKeys(keys) {
		value := data[key]
		if key == "timing" {
			table, ok := value.(map[string]any)
			if !ok {
				return nil, &ConfigError{"'timing' must be a table", lineOfKey(text, key, "")}
			}
			for _, tkey := range orderedKeys(keys, "timing") {
				tvalue := table[tkey]
				k, known := timingKeys[tkey]
				if !known {
					return nil, &ConfigError{fmt.Sprintf("unknown key '%s'", tkey), lineOfKey(text, tkey, "timing")}
				}
				if err := check(tkey, tvalue, k, text, "timing"); err != nil {
					return nil, err
				}
				if tkey == "backoff" {
					backoff, ok := numbers(tvalue)
					if !ok {
						return nil, &ConfigError{"'backoff' must be a list of numbers", lineOfKey(text, tkey, "timing")}
					}
					cfg.Backoff = backoff
					continue
				}
				f, _ := number(tvalue)
				cfg.setTiming(tkey, f)
			}
			continue
		}
		k, known := topKeys[key]
		if !known {
			return nil, &ConfigError{fmt.Sprintf("unknown key '%s'", key), lineOfKey(text, key, "")}
		}
		if err := check(key, value, k, text, ""); err != nil {
			return nil, err
		}
		if key == "university" {
			continue
		}
		if slices.Contains(unis.ProfileFields, key) {
			v, err := unis.CheckField(key, value, false)
			if err != nil {
				return nil, &ConfigError{err.Error(), lineOfKey(text, key, "")}
			}
			cfg.setProfileField(key, v)
			continue
		}
		cfg.setTop(key, value)
	}

	if cfg.User == "" {
		return nil, &ConfigError{Message: "'user' (university ID) is missing"}
	}
	if cfg.Host == "" {
		return nil, &ConfigError{Message: `'host' is missing, it is needed with university = "other"`}
	}
	for _, p := range []struct {
		name string
		port int
	}{{"socks_port", cfg.SocksPort}, {"http_port", cfg.HTTPPort}} {
		if p.port < 1 || p.port > 65535 {
			return nil, &ConfigError{fmt.Sprintf("'%s' must be between 1 and 65535", p.name), lineOfKey(text, p.name, "")}
		}
	}
	if cfg.SocksPort == cfg.HTTPPort {
		return nil, &ConfigError{Message: "'socks_port' and 'http_port' must differ"}
	}
	if cfg.IdleMinutes <= 0 {
		return nil, &ConfigError{"'idle_minutes' must be greater than 0", lineOfKey(text, "idle_minutes", "")}
	}
	if len(cfg.Backoff) == 0 {
		return nil, &ConfigError{"'backoff' must not be empty", lineOfKey(text, "backoff", "timing")}
	}
	return &cfg, nil
}

// setTop sets a top-level key that is not a profile field, after its type was checked.
func (c *Config) setTop(key string, value any) {
	switch key {
	case "host":
		c.Host = value.(string)
	case "user":
		c.User = value.(string)
	case "useragent":
		c.Useragent = value.(string)
	case "openconnect":
		c.OpenConnect = value.(string)
	case "ocproxy":
		c.OCProxy = value.(string)
	case "auto_update":
		c.AutoUpdate = value.(bool)
	case "idle_minutes":
		c.IdleMinutes, _ = number(value)
		_, c.IdleMinutesInt = value.(int64)
	case "socks_port":
		c.SocksPort = int(value.(int64))
	case "http_port":
		c.HTTPPort = int(value.(int64))
	}
}

func (c *Config) setTiming(key string, f float64) {
	switch key {
	case "ready_timeout":
		c.ReadyTimeout = f
	case "client_wait":
		c.ClientWait = f
	case "stop_grace":
		c.StopGrace = f
	case "probe_timeout":
		c.ProbeTimeout = f
	case "keyring_timeout":
		c.KeyringTimeout = f
	case "halfclose_grace":
		c.HalfcloseGrace = f
	case "demand_window":
		c.DemandWindow = f
	case "retry_interval":
		c.RetryInterval = f
	case "tick":
		c.Tick = f
	}
}

const Template = `# uni-vpn configuration
university = "{university}"  # profile from uni_vpn/universities.json, "other" if not listed
user = "{user}"          # university ID
{overrides}idle_minutes = 15        # tear the tunnel down after this many minutes without traffic
socks_port = 1080        # SOCKS5 proxy for the browser
http_port = 1081         # status page http://127.0.0.1:1081
# openconnect = "/usr/sbin/openconnect"
# ocproxy = "/usr/bin/ocproxy"
`

// TOMLValue writes a value for config.toml: a bool or an int as is, anything else as a string.
func TOMLValue(value any) string {
	switch v := value.(type) {
	case bool:
		if v {
			return "true"
		}
		return "false"
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	}
	s, ok := value.(string)
	if !ok {
		s = unis.Repr(value) // str() of a number or list
	}
	// A JSON string (json.dumps, ensure_ascii=False) is a valid TOML basic string.
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
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// mkdirParent is path.parent.mkdir(parents=True, exist_ok=True, mode=0o700): only the last
// directory gets the mode.
func mkdirParent(path string) error {
	dir := filepath.Dir(path)
	err := os.Mkdir(dir, 0o700)
	if errors.Is(err, fs.ErrNotExist) {
		if err = os.MkdirAll(filepath.Dir(dir), 0o777); err == nil {
			err = os.Mkdir(dir, 0o700)
		}
	}
	if errors.Is(err, fs.ErrExist) {
		if st, serr := os.Stat(dir); serr == nil && st.IsDir() {
			return nil
		}
	}
	return err
}

// WriteInitial writes a new config.toml (pass unis.DefaultID for no particular university).
func WriteInitial(path, user, university string, overrides []Setting) error {
	if !ValidUser(user) {
		return &ConfigError{Message: "invalid university ID: " + unis.Repr(user)}
	}
	checked, err := CheckOverrides(overrides)
	if err == nil {
		_, err = ProfileConfig(university, checked)
	}
	if err != nil {
		return &ConfigError{Message: err.Error()}
	}
	var lines strings.Builder
	for _, s := range checked {
		fmt.Fprintf(&lines, "%s = %s\n", s.Key, TOMLValue(s.Value))
	}
	if err := mkdirParent(path); err != nil {
		return err
	}
	text := strings.NewReplacer("{university}", university, "{user}", user, "{overrides}", lines.String()).Replace(Template)
	if err := writeText(path, text); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// "ab123", or with a realm the university asks for: "st123456@stud.uni-stuttgart.de"
var userRE = regexp.MustCompile(`\A[A-Za-z0-9][A-Za-z0-9._-]{0,63}(?:@[A-Za-z0-9][A-Za-z0-9.-]{0,252})?\z`)

func ValidUser(user string) bool { return userRE.MatchString(user) }

// SetUser writes a new university ID into an existing config.toml, keeping everything else.
func SetUser(path, user string) error {
	if !ValidUser(user) {
		return &ConfigError{Message: "invalid university ID: " + unis.Repr(user)}
	}
	return SetValues(path, []Setting{{"user", user}})
}

// A basic ("...") or literal ('...') string, a boolean or an integer, as written by hand.
const valueRE = `(?:"(?:[^"\\\n]|\\.)*"|'[^'\n]*'|true|false|[0-9]+\b)`

// pyEqual is Python's == between a decoded TOML value and a str or bool.
func pyEqual(a, b any) bool {
	if s, ok := a.(string); ok {
		t, ok := b.(string)
		return ok && s == t
	}
	if _, ok := b.(string); ok {
		return false
	}
	x, ok1 := number(a)
	y, ok2 := number(b)
	return ok1 && ok2 && x == y
}

// SetValues sets top-level keys in an existing config.toml, keeping comments, order and
// everything else.
func SetValues(path string, values []Setting) error {
	text, err := readText(path)
	if err != nil {
		return err
	}
	updated := text
	for _, s := range values {
		literal := TOMLValue(s.Value)
		re := regexp.MustCompile(`(?m)^(` + ws + `*` + regexp.QuoteMeta(s.Key) + ws + `*=` + ws + `*)` + valueRE)
		if m := re.FindStringSubmatchIndex(updated); m != nil {
			updated = updated[:m[0]] + updated[m[2]:m[3]] + literal + updated[m[1]:]
			continue
		}
		head, rest, found := strings.Cut(updated, "\n[")
		updated = strings.TrimRight(head, "\n") + fmt.Sprintf("\n%s = %s\n", s.Key, literal)
		if found {
			updated += "\n[" + rest
		}
	}
	// Forms the regex does not know ('''...''', "user" = ...) must not end in a broken file.
	written, _, err := decode(updated)
	if err != nil {
		written = map[string]any{}
	}
	for _, s := range values {
		if v, ok := written[s.Key]; !ok || !pyEqual(v, s.Value) {
			keys := make([]string, len(values))
			for i, s := range values {
				keys[i] = s.Key
			}
			return &ConfigError{Message: fmt.Sprintf("could not change %s in %s, please edit it by hand",
				strings.Join(keys, ", "), path)}
		}
	}
	if updated != text {
		return writeText(path, updated)
	}
	return nil
}

var tableStart = regexp.MustCompile(`(?m)^[ \t]*\[`)

// RemoveKeys deletes top-level keys from an existing config.toml, keeping everything else.
// Used when the university changes: overrides that belonged to the old profile must not
// stay behind.
func RemoveKeys(path string, keys []string) error {
	text, err := readText(path)
	if err != nil {
		return err
	}
	head, rest := text, ""
	if loc := tableStart.FindStringIndex(text); loc != nil {
		head, rest = text[:loc[0]], text[loc[0]:]
	}
	for _, key := range keys {
		re := regexp.MustCompile(`(?m)^[ \t]*` + regexp.QuoteMeta(key) + `[ \t]*=[ \t]*` + valueRE +
			`[ \t]*(?:#[^\n]*)?(?:\n|$)`)
		head = re.ReplaceAllLiteralString(head, "")
	}
	updated := head + rest
	written, _, err := decode(updated)
	broken := err != nil
	for _, key := range keys {
		if _, ok := written[key]; ok {
			broken = true
		}
	}
	if broken {
		return &ConfigError{Message: fmt.Sprintf("could not remove %s from %s, please edit it by hand",
			strings.Join(keys, ", "), path)}
	}
	if updated != text {
		return writeText(path, updated)
	}
	return nil
}

var portLine = regexp.MustCompile(`\A` + ws + `*(socks_port|http_port)` + ws + `*=` + ws + `*([0-9]{1,5})` + ws + `*(#.*)?\z`)

// PortsFromBroken reads the ports from a broken config.toml as far as possible; path "" is
// DefaultPath. Keys are "socks_port" and "http_port".
//
// Even then the status page must be reachable where browsers and the user expect
// it, otherwise nobody sees the error message with the line number.
func PortsFromBroken(path string) map[string]int {
	if path == "" {
		path = DefaultPath()
	}
	ports := map[string]int{}
	text, err := readText(path)
	if err != nil {
		return ports
	}
	for _, line := range splitLines(text) {
		if strings.HasPrefix(strings.TrimFunc(line, isSpace), "[") {
			break // tables like [timing] from here on, no more top-level keys
		}
		m := portLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if port, _ := strconv.Atoi(m[2]); port >= 1 && port <= 65535 {
			ports[m[1]] = port
		}
	}
	if len(ports) == 2 && ports["socks_port"] == ports["http_port"] {
		return map[string]int{}
	}
	return ports
}
