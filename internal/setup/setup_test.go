package setup

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DavidVinu/uni-vpn/internal/config"
	"github.com/DavidVinu/uni-vpn/internal/credentials"
	"github.com/DavidVinu/uni-vpn/internal/desktop"
	"github.com/DavidVinu/uni-vpn/internal/detect"
	"github.com/DavidVinu/uni-vpn/internal/platform"
	"github.com/DavidVinu/uni-vpn/internal/service"
)

// The tests are a port of tests/test_setup.py: a temporary HOME, a fake service, keyring and
// proxy, and the real browser entry of the app.

type fakeService struct {
	home      string
	installed []string
	err       error
	removed   int
	result    bool
	removeErr error
}

func (f *fakeService) UnitTargetPath() string {
	return filepath.Join(f.home, ".config", "systemd", "user", "uni-vpn.service")
}

func (f *fakeService) Install(dry bool) ([]string, error) {
	target := f.UnitTargetPath()
	if f.err != nil {
		return nil, f.err
	}
	if !dry {
		os.MkdirAll(filepath.Dir(target), 0o700)
		os.WriteFile(target, []byte("unit"), 0o600)
	}
	f.installed = append(f.installed, target)
	return []string{target}, nil
}

func (f *fakeService) Uninstall() (bool, error) {
	f.removed++
	return f.result, f.removeErr
}

type harness struct {
	t           *testing.T
	home        string
	s           *Setup
	out         *bytes.Buffer
	svc         *fakeService
	stored      [][2]string
	storedTOTP  [][2]string
	deleted     []string
	keyring     map[credentials.Kind]string
	proxyCalls  []int
	proxyResult string
	prompts     []string
	asked       []string            // secret prompts
	answers     map[string][]string // prompt start -> answers, used in order
	secrets     func(prompt string) string
	urls        []string
	opened      bool
	commands    [][]string
}

func newHarness(t *testing.T) *harness {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("PATH", "/usr/bin:"+filepath.Join(home, ".local", "bin"))
	h := &harness{t: t, home: home, out: &bytes.Buffer{}, svc: &fakeService{home: home, result: true},
		keyring:     map[credentials.Kind]string{credentials.KindPassword: "missing", credentials.KindTOTP: "missing"},
		proxyResult: "ok", answers: map[string][]string{}}
	h.secrets = func(prompt string) string {
		if strings.Contains(prompt, "password") {
			return "pw"
		}
		return "gezd gnbv gy3t qojq gezd gnbv gy3t qojq"
	}
	run := func(c service.Cmd) (service.Result, error) {
		h.commands = append(h.commands, c.Args)
		return service.Result{}, nil
	}
	root := filepath.Join(home, "uni-vpn")
	// The app: never started by a test; without WebKitGTK the browser entry stands in for it.
	app := &desktop.Desktop{GOOS: "linux", Root: root, Run: run, Out: h.out, Getenv: os.Getenv,
		Home: func() string { return home }, HasDesktop: func() bool { return false },
		Spawn: func(args []string) bool { t.Fatal("spawned", args); return false }}
	h.s = &Setup{
		GOOS: "linux", Root: root, Binary: filepath.Join(root, "bin", "uni-vpn-core"), Out: h.out,
		Getenv: os.Getenv, Home: func() string { return home }, IsTerminal: func() bool { return false },
		Input: func(prompt string) (string, error) {
			h.prompts = append(h.prompts, prompt)
			for start, values := range h.answers {
				if strings.HasPrefix(prompt, start) && len(values) > 0 {
					h.answers[start] = values[1:]
					return values[0], nil
				}
			}
			if strings.HasPrefix(prompt, "University ID") {
				return "ab123", nil
			}
			if strings.HasPrefix(prompt, "Delete the password") {
				return "n", nil
			}
			return "", nil
		},
		Getpass: func(prompt string) (string, error) {
			h.asked = append(h.asked, prompt)
			return h.secrets(prompt), nil
		},
		FindBinary: func(name, override string) string { return "/usr/bin/" + name },
		Service:    h.svc, Run: run, UID: 501,
		ProxyInstall: func(port int) (string, error) {
			h.proxyCalls = append(h.proxyCalls, port)
			return h.proxyResult, nil
		},
		ProxyUninstall: func() (string, error) { return "restored", nil },
		StorePassword: func(user, pw string) error {
			h.stored = append(h.stored, [2]string{user, pw})
			return nil
		},
		StoreTOTP: func(user, token string) error {
			h.storedTOTP = append(h.storedTOTP, [2]string{user, token})
			return nil
		},
		DeletePassword: func(user string) (bool, error) { h.deleted = append(h.deleted, user); return true, nil },
		DeleteTOTP:     func(user string) (bool, error) { h.deleted = append(h.deleted, "totp:"+user); return true, nil },
		KeyringState:   func(user string, kind credentials.Kind) string { return h.keyring[kind] },
		PortOpen:       func(int) bool { return true },
		Sleep:          func(time.Duration) {},
		Now:            time.Now,
		OpenURL: func(link string) bool {
			h.urls = append(h.urls, link)
			return h.opened
		},
		HasDesktop: func() bool { return false },
		Probe: func(host, usergroup, group string) (detect.Detection, error) {
			t.Fatal("no probe expected")
			return detect.Detection{}, nil
		},
		App:            app,
		IsAdmin:        func() bool { return true },
		AddUserPath:    func(string) (bool, error) { return false, nil },
		RemoveUserPath: func(string) error { return nil },
		CiscoInstalled: func() bool { return false },
		Writable:       func(string) bool { return true },
	}
	return h
}

