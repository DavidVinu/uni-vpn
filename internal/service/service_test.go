package service

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/DavidVinu/uni-vpn/internal/winsys"
)

// --- rendering ---

func TestTemplatesMatchRepositoryFiles(t *testing.T) {
	for path, want := range map[string]string{
		"../../systemd/uni-vpn.service.in":            SystemdTemplate,
		"../../launchd/de.davidvinu.uni-vpn.plist.in": LaunchdTemplate,
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		// git may check the files out with CRLF on Windows.
		if got := strings.ReplaceAll(string(data), "\r\n", "\n"); got != want {
			t.Fatalf("%s differs from the copy in render.go", path)
		}
	}
}

func TestRenderReplacesAll(t *testing.T) {
	got, err := Render("a @X@ b @Y@", []Pair{{"X", "1"}, {"Y", "2"}})
	if err != nil || got != "a 1 b 2" {
		t.Fatal(got, err)
	}
}

func TestRenderRejectsLeftover(t *testing.T) {
	_, err := Render("a @X@ @Z@", []Pair{{"X", "1"}})
	if err == nil || err.Error() != "Placeholders not replaced: @Z@" {
		t.Fatal(err)
	}
}

func mustRender(text string, err error) string {
	if err != nil {
		panic(err)
	}
	return text
}

func TestSystemdUnit(t *testing.T) {
	text := mustRender(RenderPythonUnit(false, "/usr/bin/python3", "/home/x/uni-vpn/bin/uni-vpn", "/home/x/.local/state/uni-vpn", "", nil))
	for _, want := range []string{`ExecStart="/usr/bin/python3" "/home/x/uni-vpn/bin/uni-vpn" daemon`,
		"WantedBy=graphical-session.target", "PartOf=graphical-session.target"} {
		if !strings.Contains(text, want) {
			t.Fatal(want)
		}
	}
	if strings.Contains(text, "@") || strings.Contains(text, "Environment=") {
		t.Fatal(text)
	}
}

func TestSystemdUnitQuotesPathWithSpace(t *testing.T) {
	text := mustRender(RenderPythonUnit(false, "/usr/bin/python3", "/home/x/Uni Stuff/uni-vpn/bin/uni-vpn", "/home/x/.local/state/uni-vpn", "", nil))
	if !strings.Contains(text, `ExecStart="/usr/bin/python3" "/home/x/Uni Stuff/uni-vpn/bin/uni-vpn" daemon`) {
		t.Fatal(text)
	}
}

func TestRenderUnitRejectsQuoteAndBackslash(t *testing.T) {
	for _, bad := range []string{`/home/x/a"b/uni-vpn`, `/home/x/a\b/uni-vpn`} {
		for _, macOS := range []bool{false, true} {
			if _, err := RenderPythonUnit(macOS, "/usr/bin/python3", bad, "/home/x/state", "", nil); err == nil {
				t.Fatal(bad)
			}
			if _, err := RenderUnit(macOS, bad, "/home/x/state", "", nil); err == nil {
				t.Fatal(bad)
			}
		}
	}
	_, err := RenderPythonUnit(false, "/usr/bin/python3", "/home/x/uni-vpn", "/home/x/state", "",
		[]Pair{{"XDG_CONFIG_HOME", `/home/x/"cfg`}})
	if err == nil || err.Error() != `XDG_CONFIG_HOME must not contain quotes, backslashes or line breaks: '/home/x/"cfg'` {
		t.Fatal(err)
	}
	_, err = RenderUnit(false, `/home/x/a\b/uni-vpn`, "/s", "", nil)
	if err == nil || err.Error() != `uni-vpn must not contain quotes, backslashes or line breaks: '/home/x/a\\b/uni-vpn'` {
		t.Fatal(err)
	}
}

