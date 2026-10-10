// Package universities holds the university profiles (universities.json): gateway, groups,
// client quirks, second factor. It mirrors uni_vpn/universities.py.
package universities

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/DavidVinu/uni-vpn/internal/i18n"
	assets "github.com/DavidVinu/uni-vpn/uni_vpn"
)

const (
	DefaultID        = "heidelberg" // config.toml without "university"
	OtherID          = "other"      // a university that is not listed: everything comes from config.toml
	DefaultUseragent = "AnyConnect Linux_64 5.1.18.314"
)

var (
	MFAModes  = []string{"none", "totp_field", "totp_append", "duo_push", "saml"}
	TOTPModes = []string{"totp_field", "totp_append"}
	OSValues  = []string{"", "linux", "linux-64", "win", "mac-intel", "android", "apple-ios"}
	OSChoices = `"", linux, linux-64, win, mac-intel, android, apple-ios`
)

// ProfileFields are the profile fields config.toml may override, in the Python order.
var ProfileFields = []string{"host", "usergroup", "authgroup", "username_suffix", "useragent",
	"os", "no_external_auth", "mfa", "totp_separator", "mfa_portal_url"}

// IsBoolField reports whether a profile field is a bool (all others are text).
func IsBoolField(name string) bool { return name == "no_external_auth" }

const label = `[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?`

var (
	idRE     = regexp.MustCompile(`\A[a-z0-9][a-z0-9-]{0,39}\z`)
	domainRE = regexp.MustCompile(`\A` + label + `(?:\.` + label + `)+\z`)
	hostRE   = regexp.MustCompile(`\A` + label + `(?:\.` + label + `)+(?::[0-9]{1,5})?\z`)
	// config.toml written by hand before profiles existed: anything openconnect accepts (a URL too).
	looseHostRE = regexp.MustCompile(`\A[^\t\n\v\f\r\x1c-\x1f \x85\xa0\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}"\\]{1,255}\z`)
	usergroupRE = regexp.MustCompile(`\A[A-Za-z0-9._~+-]{0,64}(?:/[A-Za-z0-9._~+-]{1,64}){0,3}\z`)
	// Printable, no quotes or backslashes: these go into argv and config.toml as they are.
	textRE = regexp.MustCompile(`\A[^\x00-\x1f\x7f"\\]{0,128}\z`)
	urlRE  = regexp.MustCompile(`\Ahttps://[A-Za-z0-9.-]+(?::[0-9]{1,5})?(?:/[A-Za-z0-9._~%/?&=+-]*)?\z`)
)

// Python's lookaheads: the whole name at most 253 characters, for a host the part before the port.
func domainOK(s string) bool { return utf8.RuneCountInString(s) <= 253 && domainRE.MatchString(s) }

func hostOK(s string) bool {
	name, _, _ := strings.Cut(s, ":")
	return utf8.RuneCountInString(name) <= 253 && hostRE.MatchString(s)
}

// FieldError is a profile value that is not allowed; Field names it for the setup assistant.
type FieldError struct {
	Field   string
	Message string
	Text    *i18n.Text // the message as a catalog text, nil when it has none
}

func (e *FieldError) Error() string { return e.Message }

// Unwrap hands the catalog text to i18n.Of.
func (e *FieldError) Unwrap() error {
	if e.Text == nil {
		return nil
	}
	return *e.Text
}

// TextError is a FieldError with a catalog text.
func TextError(field string, text i18n.Text) *FieldError {
	return &FieldError{Field: field, Message: text.String(), Text: &text}
}

type Profile struct {
	ID             string
	Name           string
	Country        string
	Host           string
	Usergroup      string
	Authgroup      string
	UsernameSuffix string
	Useragent      string
	OS             string
	NoExternalAuth bool
	MFA            string
	TOTPSeparator  string
	MFAPortalURL   string
	MFASteps       []string
	DefaultDomains []string
	Aliases        []string
	Verified       bool
	Notes          string
}

// PublicProfile is what the setup assistant gets: everything except the notes for contributors.
type PublicProfile struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Country        string   `json:"country"`
	Host           string   `json:"host"`
	Usergroup      string   `json:"usergroup"`
	Authgroup      string   `json:"authgroup"`
	UsernameSuffix string   `json:"username_suffix"`
	Useragent      string   `json:"useragent"`
	OS             string   `json:"os"`
	NoExternalAuth bool     `json:"no_external_auth"`
	MFA            string   `json:"mfa"`
	TOTPSeparator  string   `json:"totp_separator"`
	MFAPortalURL   string   `json:"mfa_portal_url"`
	MFASteps       []string `json:"mfa_steps"`
	DefaultDomains []string `json:"default_domains"`
	Aliases        []string `json:"aliases"`
	Verified       bool     `json:"verified"`
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return slices.Clone(s)
}