func (h *harness) setup(a Args) int {
	h.t.Helper()
	h.out.Reset()
	code, err := h.s.Setup(a)
	if err != nil {
		h.t.Fatal(err, h.out.String())
	}
	return code
}

func (h *harness) uninstall(a Args) int {
	h.t.Helper()
	h.out.Reset()
	code, err := h.s.Uninstall(a)
	if err != nil {
		h.t.Fatal(err, h.out.String())
	}
	return code
}

func (h *harness) cfgPath() string { return filepath.Join(h.home, ".config", "uni-vpn", "config.toml") }

func (h *harness) wantOut(t *testing.T, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(h.out.String(), w) {
			t.Fatalf("missing %q in:\n%s", w, h.out.String())
		}
	}
}

func (h *harness) notOut(t *testing.T, unwanted ...string) {
	t.Helper()
	for _, w := range unwanted {
		if strings.Contains(h.out.String(), w) {
			t.Fatalf("unexpected %q in:\n%s", w, h.out.String())
		}
	}
}

func writeConfig(t *testing.T, path, text string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o700)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

const heidelbergToken = "base32:GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"

func TestSetupCreatesEverything(t *testing.T) {
	posixOnly(t)
	h := newHarness(t)
	if code := h.setup(Args{}); code != 0 {
		t.Fatal(code, h.out.String())
	}
	data, _ := os.ReadFile(h.cfgPath())
	if !strings.Contains(string(data), `user = "ab123"`) {
		t.Fatal(string(data))
	}
	link := filepath.Join(h.home, ".local", "bin", "uni-vpn")
	wrapper, _ := os.ReadFile(link)
	if string(wrapper) != "#!/bin/sh\nexec \""+h.s.Binary+"\" \"$@\"\n" {
		t.Fatal(string(wrapper))
	}
	if st, err := os.Stat(link); err != nil || st.Mode().Perm()&0o111 == 0 {
		t.Fatal("wrapper not executable")
	}
	if !reflect.DeepEqual(h.stored, [][2]string{{"ab123", "pw"}}) ||
		!reflect.DeepEqual(h.storedTOTP, [][2]string{{"ab123", heidelbergToken}}) {
		t.Fatal(h.stored, h.storedTOTP)
	}
	h.wantOut(t, "Check code", "Proxy rule registered with the system", "http://127.0.0.1:1081/")
	recorded := Recorded()
	for _, want := range []string{h.cfgPath(), link, h.svc.installed[0], h.s.ApportIgnorePath(),
		filepath.Join(h.home, ".local", "share", "applications", "uni-vpn.desktop")} {
		if !slices.Contains(recorded, want) {
			t.Fatal(want, recorded)
		}
	}
	apport, _ := os.ReadFile(h.s.ApportIgnorePath())
	if !strings.Contains(string(apport), `program="/usr/bin/openconnect"`) {
		t.Fatal(string(apport))
	}
	if !reflect.DeepEqual(h.proxyCalls, []int{1081}) {
		t.Fatal(h.proxyCalls)
	}
}

func TestInvalidConfigDoesNotStopAnUpdate(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		port       int
	}{
		{"bad socks port", "user = \"ab123\"\nsocks_port = \"x\"\n", 1081},
		{"keeps the daemon's ports", "user = \"ab123\"\nhttp_port = 1091\nsocks_port = 1090\nidle_minutes = \"x\"\n", 1091},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			writeConfig(t, h.cfgPath(), tc.text)
			if code := h.setup(Args{User: "cd456"}); code != 0 {
				t.Fatal(code, h.out.String())
			}
			h.wantOut(t, "invalid")
			if len(h.svc.installed) != 1 || !reflect.DeepEqual(h.proxyCalls, []int{tc.port}) || h.stored != nil {
				t.Fatal(h.svc.installed, h.proxyCalls, h.stored)
			}
			data, _ := os.ReadFile(h.cfgPath())
			if string(data) != tc.text {
				t.Fatal(string(data))
			}
		})
	}
}