func TestSystemdUnitExtraEnv(t *testing.T) {
	text := mustRender(RenderPythonUnit(false, "/usr/bin/python3", "/home/x/uni-vpn/bin/uni-vpn", "/home/x/.local/state/uni-vpn", "",
		[]Pair{{"XDG_CONFIG_HOME", "/home/x/.cfg"}, {"XDG_STATE_HOME", "/home/x/st ate"}}))
	if !strings.Contains(text, `Environment="XDG_CONFIG_HOME=/home/x/.cfg"`+"\n"+`Environment="XDG_STATE_HOME=/home/x/st ate"`+"\n") ||
		strings.Contains(text, "@") {
		t.Fatal(text)
	}
}

func TestLaunchdPlistEscapesXMLInPaths(t *testing.T) {
	py := mustRender(RenderPythonUnit(true, "/opt/R&D <py>/python3", "/Users/x/R&D <1>/uni-vpn", "/Users/x/Logs & more", "/opt/homebrew", nil))
	if args := parsePlist(t, py).Args; !reflect.DeepEqual(args, []string{"/opt/R&D <py>/python3", "/Users/x/R&D <1>/uni-vpn", "daemon"}) {
		t.Fatal(args)
	}
	if !strings.Contains(py, "<string>/Users/x/Logs &amp; more/launchd.log</string>") {
		t.Fatal(py)
	}
	gobin := mustRender(RenderUnit(true, "/Users/x/R&D <1>/uni-vpn", "/Users/x/Logs & more", "/opt/homebrew", nil))
	if args := parsePlist(t, gobin).Args; !reflect.DeepEqual(args, []string{"/Users/x/R&D <1>/uni-vpn", "daemon"}) {
		t.Fatal(args)
	}
}

func TestSystemdUnitKeepsPercentAndDollarLiteral(t *testing.T) {
	// ExecStart= expands %h and $HOME, Environment= only specifiers.
	env := []Pair{{"XDG_CONFIG_HOME", "/home/x/50%/$cfg"}}
	py := mustRender(RenderPythonUnit(false, "/usr/bin/python3", "/home/x/100%/$HOME/uni-vpn", "/home/x/state", "", env))
	gobin := mustRender(RenderUnit(false, "/home/x/100%/$HOME/uni-vpn", "/home/x/state", "", env))
	for text, want := range map[string]string{
		py:    `ExecStart="/usr/bin/python3" "/home/x/100%%/$$HOME/uni-vpn" daemon`,
		gobin: `ExecStart="/home/x/100%%/$$HOME/uni-vpn" daemon`,
	} {
		if !strings.Contains(text, want) || !strings.Contains(text, `Environment="XDG_CONFIG_HOME=/home/x/50%%/$cfg"`) {
			t.Fatal(text)
		}
	}
}

// plist is a minimal reader for the plists we render: the top dict's keys and values.
type plist struct {
	Label     string
	Args      []string
	Env       map[string]string
	KeepAlive bool
}

func parsePlist(t *testing.T, text string) plist {
	t.Helper()
	dec := xml.NewDecoder(strings.NewReader(text))
	var (
		p      = plist{Env: map[string]string{}}
		path   []string
		key    string
		envKey string
	)
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch tok := tok.(type) {
		case xml.StartElement:
			path = append(path, tok.Name.Local)
			if tok.Name.Local == "true" && len(path) == 3 && key == "KeepAlive" {
				p.KeepAlive = true
			}
		case xml.EndElement:
			path = path[:len(path)-1]
		case xml.CharData:
			s := string(tok)
			if strings.TrimSpace(s) == "" {
				continue
			}
			depth, last := len(path), path[len(path)-1]
			switch {
			case depth == 3 && last == "key":
				key = s
			case depth == 3 && key == "Label":
				p.Label = s
			case depth == 4 && key == "ProgramArguments":
				p.Args = append(p.Args, s)
			case depth == 4 && key == "EnvironmentVariables" && last == "key":
				envKey = s
			case depth == 4 && key == "EnvironmentVariables":
				p.Env[envKey] = s
			}
		}
	}
	return p
}