func (p *Profile) Public() PublicProfile {
	return PublicProfile{
		ID: p.ID, Name: p.Name, Country: p.Country, Host: p.Host, Usergroup: p.Usergroup,
		Authgroup: p.Authgroup, UsernameSuffix: p.UsernameSuffix, Useragent: p.Useragent, OS: p.OS,
		NoExternalAuth: p.NoExternalAuth, MFA: p.MFA, TOTPSeparator: p.TOTPSeparator,
		MFAPortalURL: p.MFAPortalURL, MFASteps: nonNil(p.MFASteps),
		DefaultDomains: nonNil(p.DefaultDomains), Aliases: nonNil(p.Aliases), Verified: p.Verified,
	}
}

func newProfile(id, name string) Profile {
	return Profile{ID: id, Name: name, Useragent: DefaultUseragent, NoExternalAuth: true, MFA: "none",
		MFASteps: []string{}, DefaultDomains: []string{}, Aliases: []string{}}
}

// Other is the profile for a university that is not listed.
var Other = newProfile(OtherID, "Other university")

// Field returns a profile field by its ProfileFields name.
func (p *Profile) Field(name string) any {
	switch name {
	case "host":
		return p.Host
	case "usergroup":
		return p.Usergroup
	case "authgroup":
		return p.Authgroup
	case "username_suffix":
		return p.UsernameSuffix
	case "useragent":
		return p.Useragent
	case "os":
		return p.OS
	case "no_external_auth":
		return p.NoExternalAuth
	case "mfa":
		return p.MFA
	case "totp_separator":
		return p.TOTPSeparator
	case "mfa_portal_url":
		return p.MFAPortalURL
	}
	panic("universities: unknown profile field " + name)
}

func (p *Profile) setField(name string, value any) {
	if IsBoolField(name) {
		p.NoExternalAuth = value.(bool)
		return
	}
	s := value.(string)
	switch name {
	case "host":
		p.Host = s
	case "usergroup":
		p.Usergroup = s
	case "authgroup":
		p.Authgroup = s
	case "username_suffix":
		p.UsernameSuffix = s
	case "useragent":
		p.Useragent = s
	case "os":
		p.OS = s
	case "mfa":
		p.MFA = s
	case "totp_separator":
		p.TOTPSeparator = s
	case "mfa_portal_url":
		p.MFAPortalURL = s
	}
}

// CheckField validates one profile value and returns it normalized (a string or a bool).
//
// strict=false is for config.toml: a host written by hand before profiles existed may be
// anything openconnect accepts (a URL, a single name), so only whitespace and quotes are refused.
func CheckField(name string, value any, strict bool) (any, error) {
	if !slices.Contains(ProfileFields, name) {
		return nil, &FieldError{Field: name, Message: fmt.Sprintf("unknown setting '%s'", name)}
	}
	if IsBoolField(name) {
		if b, ok := value.(bool); ok {
			return b, nil
		}
		return nil, &FieldError{Field: name, Message: fmt.Sprintf("'%s' must be true or false", name)}
	}
	s, ok := value.(string)
	if !ok {
		return nil, &FieldError{Field: name, Message: fmt.Sprintf("'%s' must be text", name)}
	}
	switch {
	case name == "host" && !strict:
		if !looseHostRE.MatchString(s) {
			return nil, &FieldError{Field: name, Message: "'host' must not be empty or contain spaces or quotes"}
		}
	case name == "host":
		s = strings.TrimRight(lower(strip(s)), ".")
		if !hostOK(s) {
			return nil, TextError(name, i18n.T("address.invalid"))
		}
	case name == "usergroup":
		s = strings.Trim(strip(s), "/")
		if !usergroupRE.MatchString(s) {
			return nil, TextError(name, i18n.T("address.path"))
		}
	case name == "mfa":
		if !slices.Contains(MFAModes, s) {
			return nil, &FieldError{Field: name, Message: "'mfa' must be one of " + strings.Join(MFAModes, ", ")}
		}
	case name == "os":
		if !slices.Contains(OSValues, s) {
			return nil, &FieldError{Field: name, Message: "'os' must be one of " + OSChoices}
		}
	case name == "mfa_portal_url":
		if s != "" && !urlRE.MatchString(s) {
			return nil, &FieldError{Field: name, Message: "'mfa_portal_url' must be an https:// address"}
		}
	case !textRE.MatchString(s):
		return nil, &FieldError{Field: name, Message: fmt.Sprintf("'%s' must not contain quotes, backslashes or line breaks", name)}
	}
	return s, nil
}

var entryKeys = []string{"id", "name", "country", "host", "usergroup", "authgroup", "username_suffix",
	"useragent", "os", "no_external_auth", "mfa", "totp_separator", "mfa_portal_url", "mfa_steps",
	"default_domains", "aliases", "verified", "notes"}

// isOne is Python's `== 1` on a JSON value: 1, 1.0 and true all qualify.
func isOne(v any) bool {
	switch v := v.(type) {
	case bool:
		return v
	case json.Number:
		f, err := v.Float64()
		return err == nil && f == 1
	}
	return false
}