func TestProxyResults(t *testing.T) {
	for _, tc := range []struct {
		result      string
		want, avoid []string
	}{
		{"ok", []string{"Proxy rule registered with the system (Chrome"}, nil},
		{"unavailable", []string{"http://127.0.0.1:1081/proxy.pac", "by hand"}, []string{"Proxy rule registered"}},
		{"replaced", []string{"replaced"}, nil},
		{"failed", []string{"http://127.0.0.1:1081/proxy.pac"}, []string{"Proxy rule registered"}},
	} {
		t.Run(tc.result, func(t *testing.T) {
			h := newHarness(t)
			h.proxyResult = tc.result
			if code := h.setup(Args{User: "ab123"}); code != 0 {
				t.Fatal(code)
			}
			h.wantOut(t, tc.want...)
			h.notOut(t, tc.avoid...)
		})
	}
}

func TestUserOptionOnARerunChangesTheUniversityID(t *testing.T) {
	h := newHarness(t)
	h.setup(Args{User: "ab123"})
	if code := h.setup(Args{User: "cd456"}); code != 0 {
		t.Fatal(code, h.out.String())
	}
	cfg, err := config.Load(h.cfgPath())
	if err != nil || cfg.User != "cd456" {
		t.Fatal(cfg, err)
	}
}

func TestPathHintOnLinuxMentionsLoggingInAgain(t *testing.T) {
	h := newHarness(t)
	t.Setenv("PATH", "/usr/bin")
	h.setup(Args{User: "ab123"})
	h.wantOut(t, "logging out and in")
}

func TestMacOSCommandGoesToHomebrewBinAndUninstallRemovesIt(t *testing.T) {
	h := newHarness(t)
	brew := filepath.Join(h.home, "homebrew", "bin")
	os.MkdirAll(brew, 0o755)
	old := MacOSBinDirs
	MacOSBinDirs = []string{filepath.Join(h.home, "missing"), brew}
	t.Cleanup(func() { MacOSBinDirs = old })
	h.s.GOOS = "darwin"
	if got := h.s.CommandPath(); got != filepath.Join(brew, "uni-vpn") {
		t.Fatal(got)
	}
	h.s.InstallCommand(false, func(path string) { Record([]string{path}) })
	if !slices.Contains(Recorded(), filepath.Join(brew, "uni-vpn")) {
		t.Fatal(Recorded())
	}
	h.s.GOOS = "linux"
	h.uninstall(Args{})
	if _, err := os.Stat(filepath.Join(brew, "uni-vpn")); err == nil {
		t.Fatal("command left behind")
	}
}

func TestWrappers(t *testing.T) {
	for _, tc := range []struct {
		goos, binary, want string
	}{
		{"linux", "/home/x/uni-vpn/bin/uni-vpn-core", "#!/bin/sh\nexec \"/home/x/uni-vpn/bin/uni-vpn-core\" \"$@\"\n"},
		{"darwin", "/Users/x/a$b`c\"d/bin/uni-vpn-core", "#!/bin/sh\nexec \"/Users/x/a\\$b\\`c\\\"d/bin/uni-vpn-core\" \"$@\"\n"},
		{"windows", `C:\Program Files\uni-vpn\bin\uni-vpn-core.exe`, "@echo off\r\n\"C:\\Program Files\\uni-vpn\\bin\\uni-vpn-core.exe\" %*\r\n"},
		{"windows", `C:\Users\Jürgen\uni-vpn-core.exe`, "@chcp 65001 >nul\r\n@echo off\r\n\"C:\\Users\\Jürgen\\uni-vpn-core.exe\" %*\r\n"},
	} {
		s := &Setup{GOOS: tc.goos, Binary: tc.binary}
		if got := string(s.Wrapper()); got != tc.want {
			t.Errorf("%s: %q", tc.binary, got)
		}
	}
}

func TestNoTerminalIsAnError(t *testing.T) {
	h := newHarness(t)
	h.s.Input = func(string) (string, error) { return "", ErrNoTerminal }
	if _, err := h.s.Setup(Args{}); !errors.Is(err, ErrNoTerminal) {
		t.Fatal(err)
	}
}