func TestLaunchdPlistExtraEnv(t *testing.T) {
	text := mustRender(RenderPythonUnit(true, "/opt/homebrew/bin/python3", "/Users/x/Uni Stuff/uni-vpn/bin/uni-vpn",
		"/Users/x/Library/Logs/uni-vpn", "/opt/homebrew", []Pair{{"XDG_CONFIG_HOME", "/Users/x/.cfg & co"}}))
	data := parsePlist(t, text)
	if data.Args[1] != "/Users/x/Uni Stuff/uni-vpn/bin/uni-vpn" || data.Env["XDG_CONFIG_HOME"] != "/Users/x/.cfg & co" ||
		!strings.HasPrefix(data.Env["PATH"], "/opt/homebrew/bin:") {
		t.Fatalf("%+v", data)
	}
	plain := mustRender(RenderPythonUnit(true, "/opt/homebrew/bin/python3", "/Users/x/uni-vpn/bin/uni-vpn",
		"/Users/x/Library/Logs/uni-vpn", "/opt/homebrew", nil))
	if _, ok := parsePlist(t, plain).Env["XDG_CONFIG_HOME"]; ok {
		t.Fatal(plain)
	}
}

func TestLaunchdPlist(t *testing.T) {
	text := mustRender(RenderPythonUnit(true, "/opt/homebrew/bin/python3", "/Users/x/uni-vpn/bin/uni-vpn",
		"/Users/x/Library/Logs/uni-vpn", "/opt/homebrew", nil))
	data := parsePlist(t, text)
	if data.Label != "de.davidvinu.uni-vpn" || !data.KeepAlive ||
		!reflect.DeepEqual(data.Args, []string{"/opt/homebrew/bin/python3", "/Users/x/uni-vpn/bin/uni-vpn", "daemon"}) ||
		!strings.HasPrefix(data.Env["PATH"], "/opt/homebrew/bin:") {
		t.Fatalf("%+v", data)
	}
}

func TestBinaryUnitDiffersOnlyInTheProgram(t *testing.T) {
	env := []Pair{{"XDG_CONFIG_HOME", "/home/x/.cfg"}}
	for _, extra := range [][]Pair{nil, env} {
		py := mustRender(RenderPythonUnit(false, "/usr/bin/python3", "/opt/u/uni-vpn", "/s", "", extra))
		gobin := mustRender(RenderUnit(false, "/opt/u/uni-vpn", "/s", "", extra))
		want := strings.Replace(py, `ExecStart="/usr/bin/python3" "/opt/u/uni-vpn" daemon`, `ExecStart="/opt/u/uni-vpn" daemon`, 1)
		if gobin != want || py == want {
			t.Fatalf("%s\n---\n%s", gobin, want)
		}
		py = mustRender(RenderPythonUnit(true, "/usr/bin/python3", "/opt/u/uni-vpn", "/s", "/opt/homebrew", extra))
		gobin = mustRender(RenderUnit(true, "/opt/u/uni-vpn", "/s", "/opt/homebrew", extra))
		want = strings.Replace(py, "    <string>/usr/bin/python3</string>\n", "", 1)
		if gobin != want || py == want {
			t.Fatalf("%s\n---\n%s", gobin, want)
		}
		if args := parsePlist(t, gobin).Args; !reflect.DeepEqual(args, []string{"/opt/u/uni-vpn", "daemon"}) {
			t.Fatal(args)
		}
	}
}

func TestPassthroughEnv(t *testing.T) {
	env := map[string]string{"XDG_STATE_HOME": "/s", "XDG_CONFIG_HOME": "/c", "HOME": "/h"}
	got := PassthroughEnv(func(k string) string { return env[k] })
	if !reflect.DeepEqual(got, []Pair{{"XDG_CONFIG_HOME", "/c"}, {"XDG_STATE_HOME", "/s"}}) {
		t.Fatal(got)
	}
}

// --- install and control (POSIX paths) ---

type recorder struct {
	mu    sync.Mutex
	calls [][]string
	reply func(Cmd) (Result, error)
}