// Parse reads the registry from universities.json, in file order. The error names the entry.
func Parse(data []byte) ([]Profile, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var raw any
	if err := dec.Decode(&raw); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("universities.json: extra data after the JSON value")
	}
	top, _ := raw.(map[string]any)
	list, isList := top["universities"].([]any)
	if top == nil || !isOne(top["version"]) || !isList {
		return nil, fmt.Errorf(`universities.json: expected {"version": 1, "universities": [...]}`)
	}
	var profiles []Profile
	seen := map[string]bool{}
	for number, item := range list {
		where := fmt.Sprintf("universities.json entry %d", number+1)
		entry, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s: not an object", where)
		}
		var unknown []string
		for key := range entry {
			if !slices.Contains(entryKeys, key) {
				unknown = append(unknown, key)
			}
		}
		if len(unknown) > 0 {
			sort.Strings(unknown)
			return nil, fmt.Errorf("%s: unknown key %s", where, Repr(unknown[0]))
		}
		uid, ok := entry["id"].(string)
		if !ok || !idRE.MatchString(uid) || uid == OtherID {
			return nil, fmt.Errorf("%s: invalid id %s", where, Repr(entry["id"]))
		}
		if seen[uid] {
			return nil, fmt.Errorf("%s: duplicate id %s", where, Repr(uid))
		}
		p := newProfile(uid, "")
		for _, key := range []string{"name", "host"} {
			if s, ok := entry[key].(string); !ok || strip(s) == "" {
				return nil, fmt.Errorf("%s: '%s' is required", uid, key)
			}
		}
		p.Name = entry["name"].(string)
		for _, key := range []string{"country", "notes"} {
			v, present := entry[key]
			s, ok := v.(string)
			if present && !ok {
				return nil, fmt.Errorf("%s: '%s' must be text", uid, key)
			}
			if key == "country" {
				p.Country = s
			} else {
				p.Notes = s
			}
		}
		if v, present := entry["verified"]; present {
			b, ok := v.(bool)
			if !ok {
				return nil, fmt.Errorf("%s: 'verified' must be true or false", uid)
			}
			p.Verified = b
		}
		for _, key := range []string{"mfa_steps", "default_domains", "aliases"} {
			items := []string{}
			if v, present := entry[key]; present {
				arr, ok := v.([]any)
				if !ok {
					return nil, fmt.Errorf("%s: '%s' must be a list of text", uid, key)
				}
				for _, i := range arr {
					s, ok := i.(string)
					if !ok {
						return nil, fmt.Errorf("%s: '%s' must be a list of text", uid, key)
					}
					items = append(items, s)
				}
			}
			switch key {
			case "mfa_steps":
				p.MFASteps = items
			case "default_domains":
				p.DefaultDomains = items
			default:
				p.Aliases = items
			}
		}
		for _, domain := range p.DefaultDomains {
			if !domainOK(domain) {
				return nil, fmt.Errorf("%s: '%s' is not a host name", uid, domain)
			}
		}
		for _, key := range ProfileFields {
			v, present := entry[key]
			if !present {
				continue
			}
			value, err := CheckField(key, v, true)
			if err != nil {
				return nil, fmt.Errorf("%s: %s", uid, err)
			}
			p.setField(key, value)
		}
		seen[uid] = true
		profiles = append(profiles, p)
	}
	return profiles, nil
}

var (
	registryOnce sync.Once
	registry     []Profile
	byID         map[string]int

	foldOnce sync.Once
	foldMap  map[rune]string
)

// Registry returns the shipped profiles in file order. A broken registry is a build error.
func Registry() []Profile {
	registryOnce.Do(func() {
		profiles, err := Parse(assets.Universities)
		if err != nil {
			panic(err)
		}
		registry = profiles
		byID = make(map[string]int, len(profiles))
		for i, p := range profiles {
			byID[p.ID] = i
		}
	})
	return registry
}

// Get returns the profile for an id from config.toml; Other for "other", false if unknown.
func Get(uid string) (Profile, bool) {
	if uid == OtherID {
		return Other, true
	}
	Registry()
	i, ok := byID[uid]
	if !ok {
		return Profile{}, false
	}
	return registry[i], true
}

func sortedByName() []Profile {
	out := slices.Clone(Registry())
	sort.SliceStable(out, func(i, j int) bool { return lower(out[i].Name) < lower(out[j].Name) })
	return out
}

func PublicList() []PublicProfile {
	var out []PublicProfile
	for _, p := range sortedByName() {
		out = append(out, p.Public())
	}
	return out
}

// Search returns the profiles whose id, name, alias or host contains the text; an exact id wins.
func Search(text string) []Profile {
	query := Fold(strip(text))
	if query == "" {
		return nil
	}
	Registry()
	if i, ok := byID[query]; ok {
		return []Profile{registry[i]}
	}
	var out []Profile
	for _, p := range sortedByName() {
		for _, t := range append([]string{p.ID, p.Name, p.Host}, p.Aliases...) {
			if strings.Contains(Fold(t), query) {
				out = append(out, p)
				break
			}
		}
	}
	return out
}
