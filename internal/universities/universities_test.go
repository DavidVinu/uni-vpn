package universities

import (
	"encoding/json"
	"errors"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
)

func ids(profiles []Profile) []string {
	out := []string{}
	for _, p := range profiles {
		out = append(out, p.ID)
	}
	return out
}

func mustGet(t *testing.T, id string) Profile {
	t.Helper()
	p, ok := Get(id)
	if !ok {
		t.Fatalf("no profile %q", id)
	}
	return p
}

func TestShippedRegistryLoads(t *testing.T) {
	want := []string{
		"heidelberg", "ethz", "bremen", "muenster", "marburg", "stanford", "harvard-fasrc", "stuttgart",
		"bonn", "mannheim", "kassel", "tu-dresden", "fu-berlin", "oxford", "tamu", "ucf", "osu", "utexas",
		"asu", "ufl", "umn", "uiuc", "uga", "unc", "ncsu", "uwaterloo", "uottawa", "carleton", "hku", "ugr", "uni-halle",
		"hs-flensburg", "w-hs", "hs-offenburg", "uni-hamburg", "fau", "uni-jena", "uni-weimar", "th-koeln", "thu",
		"tu-braunschweig"}
	got := ids(Registry())
	sort.Strings(got)
	sort.Strings(want)
	if !slices.Equal(got, want) {
		t.Fatalf("registry ids = %v", got)
	}
}

func TestHeidelbergKeepsTodaysValues(t *testing.T) {
	hd := mustGet(t, "heidelberg")
	if hd.Host != "vpn-ac.uni-heidelberg.de" || hd.Useragent != "AnyConnect Linux_64 5.1.18.314" || hd.MFA != "totp_field" {
		t.Fatalf("heidelberg = %+v", hd)
	}
	if hd.NoExternalAuth {
		t.Error("today's command line has no --no-external-auth")
	}
	if hd.Usergroup != "" || hd.Authgroup != "" || hd.UsernameSuffix != "" || hd.OS != "" {
		t.Errorf("groups/suffix/os not empty: %+v", hd)
	}
	if hd.MFAPortalURL != "https://mfa.uni-heidelberg.de/" {
		t.Errorf("portal = %q", hd.MFAPortalURL)
	}
	want := []string{"sogo.uni-heidelberg.de", "elearning-med.uni-heidelberg.de", "cip.dmed.uni-heidelberg.de",
		"heico.uni-heidelberg.de"}
	if !slices.Equal(hd.DefaultDomains, want) {
		t.Errorf("default_domains = %v", hd.DefaultDomains)
	}
	if !strings.Contains(hd.MFASteps[0], "{portal}") {
		t.Errorf("mfa_steps[0] = %q", hd.MFASteps[0])
	}
}

func TestOnlyHeidelbergIsVerified(t *testing.T) {
	var verified []string
	for _, p := range Registry() {
		if p.Verified {
			verified = append(verified, p.ID)
		}
	}
	if !slices.Equal(verified, []string{"heidelberg"}) {
		t.Fatalf("verified = %v", verified)
	}
}

func TestSAMLUniversitiesAreMarked(t *testing.T) {
	var saml []string
	for _, p := range Registry() {
		if p.MFA == "saml" {
			saml = append(saml, p.ID)
		}
	}
	sort.Strings(saml)
	if !slices.Equal(saml, []string{"fu-berlin", "oxford"}) {
		t.Fatalf("saml = %v", saml)
	}
}

func TestProfilesFromTheProbes(t *testing.T) {
	tests := []struct{ id, field, got, want string }{
		{"marburg", "authgroup", mustGet(t, "marburg").Authgroup, "unimr-vpn-staff-Passwort+2FA"},
		{"marburg", "mfa", mustGet(t, "marburg").MFA, "totp_append"},
		{"stanford", "authgroup", mustGet(t, "stanford").Authgroup, "Stanford"},
		{"stanford", "mfa", mustGet(t, "stanford").MFA, "duo_push"},
		{"ethz", "username_suffix", mustGet(t, "ethz").UsernameSuffix, "@staff-net.ethz.ch"},
		{"kassel", "os", mustGet(t, "kassel").OS, "win"},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s %s = %q, want %q", tt.id, tt.field, tt.got, tt.want)
		}
	}
}