func (r *recorder) run(c Cmd) (Result, error) {
	r.mu.Lock()
	r.calls = append(r.calls, c.Args)
	r.mu.Unlock()
	if r.reply != nil {
		return r.reply(c)
	}
	return Result{}, nil
}

func (r *recorder) called(args ...string) bool {
	for _, c := range r.calls {
		if reflect.DeepEqual(c, args) {
			return true
		}
	}
	return false
}

func setupHome(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("systemd and launchd paths, which must not contain backslashes")
	}
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_STATE_HOME", "")
	return h
}

func manager(goos string, r *recorder) (*Manager, *bytes.Buffer) {
	var out bytes.Buffer
	return &Manager{
		GOOS:       goos,
		Run:        r.run,
		Out:        &out,
		Sleep:      func(time.Duration) {},
		Getenv:     os.Getenv,
		Binary:     "/opt/uni-vpn/uni-vpn",
		BrewPrefix: func() string { return "/opt/homebrew" },
		Conhost:    `C:\Windows\System32\conhost.exe`,
	}, &out
}

func TestLinuxInstallWritesUnitAndEnables(t *testing.T) {
	h := setupHome(t)
	r := &recorder{}
	m, _ := manager("linux", r)
	files, err := m.Install(false)
	unit := filepath.Join(h, ".config", "systemd", "user", "uni-vpn.service")
	if err != nil || !reflect.DeepEqual(files, []string{unit}) {
		t.Fatal(files, err)
	}
	data, err := os.ReadFile(unit)
	if err != nil || !strings.Contains(string(data), `ExecStart="/opt/uni-vpn/uni-vpn" daemon`) {
		t.Fatal(string(data), err)
	}
	if !r.called("systemctl", "--user", "daemon-reload") || !r.called("systemctl", "--user", "enable", "--now", "uni-vpn") {
		t.Fatal(r.calls)
	}
}

func TestInstallTakesXDGFromEnvironment(t *testing.T) {
	h := setupHome(t)
	unit := filepath.Join(h, ".config", "systemd", "user", "uni-vpn.service")
	m, _ := manager("linux", &recorder{})
	t.Setenv("XDG_STATE_HOME", filepath.Join(h, "st"))
	if _, err := m.Install(false); err != nil {
		t.Fatal(err)
	}
	text, _ := os.ReadFile(unit)
	for _, want := range []string{
		fmt.Sprintf(`Environment="XDG_CONFIG_HOME=%s"`, filepath.Join(h, ".config")),
		fmt.Sprintf(`Environment="XDG_STATE_HOME=%s"`, filepath.Join(h, "st")),
	} {
		if !strings.Contains(string(text), want) {
			t.Fatal(string(text))
		}
	}
	os.Unsetenv("XDG_CONFIG_HOME")
	os.Unsetenv("XDG_STATE_HOME")
	if _, err := m.Install(false); err != nil {
		t.Fatal(err)
	}
	if text, _ := os.ReadFile(unit); strings.Contains(string(text), "Environment=") {
		t.Fatal(string(text))
	}
}

func TestInstallRestartsRunningServiceSoNewCodeIsLoaded(t *testing.T) {
	// "enable --now" leaves a running service untouched; after an update or a repeated
	// install.sh the old code would otherwise keep running (seen 2026-09-08).
	setupHome(t)
	r := &recorder{}
	m, _ := manager("linux", r)
	if _, err := m.Install(false); err != nil {
		t.Fatal(err)
	}
	if !r.called("systemctl", "--user", "enable", "--now", "uni-vpn") ||
		!reflect.DeepEqual(r.calls[len(r.calls)-1], []string{"systemctl", "--user", "restart", "uni-vpn"}) {
		t.Fatal(r.calls)
	}
}

