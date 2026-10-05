package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	unis "github.com/DavidVinu/uni-vpn/internal/universities"
)

func write(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func load(t *testing.T, path string) *Config {
	t.Helper()
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

func loadErr(t *testing.T, path string) *ConfigError {
	t.Helper()
	_, err := Load(path)
	var ce *ConfigError
	if !errors.As(err, &ce) {
		t.Fatalf("Load = %v, want a ConfigError", err)
	}
	return ce
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestDefaultsAndUser(t *testing.T) {
	cfg := load(t, write(t, "user = \"ab123\"\n"))
	if cfg.User != "ab123" || cfg.Host != "vpn-ac.uni-heidelberg.de" || cfg.SocksPort != 1080 ||
		cfg.HTTPPort != 1081 || cfg.IdleMinutes != 15 {
		t.Errorf("cfg = %+v", cfg)
	}
	if !slices.Equal(cfg.Backoff, []float64{5, 10, 20, 40, 80, 300}) {
		t.Errorf("backoff = %v", cfg.Backoff)
	}
}

func TestMissingUser(t *testing.T) {
	if err := loadErr(t, write(t, "host = \"x\"\n")); !strings.Contains(err.Error(), "user") {
		t.Errorf("err = %v", err)
	}
}

func TestErrorLines(t *testing.T) {
	tests := []struct{ name, text string }{
		{"unknown key", "user = \"a\"\nfoo = 1\n"},
		{"syntax error", "user = \"a\"\nport = \n"},
		{"wrong type", "user = \"a\"\nsocks_port = \"x\"\n"},
		{"unknown university", "user = \"a\"\nuniversity = \"nowhere\"\n"},
		// Bad profile values report their line.
		{"mfa", "user = \"a\"\nmfa = \"sms\"\n"},
		{"os", "user = \"a\"\nos = \"beos\"\n"},
		{"no_external_auth", "user = \"a\"\nno_external_auth = 1\n"},
		{"authgroup", "user = \"a\"\nauthgroup = \"a\\\"b\"\n"},
		{"host", "user = \"a\"\nhost = \"a b\"\n"},
	}
	for _, tt := range tests {
		if err := loadErr(t, write(t, tt.text)); err.Line != 2 {
			t.Errorf("%s: line = %d (%v)", tt.name, err.Line, err)
		}
	}
}

func TestMessages(t *testing.T) {
	tests := []struct{ text, want string }{
		{"user = \"a\"\nfoo = 1\n", "config.toml line 2: unknown key 'foo'"},
		{"user = \"a\"\nsocks_port = \"x\"\n", "config.toml line 2: 'socks_port' must be int"},
		{"user = \"a\"\nsocks_port = true\n", "config.toml line 2: 'socks_port' must be int"},
		{"user = \"a\"\nidle_minutes = false\n", "config.toml line 2: 'idle_minutes' must be int/float"},
		{"user = \"a\"\nmfa = 1\n", "config.toml line 2: 'mfa' must be str"},
		{"user = \"a\"\nuniversity = \"nowhere\"\n",
			"config.toml line 2: unknown university 'nowhere', see uni_vpn/universities.json"},
		{"user = \"a\"\nmfa = \"sms\"\n",
			"config.toml line 2: 'mfa' must be one of none, totp_field, totp_append, duo_push, saml"},
		{"user = \"a\"\nsocks_port = 0\n", "config.toml line 2: 'socks_port' must be between 1 and 65535"},
		{"user = \"a\"\nsocks_port = 5\nhttp_port = 5\n", "config.toml: 'socks_port' and 'http_port' must differ"},
		{"user = \"a\"\nidle_minutes = 0\n", "config.toml line 2: 'idle_minutes' must be greater than 0"},
		{"user = \"a\"\ntiming = 5\n", "config.toml line 2: 'timing' must be a table"},
		{"user = \"a\"\n[timing]\nfoo = 1\n", "config.toml line 3: unknown key 'foo'"},
		{"user = \"a\"\n[timing]\nbackoff = 5\n", "config.toml line 3: 'backoff' must be list"},
		{"user = \"a\"\n[timing]\nbackoff = [1, \"x\"]\n", "config.toml line 3: 'backoff' must be a list of numbers"},
		{"user = \"a\"\n[timing]\nbackoff = []\n", "config.toml line 3: 'backoff' must not be empty"},
		{"user = \"a\"\n[timing]\ntick = \"x\"\n", "config.toml line 3: 'tick' must be int/float"},
		{"university = \"other\"\nuser = \"a\"\n", `config.toml: 'host' is missing, it is needed with university = "other"`},
		{"user = \"\"\n", "config.toml: 'user' (university ID) is missing"},
		{"\ufeffuser = \"a\"\n", "config.toml line 1: Invalid statement (at line 1, column 1)"},
		// The first problem in document order wins.
		{"user = \"a\"\nfoo = 1\n[timing]\nbar = 2\n", "config.toml line 2: unknown key 'foo'"},
		{"user = \"a\"\n[timing]\nbar = 2\nbaz = 1\n", "config.toml line 3: unknown key 'bar'"},
	}
	for _, tt := range tests {
		if err := loadErr(t, write(t, tt.text)); err.Error() != tt.want {
			t.Errorf("%q: %q, want %q", tt.text, err.Error(), tt.want)
		}
	}
}

func TestTimingTable(t *testing.T) {
	cfg := load(t, write(t, "user = \"a\"\n[timing]\nready_timeout = 2.5\nbackoff = [0.1, 0.2]\n"))
	if cfg.ReadyTimeout != 2.5 || !slices.Equal(cfg.Backoff, []float64{0.1, 0.2}) {
		t.Errorf("cfg = %+v", cfg)
	}
	for _, text := range []string{"user = \"a\"\ntiming = {tick = 2}\n", "user = \"a\"\ntiming.tick = 2\n"} {
		if cfg := load(t, write(t, text)); cfg.Tick != 2 {
			t.Errorf("%q: tick = %v", text, cfg.Tick)
		}
	}
}

func TestSamePortsRejected(t *testing.T) {
	loadErr(t, write(t, "user = \"a\"\nsocks_port = 5\nhttp_port = 5\n"))
}

func TestMissingFile(t *testing.T) {
	if err := loadErr(t, filepath.Join(t.TempDir(), "nope.toml")); !strings.Contains(err.Error(), "missing") {
		t.Errorf("err = %v", err)
	}
}

func TestWriteInitialRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := WriteInitial(path, "ab123", unis.DefaultID, nil); err != nil {
		t.Fatal(err)
	}
	cfg := load(t, path)
	if cfg.User != "ab123" || cfg.Path != path {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestWithoutUniversityItIsHeidelberg(t *testing.T) {
	// Every config.toml written before profiles existed looks like this.
	cfg := load(t, write(t, "host = \"vpn-ac.uni-heidelberg.de\"\nuser = \"ab123\"\n"))
	if cfg.University != "heidelberg" || cfg.MFA != "totp_field" || !cfg.NeedsTOTP() || cfg.NoExternalAuth ||
		cfg.Useragent != "AnyConnect Linux_64 5.1.18.314" || cfg.MFAPortalURL != "https://mfa.uni-heidelberg.de/" ||
		cfg.LoginName() != "ab123" {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestDefaultConfigEqualsTheHeidelbergProfile(t *testing.T) {
	reference := Default()
	hd, _ := unis.Get("heidelberg")
	ApplyProfile(&reference, hd)
	if !reflect.DeepEqual(Default(), reference) {
		t.Errorf("Default() = %+v", Default())
	}
}

func TestProfileValuesApply(t *testing.T) {
	cfg := load(t, write(t, "university = \"ethz\"\nuser = \"jdoe\"\n"))
	if cfg.Host != "sslvpn.ethz.ch" || cfg.Authgroup != "staff-net" || cfg.Useragent != "AnyConnect" ||
		!cfg.NoExternalAuth || cfg.LoginName() != "jdoe@staff-net.ethz.ch" || cfg.UniversityName != "ETH Zurich" ||
		len(cfg.DefaultDomains) != 0 {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestOverridesWinWhereverTheyStand(t *testing.T) {
	cfg := load(t, write(t, "user = \"jdoe\"\nauthgroup = \"student-net\"\nuniversity = \"ethz\"\n"+
		"username_suffix = \"@student-net.ethz.ch\"\nno_external_auth = false\n"))
	if cfg.Authgroup != "student-net" || cfg.LoginName() != "jdoe@student-net.ethz.ch" || cfg.NoExternalAuth {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestATypedRealmReplacesTheSuffix(t *testing.T) {
	cfg := load(t, write(t, "university = \"stuttgart\"\nuser = \"st123456@stud.uni-stuttgart.de\"\n"))
	if cfg.LoginName() != "st123456@stud.uni-stuttgart.de" || cfg.NeedsTOTP() {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestUnknownUniversityReportsItsLine(t *testing.T) {
	err := loadErr(t, write(t, "user = \"a\"\nuniversity = \"nowhere\"\n"))
	if err.Line != 2 || !strings.Contains(err.Error(), "nowhere") {
		t.Errorf("err = %v", err)
	}
}

func TestHandWrittenHostsKeepWorking(t *testing.T) {
	cfg := load(t, write(t, "user = \"a\"\nhost = \"https://vpn-ac.uni-heidelberg.de/\"\n"))
	if cfg.Host != "https://vpn-ac.uni-heidelberg.de/" {
		t.Errorf("host = %q", cfg.Host)
	}
}

func TestOtherNeedsAHost(t *testing.T) {
	if err := loadErr(t, write(t, "university = \"other\"\nuser = \"a\"\n")); !strings.Contains(err.Error(), "host") {
		t.Errorf("err = %v", err)
	}
	cfg := load(t, write(t, "university = \"other\"\nuser = \"a\"\nhost = \"vpn.example.edu\"\n"))
	if cfg.MFA != "none" || !cfg.NoExternalAuth {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestValidUser(t *testing.T) {
	if !ValidUser("st123456@stud.uni-stuttgart.de") {
		t.Error("user with a realm refused")
	}
	for _, user := range []string{"a@", "@b", "a@b@c", `a"b`, "a b", "a@b c", ""} {
		if ValidUser(user) {
			t.Errorf("ValidUser(%q)", user)
		}
	}
}

func TestWriteInitialWritesUniversityAndOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	err := WriteInitial(path, "ab123", "other", []Setting{{"host", "VPN.Example.edu"},
		{"authgroup", "Staff (Split)"}, {"mfa", "none"}, {"no_external_auth", false}})
	if err != nil {
		t.Fatal(err)
	}
	want := `# uni-vpn configuration
university = "other"  # profile from uni_vpn/universities.json, "other" if not listed
user = "ab123"          # university ID
host = "vpn.example.edu"
authgroup = "Staff (Split)"
mfa = "none"
no_external_auth = false
idle_minutes = 15        # tear the tunnel down after this many minutes without traffic
socks_port = 1080        # SOCKS5 proxy for the browser
http_port = 1081         # status page http://127.0.0.1:1081
# openconnect = "/usr/sbin/openconnect"
# ocproxy = "/usr/bin/ocproxy"
`
	if got := strings.ReplaceAll(read(t, path), "\r\n", "\n"); got != want {
		t.Errorf("file =\n%s", got)
	}
	cfg := load(t, path)
	if cfg.Host != "vpn.example.edu" || cfg.Authgroup != "Staff (Split)" || cfg.MFA != "none" || cfg.NoExternalAuth {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestWriteInitialForAListedUniversityWritesNoHost(t *testing.T) {
	// Without a host line a fix in universities.json reaches existing installs.
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := WriteInitial(path, "ab123", "bonn", nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(read(t, path), "host") {
		t.Error("host written")
	}
	if host := load(t, path).Host; host != "unibn-vpn.uni-bonn.de" {
		t.Errorf("host = %q", host)
	}
}

func TestWriteInitialRefusesBadProfiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	tests := []struct {
		user, university string
		overrides        []Setting
		want             string
	}{
		{"ab123", "nowhere", nil, "config.toml: Unknown university 'nowhere'"},
		{"ab123", "other", nil, "config.toml: Enter the VPN address, for example vpn.example.edu"},
		{"ab123", "other", []Setting{{"host", "vpn.example.edu"}, {"mfa", "sms"}},
			"config.toml: 'mfa' must be one of none, totp_field, totp_append, duo_push, saml"},
		{"a b", "other", nil, "config.toml: invalid university ID: 'a b'"},
	}
	for _, tt := range tests {
		err := WriteInitial(path, tt.user, tt.university, tt.overrides)
		var ce *ConfigError
		if !errors.As(err, &ce) || ce.Error() != tt.want {
			t.Errorf("WriteInitial(%q, %v) = %v, want %q", tt.university, tt.overrides, err, tt.want)
		}
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("file written")
	}
}

func TestProfileConfigNamesTheField(t *testing.T) {
	tests := []struct {
		university string
		overrides  []Setting
		field      string
	}{
		{"other", []Setting{{"host", "vpn.example.edu"}, {"authgroup", "a\nb"}}, "authgroup"},
		{"nowhere", nil, "university"},
	}
	for _, tt := range tests {
		_, err := ProfileConfig(tt.university, tt.overrides)
		var fe *unis.FieldError
		if !errors.As(err, &fe) || fe.Field != tt.field {
			t.Errorf("ProfileConfig(%q) = %v, want field %s", tt.university, err, tt.field)
		}
	}
}

func TestSetValuesSetsSeveralKeysAndKeepsTheRest(t *testing.T) {
	path := write(t, "# mine\nuser = \"ab1\"  # comment\nno_external_auth = true\n[timing]\ntick = 5\n")
	err := SetValues(path, []Setting{{"user", "cd2"}, {"university", "bonn"}, {"no_external_auth", false}})
	if err != nil {
		t.Fatal(err)
	}
	text := read(t, path)
	if !strings.Contains(text, "# mine") || !strings.Contains(text, "# comment") {
		t.Errorf("comments lost:\n%s", text)
	}
	if want := "# mine\nuser = \"cd2\"  # comment\nno_external_auth = false\nuniversity = \"bonn\"\n\n[timing]\ntick = 5\n"; strings.ReplaceAll(text, "\r\n", "\n") != want {
		t.Errorf("file =\n%q", text)
	}
	cfg := load(t, path)
	if cfg.User != "cd2" || cfg.University != "bonn" || cfg.NoExternalAuth || cfg.Tick != 5 {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestSetUserRefusesUnknownFormsInsteadOfBreakingTheFile(t *testing.T) {
	for _, line := range []string{`user = """ab1"""`, `"user" = "ab1"`} {
		original := line + "\nhttp_port = 1081\n"
		path := write(t, original)
		var ce *ConfigError
		if err := SetUser(path, "zz9"); !errors.As(err, &ce) {
			t.Errorf("%s: SetUser = %v", line, err)
		}
		if read(t, path) != original {
			t.Errorf("%s: file changed", line)
		}
	}
}

func TestSetUserReplacesDoubleAndSingleQuotedValues(t *testing.T) {
	for _, line := range []string{`user = "ab123"`, `user = 'ab123'`, `user='ab123'  # mine`} {
		path := write(t, "host = \"x\"\n"+line+"\nhttp_port = 1081\n[timing]\ntick = 5\n")
		if err := SetUser(path, "cd456"); err != nil {
			t.Fatal(err)
		}
		if text := read(t, path); strings.Count(text, "user") != 1 {
			t.Errorf("%s:\n%s", line, text)
		}
		cfg := load(t, path)
		if cfg.User != "cd456" || cfg.HTTPPort != 1081 {
			t.Errorf("%s: cfg = %+v", line, cfg)
		}
	}
}

func TestSetUserAddsTheKeyWhenMissing(t *testing.T) {
	path := write(t, "host = \"x\"\n[timing]\ntick = 5\n")
	if err := SetUser(path, "cd456"); err != nil {
		t.Fatal(err)
	}
	cfg := load(t, path)
	if cfg.User != "cd456" || cfg.Tick != 5 {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestPortsFromBroken(t *testing.T) {
	tests := []struct {
		text string
		want map[string]int
	}{
		{"socks_port = 2000  # mine\nhttp_port = 2001\nbroken =\n", map[string]int{"socks_port": 2000, "http_port": 2001}},
		{"socks_port = 99999\nhttp_port = 2001\n", map[string]int{"http_port": 2001}},
		{"socks_port = 5\nhttp_port = 5\n", map[string]int{}},
		{"[timing]\nsocks_port = 2000\n", map[string]int{}},
	}
	for _, tt := range tests {
		if got := PortsFromBroken(write(t, tt.text)); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%q: %v, want %v", tt.text, got, tt.want)
		}
	}
	if got := PortsFromBroken(filepath.Join(t.TempDir(), "nope.toml")); len(got) != 0 {
		t.Errorf("missing file: %v", got)
	}
}

func TestTOMLValue(t *testing.T) {
	tests := []struct {
		in   any
		want string
	}{
		{true, "true"}, {false, "false"}, {`a"b\c`, `"a\"b\\c"`}, {"ä\x01\n", "\"ä\\u0001\\n\""}, {"<&>", `"<&>"`},
	}
	for _, tt := range tests {
		if got := TOMLValue(tt.in); got != tt.want {
			t.Errorf("TOMLValue(%q) = %s, want %s", tt.in, got, tt.want)
		}
	}
}

func TestSplitLinesAndLineOfKey(t *testing.T) {
	if got := splitLines("a\nb\r\nc\x0cd\n"); !slices.Equal(got, []string{"a", "b", "c", "d"}) {
		t.Errorf("splitLines = %q", got)
	}
	if got := lineOfKey("a = 1\n[None]\nfoo = 1\n", "foo", ""); got != 3 {
		t.Errorf("[None] counts as the top level in Python: %d", got)
	}
	if got := lineOfKey("[x]\nfoo = 1\n", "foo", ""); got != 0 {
		t.Errorf("foo in [x] = %d", got)
	}
}

func TestBackoffRejectsBooleans(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("user = \"ab1\"\n[timing]\nbackoff = [true, 5]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || err.Error() != "config.toml line 3: 'backoff' must be a list of numbers" {
		t.Fatalf("got %v", err)
	}
}