const entry = `{"id": "example", "name": "Example University", "host": "vpn.example.edu"`

// registryWith is the one-entry registry with extra JSON members, e.g. `"mfa": "sms"`.
func registryWith(extra string) []byte {
	e := entry + "}"
	if extra != "" {
		// Later members win, as in Python's dict(ENTRY, **changes).
		e = entry + ", " + extra + "}"
	}
	return []byte(`{"version": 1, "universities": [` + e + `]}`)
}

func TestNewEntriesDefaultToNoExternalAuth(t *testing.T) {
	if !mustGet(t, "bonn").NoExternalAuth {
		t.Error("bonn without no_external_auth")
	}
	profiles, err := Parse(registryWith(""))
	if err != nil || !profiles[0].NoExternalAuth {
		t.Fatalf("parse = %v, %v", profiles, err)
	}
}

func TestOtherAndUnknownIDs(t *testing.T) {
	other := mustGet(t, "other")
	if other.ID != "other" || other.Host != "" {
		t.Errorf("other = %+v", other)
	}
	if _, ok := Get("nowhere"); ok {
		t.Error("nowhere found")
	}
}

func TestPublicListIsSortedAndHasNoNotes(t *testing.T) {
	entries := PublicList()
	var names []string
	for _, e := range entries {
		names = append(names, strings.ToLower(e.Name))
	}
	if !slices.IsSorted(names) {
		t.Errorf("not sorted: %v", names)
	}
	data, err := json.Marshal(entries) // served as JSON as it is
	if err != nil {
		t.Fatal(err)
	}
	var decoded []map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, e := range decoded {
		if _, ok := e["notes"]; ok {
			t.Fatalf("notes in %v", e["id"])
		}
		if e["aliases"] == nil || e["mfa_steps"] == nil || e["default_domains"] == nil {
			t.Fatalf("null list in %v", e["id"])
		}
	}
}

func TestSearchIgnoresCaseAndUmlautsAndPrefersAnExactID(t *testing.T) {
	tests := []struct {
		query string
		want  []string
	}{
		{"Zürich", []string{"ethz"}},
		{"zuerich", []string{"ethz"}},
		{"MÜNSTER", []string{"muenster"}},
		{"fasrc", []string{"harvard-fasrc"}},
		{"bonn", []string{"bonn"}},
		{"   ", []string{}},
	}
	for _, tt := range tests {
		if got := ids(Search(tt.query)); !slices.Equal(got, tt.want) {
			t.Errorf("Search(%q) = %v, want %v", tt.query, got, tt.want)
		}
	}
}