func TestInstallReportsFailedLoad(t *testing.T) {
	h := setupHome(t)
	r := &recorder{reply: func(c Cmd) (Result, error) {
		if strings.Join(c.Args, " ") == "systemctl --user daemon-reload" {
			return Result{Code: 1, Stderr: "Failed to connect to bus: No medium found"}, nil
		}
		return Result{}, nil
	}}
	m, _ := manager("linux", r)
	_, err := m.Install(false)
	var se *Error
	unit := filepath.Join(h, ".config", "systemd", "user", "uni-vpn.service")
	if !errors.As(err, &se) || se.Error() != "systemctl --user daemon-reload: Failed to connect to bus: No medium found" ||
		!reflect.DeepEqual(se.Files, []string{unit}) {
		t.Fatal(err)
	}
	if _, err := os.Stat(unit); err != nil {
		t.Fatal(err)
	}
}

func TestInstallReportsMissingSystemctl(t *testing.T) {
	setupHome(t)
	m, _ := manager("linux", &recorder{reply: func(c Cmd) (Result, error) {
		return Exec(Cmd{Args: []string{"/nonexistent/systemctl"}})
	}})
	_, err := m.Install(false)
	var se *Error
	if !errors.As(err, &se) || se.Error() != "systemctl: No such file or directory" {
		t.Fatal(err)
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	setupHome(t)
	r := &recorder{}
	m, out := manager("linux", r)
	files, err := m.Install(true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(files[0]); err == nil || len(r.calls) != 0 {
		t.Fatal(r.calls)
	}
	if out.String() != fmt.Sprintf("-> would write %s and load the service\n", files[0]) {
		t.Fatal(out.String())
	}
}

func TestMacOSInstallBootstraps(t *testing.T) {
	h := setupHome(t)
	r := &recorder{}
	m, _ := manager("darwin", r)
	files, err := m.Install(false)
	plistPath := filepath.Join(h, "Library", "LaunchAgents", "de.davidvinu.uni-vpn.plist")
	if err != nil || !reflect.DeepEqual(files, []string{plistPath}) {
		t.Fatal(files, err)
	}
	data, _ := os.ReadFile(plistPath)
	if p := parsePlist(t, string(data)); !reflect.DeepEqual(p.Args, []string{"/opt/uni-vpn/uni-vpn", "daemon"}) ||
		!strings.HasPrefix(p.Env["PATH"], "/opt/homebrew/bin:") {
		t.Fatalf("%+v", p)
	}
	bootstrapped := false
	for _, c := range r.calls {
		bootstrapped = bootstrapped || (c[0] == "launchctl" && c[1] == "bootstrap")
	}
	if !bootstrapped {
		t.Fatal(r.calls)
	}
}

func TestMacOSBootstrapIsRetried(t *testing.T) {
	setupHome(t)
	fails := 2
	r := &recorder{reply: func(c Cmd) (Result, error) {
		if c.Args[1] == "bootstrap" && fails > 0 {
			fails--
			return Result{Code: 5, Stderr: "Bootstrap failed: 5: Input/output error"}, nil
		}
		return Result{}, nil
	}}
	m, _ := manager("darwin", r)
	if _, err := m.Install(false); err != nil {
		t.Fatal(err)
	}
	r.reply = func(c Cmd) (Result, error) { return Result{Code: 5, Stderr: "Bootstrap failed: 5"}, nil }
	if _, err := m.Install(false); err == nil || !strings.Contains(err.Error(), "Bootstrap failed: 5") {
		t.Fatal(err)
	}
}

func TestUninstallRemovesUnit(t *testing.T) {
	setupHome(t)
	r := &recorder{}
	m, _ := manager("linux", r)
	files, err := m.Install(false)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := m.Uninstall(); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if _, err := os.Stat(files[0]); err == nil {
		t.Fatal("unit left")
	}
	if !r.called("systemctl", "--user", "disable", "--now", "uni-vpn") {
		t.Fatal(r.calls)
	}
}

func TestUninstallWithoutSystemctlOrLaunchctlIsFalse(t *testing.T) {
	setupHome(t)
	for _, goos := range []string{"linux", "darwin"} {
		m, _ := manager(goos, &recorder{reply: func(Cmd) (Result, error) {
			return Exec(Cmd{Args: []string{"/nonexistent/tool"}})
		}})
		unit := m.UnitTargetPath()
		os.MkdirAll(filepath.Dir(unit), 0o700)
		os.WriteFile(unit, []byte("x"), 0o600)
		if ok, err := m.Uninstall(); ok || err != nil {
			t.Fatal(goos, ok, err)
		}
		if _, err := os.Stat(unit); err == nil {
			t.Fatal(goos, "unit left")
		}
	}
}

func TestDisconnectNeverGoesThroughAProxy(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		seen = append(seen, req.Method+" "+req.URL.Path)
		mu.Unlock()
		fmt.Fprint(w, `{"state": "idle"}`)
	}))
	defer srv.Close()
	port, _ := strconv.Atoi(srv.URL[strings.LastIndex(srv.URL, ":")+1:])
	for _, k := range []string{"HTTP_PROXY", "http_proxy"} {
		t.Setenv(k, "http://127.0.0.1:9")
	}
	for _, k := range []string{"NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
	m, _ := manager("windows", &recorder{})
	m.HTTPPort = func() (int, error) { return port, nil }
	m.disconnectDaemon(time.Second)
	if !reflect.DeepEqual(seen, []string{"POST /api/disconnect", "GET /status.json"}) {
		t.Fatal(seen)
	}
}