func TestSetupIsIdempotent(t *testing.T) {
	h := newHarness(t)
	h.setup(Args{})
	if code := h.setup(Args{}); code != 0 {
		t.Fatal(code)
	}
	recorded := Recorded()
	if len(recorded) != len(slices.Compact(slices.Sorted(slices.Values(recorded)))) {
		t.Fatal(recorded)
	}
}

func TestDryRunChangesNothing(t *testing.T) {
	h := newHarness(t)
	if code := h.setup(Args{DryRun: true, User: "ab123"}); code != 0 {
		t.Fatal(code)
	}
	if _, err := os.Stat(filepath.Join(h.home, ".config", "uni-vpn")); err == nil {
		t.Fatal("config dir created")
	}
	if h.stored != nil || h.storedTOTP != nil || h.proxyCalls != nil || h.prompts != nil || h.asked != nil {
		t.Fatal(h.stored, h.storedTOTP, h.proxyCalls, h.prompts)
	}
	h.wantOut(t, "would", "TOTP", "proxy rule", "Heidelberg University", "as a wrapper for "+h.s.Binary)
	entries, _ := os.ReadDir(h.home)
	if len(entries) != 0 {
		t.Fatal(entries)
	}
}

func TestSecrets(t *testing.T) {
	for _, tc := range []struct {
		name     string
		password string // keyring state
		totp     string // what is typed for the TOTP secret
		prompts  int
		stored   int
		tokens   int
		want     []string
	}{
		{"totp asked alone when the password is there", "present", "gezd gnbv gy3t qojq gezd gnbv gy3t qojq", 1, 0, 1, nil},
		{"invalid totp is reported", "missing", "0189", 2, 1, 0, []string{"That is not the secret", "in the app, under Settings"}},
		{"empty totp points to the app", "missing", "", 2, 1, 0, []string{"in the app, under Settings"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.keyring[credentials.KindPassword] = tc.password
			h.secrets = func(prompt string) string {
				if strings.Contains(prompt, "password") {
					return "pw"
				}
				return tc.totp
			}
			if code := h.setup(Args{User: "ab123"}); code != 0 {
				t.Fatal(code, h.out.String())
			}
			if len(h.asked) != tc.prompts || len(h.stored) != tc.stored || len(h.storedTOTP) != tc.tokens {
				t.Fatal(h.asked, h.stored, h.storedTOTP)
			}
			h.wantOut(t, tc.want...)
		})
	}
}

func TestTOTPHintNamesThePortalBeforeAsking(t *testing.T) {
	h := newHarness(t)
	h.setup(Args{User: "ab123"})
	out := h.out.String()
	if i := strings.Index(out, "mfa.uni-heidelberg.de"); i < 0 || i > strings.Index(out, "Check code") {
		t.Fatal(out)
	}
}

func TestExistingSecretsAreNotAskedAgain(t *testing.T) {
	h := newHarness(t)
	h.keyring[credentials.KindPassword], h.keyring[credentials.KindTOTP] = "present", "present"
	h.setup(Args{User: "ab123"})
	if len(h.asked) != 0 {
		t.Fatal(h.asked)
	}
	h.wantOut(t, "already in the keyring")
}

func TestMissingPrograms(t *testing.T) {
	for _, dry := range []bool{true, false} {
		h := newHarness(t)
		h.s.FindBinary = func(string, string) string { return "" }
		code := h.setup(Args{DryRun: dry, User: "ab123"})
		if dry && (code != 0 || !strings.Contains(h.out.String(), "would require")) {
			t.Fatal(code, h.out.String())
		}
		if !dry && (code != 1 || !strings.Contains(h.out.String(), "Missing: openconnect")) {
			t.Fatal(code, h.out.String())
		}
	}
}

func TestServiceFailureReportsAndKeepsRecords(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  *service.Error
	}{
		{"with files", &service.Error{Msg: "Failed to connect to bus: No medium found"}},
		{"without files", &service.Error{Msg: "Bootstrap failed: 5: Input/output error"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			unit := h.svc.UnitTargetPath()
			if tc.name == "with files" {
				tc.err.Files = []string{unit}
			}
			h.svc.err = tc.err
			if code := h.setup(Args{}); code != 1 {
				t.Fatal(code)
			}
			h.wantOut(t, "Service could not be loaded: "+tc.err.Msg)
			recorded := Recorded()
			for _, want := range []string{h.cfgPath(), filepath.Join(h.home, ".local", "bin", "uni-vpn"), h.s.ApportIgnorePath()} {
				if !slices.Contains(recorded, want) {
					t.Fatal(want, recorded)
				}
			}
			if slices.Contains(recorded, unit) != (tc.name == "with files") {
				t.Fatal(recorded)
			}
		})
	}
}