func TestFold(t *testing.T) {
	tests := map[string]string{"Zürich": "zurich", "MÜNSTER": "munster", "ﬁ½": "fi12", "Straße": "stra", "Ærø": "r"}
	for in, want := range tests {
		if got := Fold(in); got != want {
			t.Errorf("Fold(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReadmeListsEveryUniversity(t *testing.T) {
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range Registry() {
		if !strings.Contains(string(readme), "| "+p.Name+" | "+p.Host+" |") {
			t.Errorf("README misses %s", p.ID)
		}
	}
}

func TestRefusesBrokenEntries(t *testing.T) {
	tests := []struct {
		data []byte
		text string
	}{
		{registryWith(`"mfa": "sms"`), "mfa"},
		{registryWith(`"host": "not a host"`), "VPN address"},
		{registryWith(`"colour": "red"`), "colour"},
		{registryWith(`"id": "other"`), "invalid id"},
		{registryWith(`"id": "Bad_Id"`), "invalid id"},
		{registryWith(`"default_domains": ["localhost"]`), "localhost"},
		{registryWith(`"no_external_auth": "yes"`), "true or false"},
		{registryWith(`"authgroup": "a\"b"`), "quotes"},
		{registryWith(`"name": ""`), "'name' is required"},
		{[]byte(`{"version": 2, "universities": []}`), "version"},
		{[]byte(`{"version": 1, "universities": [` + entry + `}, ` + entry + `}]}`), "duplicate"},
	}
	for _, tt := range tests {
		_, err := Parse(tt.data)
		if err == nil || !strings.Contains(err.Error(), tt.text) {
			t.Errorf("Parse(%s) = %v, want %q", tt.data, err, tt.text)
		}
	}
}

func TestParseMessages(t *testing.T) {
	tests := []struct {
		data []byte
		want string
	}{
		{registryWith(`"colour": "red", "aaa": 1`), "universities.json entry 1: unknown key 'aaa'"},
		{registryWith(`"id": 5`), "universities.json entry 1: invalid id 5"},
		{registryWith(`"id": null`), "universities.json entry 1: invalid id None"},
		{registryWith(`"id": "it's"`), `universities.json entry 1: invalid id "it's"`},
		{registryWith(`"mfa": "sms"`), "example: 'mfa' must be one of none, totp_field, totp_append, duo_push, saml"},
		{registryWith(`"aliases": "x"`), "example: 'aliases' must be a list of text"},
		{registryWith(`"country": 1`), "example: 'country' must be text"},
	}
	for _, tt := range tests {
		if _, err := Parse(tt.data); err == nil || err.Error() != tt.want {
			t.Errorf("Parse(%s) = %v, want %q", tt.data, err, tt.want)
		}
	}
}

func TestCheckFieldNormalizes(t *testing.T) {
	tests := []struct {
		name   string
		value  any
		strict bool
		want   any
	}{
		{"host", " VPN.Example.EDU. ", true, "vpn.example.edu"},
		{"usergroup", "/exchange/", true, "exchange"},
		{"host", "vpn.example.edu:8443", true, "vpn.example.edu:8443"},
		{"host", "https://vpn.example.edu/", false, "https://vpn.example.edu/"},
		// Group names with spaces and plus are fine.
		{"authgroup", "RWTH-VPN (Split Tunnel)", true, "RWTH-VPN (Split Tunnel)"},
		{"authgroup", "unimr-vpn-staff-Passwort+2FA", true, "unimr-vpn-staff-Passwort+2FA"},
	}
	for _, tt := range tests {
		got, err := CheckField(tt.name, tt.value, tt.strict)
		if err != nil || got != tt.want {
			t.Errorf("CheckField(%q, %q) = %v, %v", tt.name, tt.value, got, err)
		}
	}
}

func TestCheckFieldNamesTheField(t *testing.T) {
	tests := []struct {
		name  string
		value any
	}{
		{"host", "vpn example"}, {"host", "vpn.example.edu/staff"}, {"mfa", "sms"}, {"os", "beos"},
		{"usergroup", "a b"}, {"mfa_portal_url", "http://x.example"}, {"authgroup", "a\nb"},
		{"useragent", int64(5)}, {"no_external_auth", "true"}, {"colour", "red"},
	}
	for _, tt := range tests {
		_, err := CheckField(tt.name, tt.value, true)
		var fe *FieldError
		if !errors.As(err, &fe) || fe.Field != tt.name {
			t.Errorf("CheckField(%q, %v) = %v", tt.name, tt.value, err)
		}
	}
}

func TestRepr(t *testing.T) {
	tests := []struct {
		in   any
		want string
	}{
		{"ab", "'ab'"}, {"a'b", `"a'b"`}, {`a'"b`, `'a\'"b'`}, {"a\nb\\", `'a\nb\\'`}, {"\x00é\u200b", "'\\x00é\\u200b'"},
		{nil, "None"}, {true, "True"}, {1.5, "1.5"}, {2.0, "2.0"}, {1e16, "1e+16"}, {0.00001, "1e-05"},
		{json.Number("5"), "5"}, {[]any{"a", int64(1)}, "['a', 1]"},
	}
	for _, tt := range tests {
		if got := Repr(tt.in); got != tt.want {
			t.Errorf("Repr(%#v) = %s, want %s", tt.in, got, tt.want)
		}
	}
}