func TestIsActive(t *testing.T) {
	m, _ := manager("linux", &recorder{reply: func(Cmd) (Result, error) { return Result{Stdout: "active\n"}, nil }})
	if !m.IsActive() {
		t.Fatal("not active")
	}
	m, _ = manager("linux", &recorder{reply: func(c Cmd) (Result, error) { return Result{}, errors.New("no systemctl") }})
	if m.IsActive() {
		t.Fatal("active")
	}
}

func TestControlRunsSystemctlWithOutputOnTerminal(t *testing.T) {
	setupHome(t)
	var seen []Cmd
	r := &recorder{reply: func(c Cmd) (Result, error) {
		seen = append(seen, c)
		return Result{Code: 3}, nil
	}}
	m, out := manager("linux", r)
	if rc, _ := m.Control("status"); rc != 1 || !strings.Contains(out.String(), "please run install.sh") {
		t.Fatal(rc, out.String())
	}
	m.Install(false)
	seen = nil
	if rc, err := m.Control("status"); rc != 3 || err != nil {
		t.Fatal(rc, err)
	}
	if !reflect.DeepEqual(seen, []Cmd{{Args: []string{"systemctl", "--user", "status", "--no-pager", "uni-vpn"}}}) {
		t.Fatal(seen)
	}
	if _, err := m.Control("bogus"); err == nil {
		t.Fatal("bogus action accepted")
	}
}

// --- Windows (Task Scheduler), simulated ---

func TestWindowsControlEndsTaskAfterLoggingOut(t *testing.T) {
	// The daemon is asked to log out first, then the task ends; "/Run" only once it stopped.
	var mu sync.Mutex
	var api []string
	state := "connected"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		api = append(api, req.Method+" "+req.URL.Path+" "+req.Header.Get("X-Uni-VPN"))
		if req.URL.Path == "/api/disconnect" {
			state = "disconnecting"
			return
		}
		fmt.Fprintf(w, `{"state": %q}`, state)
		state = "idle"
	}))
	defer srv.Close()
	port, _ := strconv.Atoi(srv.URL[strings.LastIndex(srv.URL, ":")+1:])

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	running := 2
	r := &recorder{reply: func(c Cmd) (Result, error) {
		if c.Args[0] == "powershell" {
			if running > 0 {
				running--
				return Result{Stdout: "Running\r\n"}, nil
			}
			return Result{Stdout: "Ready\r\n"}, nil
		}
		return Result{}, nil
	}}
	m, _ := manager("windows", r)
	m.HTTPPort = func() (int, error) { return port, nil }
	target := m.UnitTargetPath()
	os.MkdirAll(filepath.Dir(target), 0o700)
	os.WriteFile(target, []byte("x"), 0o600)
	if rc, err := m.Control("restart"); rc != 0 || err != nil {
		t.Fatal(rc, err)
	}
	if !reflect.DeepEqual(api, []string{"POST /api/disconnect 1", "GET /status.json ", "GET /status.json "}) {
		t.Fatal(api)
	}
	var names []string
	for _, c := range r.calls {
		names = append(names, strings.Join(c, " "))
	}
	ps := "powershell -NoProfile -NonInteractive -Command (Get-ScheduledTask -TaskName 'uni-vpn').State"
	if !reflect.DeepEqual(names, []string{"schtasks /End /TN uni-vpn", ps, ps, ps, "schtasks /Run /TN uni-vpn"}) {
		t.Fatal(names)
	}
}