func TestWaitForPort(t *testing.T) {
	h := newHarness(t)
	answers := []bool{false, false, true}
	var seen []int
	var sleeps []time.Duration
	h.s.PortOpen = func(port int) bool {
		seen = append(seen, port)
		ok := answers[0]
		answers = answers[1:]
		return ok
	}
	h.s.Sleep = func(d time.Duration) { sleeps = append(sleeps, d) }
	if !h.s.WaitForPort(1081, 5*time.Second) || len(seen) != 3 || len(sleeps) != 2 || sleeps[0] != 250*time.Millisecond {
		t.Fatal(seen, sleeps)
	}
	// Start 0, deadline 5 s: sleeps after 1, 2 and 4 s, gives up at 5.5 s.
	clock := []float64{0, 1, 2, 4, 5.5}
	base := time.Now()
	h.s.Now = func() time.Time {
		now := clock[0]
		clock = clock[1:]
		return base.Add(time.Duration(now * float64(time.Second)))
	}
	h.s.PortOpen = func(int) bool { return false }
	sleeps = nil
	if h.s.WaitForPort(1081, 5*time.Second) || len(sleeps) != 3 {
		t.Fatal(sleeps)
	}
}

// --- with a desktop: the setup assistant ---

func TestGUISetup(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   Args
		opened bool
		urls   []string
		want   string
	}{
		{"asks nothing and opens the assistant", Args{}, true, []string{"http://127.0.0.1:1081/"}, "Finish in the browser window"},
		{"prefills the ID", Args{User: "ab123"}, true, []string{"http://127.0.0.1:1081/?user=ab123"}, ""},
		{"passes the university", Args{User: "ab123", University: "ethz"}, true,
			[]string{"http://127.0.0.1:1081/?user=ab123&university=ethz"}, ""},
		{"says where to go without a browser", Args{}, false, []string{"http://127.0.0.1:1081/"},
			"Open http://127.0.0.1:1081/ in a browser"},
		{"no browser for the installer", Args{NoBrowser: true}, true, nil, "Finish in the browser (http://127.0.0.1:1081/)."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.s.HasDesktop = func() bool { return true }
			h.opened = tc.opened
			h.s.Input = func(p string) (string, error) { t.Fatal("asked", p); return "", nil }
			h.s.Getpass = h.s.Input
			if code := h.setup(tc.args); code != 0 {
				t.Fatal(code, h.out.String())
			}
			if !reflect.DeepEqual(h.urls, tc.urls) {
				t.Fatal(h.urls)
			}
			h.wantOut(t, tc.want)
			if _, err := os.Stat(h.cfgPath()); err == nil {
				t.Fatal("config written")
			}
			if !slices.Contains(Recorded(), h.cfgPath()) || h.stored != nil || !reflect.DeepEqual(h.proxyCalls, []int{1081}) {
				t.Fatal(Recorded(), h.stored, h.proxyCalls)
			}
			entry, _ := os.ReadFile(filepath.Join(h.home, ".local", "share", "applications", "uni-vpn.desktop"))
			if !strings.Contains(string(entry), "Exec=xdg-open http://127.0.0.1:1081/") {
				t.Fatal(string(entry))
			}
		})
	}
}

func TestNoGUIFlagAsksInTheTerminal(t *testing.T) {
	h := newHarness(t)
	h.s.HasDesktop = func() bool { return true }
	if code := h.setup(Args{NoGUI: true}); code != 0 || !reflect.DeepEqual(h.stored, [][2]string{{"ab123", "pw"}}) {
		t.Fatal(code, h.stored)
	}
}

func TestServiceThatNeverAnswersIsReportedInsteadOfOpeningTheBrowser(t *testing.T) {
	h := newHarness(t)
	h.s.HasDesktop = func() bool { return true }
	h.s.PortOpen = func(int) bool { return false }
	start := time.Now()
	h.s.Now = func() time.Time { start = start.Add(time.Second); return start }
	if code := h.setup(Args{}); code != 1 || h.urls != nil {
		t.Fatal(code, h.urls)
	}
	h.wantOut(t, "Restart the computer")
}

func TestInvalidUniversityIDIsRefused(t *testing.T) {
	h := newHarness(t)
	if code := h.setup(Args{User: `ab"1`}); code != 1 {
		t.Fatal(code)
	}
	h.wantOut(t, "Not a university ID")
}

// --- the university question ---

func fixture(t *testing.T, name, host, usergroup string) detect.Detection {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "tests", "fixtures", "detect", name))
	if err != nil {
		t.Fatal(err)
	}
	return detect.ParseReply(data, host, usergroup)
}

func TestUniversityChoice(t *testing.T) {
	type probeCall struct{ host, usergroup, group string }
	for _, tc := range []struct {
		name    string
		args    Args
		answers map[string][]string
		probe   func(t *testing.T, host, usergroup, group string) detect.Detection
		code    int
		want    []string
		check   func(t *testing.T, h *harness, cfg *config.Config, calls []probeCall)
	}{
		{name: "enter keeps heidelberg", want: []string{"mfa.uni-heidelberg.de"},
			check: func(t *testing.T, h *harness, cfg *config.Config, _ []probeCall) {
				if cfg.University != "heidelberg" || len(h.storedTOTP) != 1 {
					t.Fatal(cfg.University, h.storedTOTP)
				}
			}},
		{name: "a name picks the profile and skips the totp question", answers: map[string][]string{"Your university": {"bonn"}},
			want: []string{"University of Bonn"},
			check: func(t *testing.T, h *harness, cfg *config.Config, _ []probeCall) {
				if cfg.University != "bonn" || len(h.stored) != 1 || h.storedTOTP != nil || strings.Contains(h.out.String(), "TOTP") {
					t.Fatal(cfg.University, h.stored, h.storedTOTP)
				}
			}},
		{name: "an ambiguous name asks again", answers: map[string][]string{"Your university": {"universit", "mannheim"}},
			want: []string{"Which one?"},
			check: func(t *testing.T, h *harness, cfg *config.Config, _ []probeCall) {
				if cfg.University != "mannheim" {
					t.Fatal(cfg.University)
				}
			}},
		{name: "three misses stop without a config", answers: map[string][]string{"Your university": {"atlantis", "atlantis", "atlantis"}},
			code: 1, want: []string{"Type other"}},
		{name: "saml university is refused", answers: map[string][]string{"Your university": {"oxford"}}, code: 1,
			want: []string{"web page"}},
		{name: "other probes the gateway and writes the profile",
			answers: map[string][]string{"Your university": {"other"}, "VPN address": {"https://vpn.example.edu/staff"}},
			probe: func(t *testing.T, host, usergroup, group string) detect.Detection {
				return fixture(t, "bremen.xml", host, usergroup)
			},
			check: func(t *testing.T, h *harness, cfg *config.Config, calls []probeCall) {
				if !reflect.DeepEqual(calls, []probeCall{{"vpn.example.edu", "staff", ""}}) {
					t.Fatal(calls)
				}
				got := []string{cfg.University, cfg.Host, cfg.Usergroup, cfg.Authgroup, cfg.MFA}
				if !reflect.DeepEqual(got, []string{"other", "vpn.example.edu", "staff", "Tunnel-All-Traffic", "totp_field"}) ||
					len(h.storedTOTP) != 1 {
					t.Fatal(got, h.storedTOTP)
				}
			}},
		{name: "other with another group probes again",
			answers: map[string][]string{"Your university": {"other"}, "VPN address": {"su-vpn.stanford.edu"},
				"Group": {"Stanford"}, "Second factor": {"duo_push"}},
			probe: func(t *testing.T, host, usergroup, group string) detect.Detection {
				if group != "" {
					return fixture(t, "stanford-group-stanford.xml", host, "")
				}
				return fixture(t, "stanford.xml", host, "")
			},
			check: func(t *testing.T, h *harness, cfg *config.Config, calls []probeCall) {
				if cfg.Authgroup != "Stanford" || cfg.MFA != "duo_push" || h.storedTOTP != nil || len(calls) != 2 {
					t.Fatal(cfg.Authgroup, cfg.MFA, h.storedTOTP, calls)
				}
			}},
		{name: "other with saml stops", code: 1, want: []string{"web page"},
			answers: map[string][]string{"Your university": {"other"}, "VPN address": {"vpn.fu-berlin.de"}},
			probe: func(t *testing.T, host, usergroup, group string) detect.Detection {
				return fixture(t, "fu-berlin.xml", host, "")
			}},
		{name: "the university option skips the question", args: Args{University: "stanford"},
			check: func(t *testing.T, h *harness, cfg *config.Config, _ []probeCall) {
				if slices.Contains(h.prompts, UniversityPrompt) || cfg.University != "stanford" || h.storedTOTP != nil {
					t.Fatal(h.prompts, cfg.University)
				}
			}},
		{name: "unknown university option", args: Args{University: "atlantis"}, code: 1},
		{name: "other as university option", args: Args{University: "other"}, code: 1},
		{name: "saml university option", args: Args{University: "fu-berlin"}, code: 1},
		{name: "dry run asks no university", args: Args{DryRun: true, User: "ab123"}, want: []string{"Heidelberg University"},
			check: func(t *testing.T, h *harness, _ *config.Config, _ []probeCall) {
				if len(h.prompts) != 0 {
					t.Fatal(h.prompts)
				}
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.answers = tc.answers
			if h.answers == nil {
				h.answers = map[string][]string{}
			}
			var calls []probeCall
			if tc.probe != nil {
				h.s.Probe = func(host, usergroup, group string) (detect.Detection, error) {
					calls = append(calls, probeCall{host, usergroup, group})
					return tc.probe(t, host, usergroup, group), nil
				}
			}
			if code := h.setup(tc.args); code != tc.code {
				t.Fatal(code, h.out.String())
			}
			h.wantOut(t, tc.want...)
			cfg, err := config.Load(h.cfgPath())
			if tc.code != 0 || tc.args.DryRun {
				if err == nil {
					t.Fatal("config written")
				}
				cfg = nil
			} else if err != nil {
				t.Fatal(err)
			}
			if tc.check != nil {
				tc.check(t, h, cfg, calls)
			}
		})
	}
}