func TestWindowsControlStopsAfterAFailedStep(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	r := &recorder{reply: func(c Cmd) (Result, error) {
		if c.Args[0] == "schtasks" && c.Args[1] == "/Change" {
			return Result{Code: 1}, nil
		}
		return Result{}, nil
	}}
	m, out := manager("windows", r)
	m.HTTPPort = func() (int, error) { return 9, nil } // discard: nothing listens
	if rc, _ := m.Control("enable"); rc != 1 || !strings.Contains(out.String(), "please run install.ps1") {
		t.Fatal(rc, out.String())
	}
	target := m.UnitTargetPath()
	os.MkdirAll(filepath.Dir(target), 0o700)
	os.WriteFile(target, []byte("x"), 0o600)
	if rc, _ := m.Control("enable"); rc != 1 {
		t.Fatal(rc)
	}
	if r.called("schtasks", "/Run", "/TN", "uni-vpn") {
		t.Fatal("/Run after a failed /Change", r.calls)
	}
}

func TestWindowsInstallWritesUTF16TaskAndRuns(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("USERDOMAIN", "UNI")
	t.Setenv("USERNAME", "ab123")
	r := &recorder{}
	m, out := manager("windows", r)
	m.HTTPPort = func() (int, error) { return 9, nil }
	files, err := m.Install(false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Warning: /opt/uni-vpn/uni-vpn is not in Program Files") {
		t.Fatal(out.String())
	}
	data, _ := os.ReadFile(files[0])
	if data[0] != 0xFF || data[1] != 0xFE {
		t.Fatal("no BOM")
	}
	units := make([]uint16, (len(data)-2)/2)
	for i := range units {
		units[i] = uint16(data[2+2*i]) | uint16(data[3+2*i])<<8
	}
	text := strings.ReplaceAll(string(utf16.Decode(units)), "\r\n", "\n")
	if text != winsys.RenderTaskBinary(`C:\Windows\System32\conhost.exe`, "/opt/uni-vpn/uni-vpn", `UNI\ab123`, "/opt") {
		t.Fatal(text)
	}
	if !r.called("schtasks", "/Create", "/TN", "uni-vpn", "/XML", files[0], "/F") ||
		!reflect.DeepEqual(r.calls[len(r.calls)-1], []string{"schtasks", "/Run", "/TN", "uni-vpn"}) {
		t.Fatal(r.calls)
	}
}

func TestWindowsInstallRefusesWhenTheOldTaskKeepsRunning(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	r := &recorder{reply: func(c Cmd) (Result, error) { return Result{Stdout: "Running"}, nil }}
	m, _ := manager("windows", r)
	m.HTTPPort = func() (int, error) { return 9, nil }
	_, err := m.Install(false)
	var se *Error
	if !errors.As(err, &se) || se.Msg != "The running service did not stop, log out and in, then run the installer again" {
		t.Fatal(err)
	}
	if r.called("schtasks", "/Run", "/TN", "uni-vpn") {
		t.Fatal(r.calls)
	}
}