func TestExistingConfigKeepsItsUniversity(t *testing.T) {
	h := newHarness(t)
	h.setup(Args{})
	if code := h.setup(Args{University: "bonn"}); code != 0 {
		t.Fatal(code)
	}
	h.wantOut(t, `set university = "bonn"`)
	if cfg, _ := config.Load(h.cfgPath()); cfg.University != "heidelberg" {
		t.Fatal(cfg.University)
	}
}

// --- uninstall ---

func TestUninstallRemovesRecordedFiles(t *testing.T) {
	h := newHarness(t)
	h.setup(Args{})
	if code := h.uninstall(Args{Yes: true}); code != 0 {
		t.Fatal(code, h.out.String())
	}
	if h.svc.removed != 1 || !reflect.DeepEqual(h.deleted, []string{"ab123", "totp:ab123"}) {
		t.Fatal(h.svc.removed, h.deleted)
	}
	h.wantOut(t, "Proxy setting restored", "Left in place")
	for _, path := range []string{h.cfgPath(), filepath.Join(h.home, ".local", "bin", "uni-vpn"), RecordsPath(),
		h.s.ApportIgnorePath()} {
		if _, err := os.Stat(path); err == nil {
			t.Fatal(path)
		}
	}
}

func TestUninstallDryRunListsTheFiles(t *testing.T) {
	h := newHarness(t)
	h.setup(Args{})
	if code := h.uninstall(Args{DryRun: true}); code != 0 || h.svc.removed != 0 {
		t.Fatal(code)
	}
	h.wantOut(t, "would remove", "   "+h.cfgPath())
	if _, err := os.Stat(h.cfgPath()); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsUninstallWithoutAdminRightsChangesNothing(t *testing.T) {
	h := newHarness(t)
	h.setup(Args{})
	h.s.GOOS = "windows"
	h.s.IsAdmin = func() bool { return false }
	h.s.ProxyUninstall = func() (string, error) { t.Fatal("proxy restored"); return "", nil }
	if code := h.uninstall(Args{Yes: true}); code != 1 || h.svc.removed != 0 {
		t.Fatal(code)
	}
	h.wantOut(t, "click Uninstall")
	if _, err := os.Stat(h.cfgPath()); err != nil {
		t.Fatal(err)
	}
}

func TestKeyringQuestion(t *testing.T) {
	for _, tc := range []struct {
		name    string
		input   func(string) (string, error)
		deleted []string
	}{
		{"declined", func(string) (string, error) { return "n", nil }, nil},
		{"no terminal means no", func(string) (string, error) { return "", ErrNoTerminal }, nil},
		{"ja", func(string) (string, error) { return " Ja ", nil }, []string{"ab123", "totp:ab123"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.setup(Args{})
			h.s.Input = tc.input
			if code := h.uninstall(Args{}); code != 0 || !reflect.DeepEqual(h.deleted, tc.deleted) {
				t.Fatal(code, h.deleted)
			}
		})
	}
}

func TestUninstallFailuresAreReportedNotHidden(t *testing.T) {
	h := newHarness(t)
	h.setup(Args{})
	h.svc.result = false
	h.s.ProxyUninstall = func() (string, error) { return "failed", nil }
	if code := h.uninstall(Args{}); code != 1 {
		t.Fatal(code)
	}
	h.wantOut(t, "Service could not be removed", "Proxy setting could not be restored")
	h.notOut(t, "Service removed", "Proxy setting restored")
}

func getShCopy(t *testing.T, h *harness, git bool) string {
	app := platform.AppInstallDir() // under h.home
	os.MkdirAll(filepath.Join(app, "bin"), 0o755)
	os.WriteFile(filepath.Join(app, "bin", "uni-vpn-core"), []byte(""), 0o755)
	if git {
		os.Mkdir(filepath.Join(app, ".git"), 0o755)
	}
	h.s.Root = app
	return app
}

func TestRemovesTheGetShCopy(t *testing.T) {
	posixOnly(t)
	h := newHarness(t)
	h.setup(Args{})
	app := getShCopy(t, h, false)
	if code := h.uninstall(Args{}); code != 0 {
		t.Fatal(code, h.out.String())
	}
	if _, err := os.Stat(filepath.Dir(app)); err == nil {
		t.Fatal("copy left behind")
	}
}

func TestKeepsAGitCloneAndTheCopyAfterAFailure(t *testing.T) {
	h := newHarness(t)
	h.setup(Args{})
	app := getShCopy(t, h, true)
	h.uninstall(Args{})
	if _, err := os.Stat(app); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(filepath.Join(app, ".git"))
	h.svc.result = false
	if code := h.uninstall(Args{}); code != 1 {
		t.Fatal(code)
	}
	if _, err := os.Stat(app); err != nil {
		t.Fatal(err)
	}
}

func TestApportIgnoreMergesIntoAnExistingFile(t *testing.T) {
	for _, tc := range []struct{ name, before, want string }{
		{"existing entries", `<?xml version="1.0"?>` + "\n" + `<apport><ignore program="/usr/bin/other" mtime="1"/></apport>` + "\n",
			`<?xml version="1.0"?>` + "\n" + `<apport><ignore program="/usr/bin/other" mtime="1"/><ignore program="/x/open&amp;connect" mtime="0" /></apport>` + "\n"},
		{"empty root", "<apport/>", `<?xml version="1.0"?>` + "\n" + `<apport><ignore program="/x/open&amp;connect" mtime="0" /></apport>` + "\n"},
		{"broken file", "<apport><ignore", `<?xml version="1.0"?>` + "\n" + `<apport><ignore program="/x/open&amp;connect" mtime="0" /></apport>` + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			path := h.s.ApportIgnorePath()
			os.WriteFile(path, []byte(tc.before), 0o600)
			for range 2 {
				if created, err := h.s.EnsureApportIgnore("/x/open&connect", false); err != nil || created != "" {
					t.Fatal(created, err)
				}
			}
			if data, _ := os.ReadFile(path); string(data) != tc.want {
				t.Fatal(string(data))
			}
		})
	}
	h := newHarness(t)
	if created, _ := h.s.EnsureApportIgnore("/bin/sh", false); created != h.s.ApportIgnorePath() {
		t.Fatal(created)
	}
}

// --- removal (uni_vpn/removal.py) ---

func TestRemoveKeepsTheKeyringAndEndsTheServiceLast(t *testing.T) {
	h := newHarness(t)
	h.setup(Args{})
	unit := h.svc.UnitTargetPath()
	h.commands = nil
	h.s.Input = func(string) (string, error) { t.Fatal("asked"); return "", nil }
	if code := h.s.Remove(); code != 0 {
		t.Fatal(code, h.out.String())
	}
	if h.deleted != nil || h.svc.removed != 0 {
		t.Fatal(h.deleted, h.svc.removed)
	}
	if _, err := os.Stat(unit); err == nil {
		t.Fatal("unit left behind")
	}
	stop := []string{"systemctl", "--user", "stop", "uni-vpn"}
	if !reflect.DeepEqual(h.commands[len(h.commands)-1], stop) || slices.ContainsFunc(h.commands[:len(h.commands)-1],
		func(c []string) bool { return reflect.DeepEqual(c, stop) }) {
		t.Fatal(h.commands)
	}
	if !slices.ContainsFunc(h.commands, func(c []string) bool {
		return reflect.DeepEqual(c, []string{"systemctl", "--user", "disable", "uni-vpn"})
	}) {
		t.Fatal(h.commands)
	}
}

// posixOnly: execute bits, and the get.sh copy that lives in Program Files on Windows.
func posixOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX only")
	}
}
