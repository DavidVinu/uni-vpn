package desktop

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DavidVinu/uni-vpn/internal/service"
)

// The tests are a port of tests/test_desktop.py, without the build steps the Go core leaves out.

type harness struct {
	t        *testing.T
	home     string
	d        *Desktop
	out      *bytes.Buffer
	created  []string
	commands [][]string
	spawns   [][]string
	reply    func(args []string) service.Result
}

func newHarness(t *testing.T, goos string) *harness {
	home := t.TempDir()
	h := &harness{t: t, home: home, out: &bytes.Buffer{}}
	env := map[string]string{"XDG_DATA_HOME": filepath.Join(home, "data"), "XDG_CONFIG_HOME": filepath.Join(home, "config"),
		"APPDATA": filepath.Join(home, "AppData")}
	h.d = &Desktop{
		GOOS: goos, Root: filepath.Join(home, "uni-vpn"), Out: h.out,
		Run: func(c service.Cmd) (service.Result, error) {
			h.commands = append(h.commands, c.Args)
			if h.reply != nil {
				return h.reply(c.Args), nil
			}
			return service.Result{}, nil
		},
		Getenv:     func(key string) string { return env[key] },
		Home:       func() string { return home },
		Spawn:      func(args []string) bool { h.spawns = append(h.spawns, args); return true },
		HasDesktop: func() bool { return true },
		IsAdmin:    func() bool { return false },
		UID:        501,
		PackageDir: filepath.Join(home, "Applications", "Uni VPN.app", "Contents", "Resources"),
	}
	return h
}

func (h *harness) install(dry bool) bool {
	return h.d.Install(1081, dry, func(path string) { h.created = append(h.created, path) })
}

func (h *harness) ran(args ...string) bool {
	return slices.ContainsFunc(h.commands, func(c []string) bool { return reflect.DeepEqual(c, args) })
}

func touch(t *testing.T, path string, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o755); err != nil {
		t.Fatal(err)
	}
}

func read(path string) string {
	data, _ := os.ReadFile(path)
	return string(data)
}

func TestShellsAndIconsAreInTheRepository(t *testing.T) {
	for _, name := range []string{"icon.svg", "linux/uni-vpn-app.py"} {
		if _, err := os.Stat(filepath.Join("..", "..", "app", name)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBrowserEntries(t *testing.T) {
	for _, tc := range []struct {
		goos, path, want string
		command          []string
	}{
		{"linux", "data/applications/uni-vpn.desktop", "Exec=xdg-open http://127.0.0.1:1081/\n", nil},
		{"windows", "AppData/Microsoft/Windows/Start Menu/Programs/Uni VPN.url",
			"[InternetShortcut]\r\nURL=http://127.0.0.1:1081/\r\n", nil},
		{"darwin", "Applications/Uni VPN.app", "", []string{"osacompile", "-o", "", "-e", `open location "http://127.0.0.1:1081/"`}},
	} {
		t.Run(tc.goos, func(t *testing.T) {
			h := newHarness(t, tc.goos)
			path := filepath.Join(h.home, filepath.FromSlash(tc.path))
			if h.install(true) || len(h.created) != 0 || !strings.Contains(h.out.String(), "would add "+path) {
				t.Fatal(h.out.String())
			}
			if h.install(false) {
				t.Fatal("native app reported")
			}
			if !reflect.DeepEqual(h.created, []string{path}) {
				t.Fatal(h.created)
			}
			if tc.command != nil {
				tc.command[2] = path
				if !h.ran(tc.command...) {
					t.Fatal(h.commands)
				}
			} else if !strings.Contains(read(path), tc.want) {
				t.Fatal(read(path))
			}
			if !strings.Contains(h.out.String(), "opens in the browser for now") || h.spawns != nil {
				t.Fatal(h.out.String(), h.spawns)
			}
		})
	}
}

// --- macOS ---

func TestLoginPlistIsWhatPlistlibWrites(t *testing.T) {
	want := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>de.davidvinu.uni-vpn.app</string>
	<key>LimitLoadToSessionType</key>
	<string>Aqua</string>
	<key>ProcessType</key>
	<string>Interactive</string>
	<key>ProgramArguments</key>
	<array>
		<string>/Applications/R&amp;D Uni VPN.app/Contents/MacOS/Uni VPN</string>
		<string>--hidden</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
</dict>
</plist>
`
	if got := LoginPlist("/Applications/R&D Uni VPN.app/Contents/MacOS/Uni VPN"); got != want {
		t.Fatal(got)
	}
}

func TestMacOSRegistersTheReadyApp(t *testing.T) {
	for _, tc := range []struct {
		name string
		prep func(h *harness) string // returns the app
	}{
		{"from the package", func(h *harness) string {
			touch(h.t, filepath.Join(h.d.PackageDir, "app", "bin", "uni-vpn-core"), "")
			app := filepath.Dir(filepath.Dir(h.d.PackageDir))
			touch(h.t, filepath.Join(app, "Contents", "MacOS", AppName), "")
			// The one an earlier setup built goes.
			touch(h.t, filepath.Join(h.home, "Applications", "Uni VPN.app", "Contents", "MacOS", "applet"), "")
			return app
		}},
		{"built by an earlier setup", func(h *harness) string {
			app := filepath.Join(h.home, "Applications", "Uni VPN.app")
			touch(h.t, filepath.Join(app, "Contents", "MacOS", AppName), "")
			touch(h.t, filepath.Join(app, "Contents", "Info.plist"), "<dict>\n\t<key>UniVPNPort</key>\n\t<string>1081</string>\n</dict>")
			return app
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, "darwin")
			h.d.PackageDir = filepath.Join(h.home, "Pkg", "Uni VPN.app", "Contents", "Resources")
			app := tc.prep(h)
			login := filepath.Join(h.home, "Library", "LaunchAgents", "de.davidvinu.uni-vpn.app.plist")
			if !h.install(true) || !strings.Contains(h.out.String(), "would start "+app+" at login") {
				t.Fatal(h.out.String())
			}
			if !h.install(false) || !reflect.DeepEqual(h.created, []string{login}) {
				t.Fatal(h.created, h.out.String())
			}
			if read(login) != LoginPlist(filepath.Join(app, "Contents", "MacOS", AppName)) ||
				!h.ran("launchctl", "bootstrap", "gui/501", login) || !h.ran("pkill", "-x", AppName) {
				t.Fatal(h.commands)
			}
			if got := h.d.AppCommand(1081, "&user=ab123"); !reflect.DeepEqual(got, []string{"open", "-a", app, "uni-vpn://open?user=ab123"}) {
				t.Fatal(got)
			}
			if tc.name == "from the package" {
				if _, err := os.Stat(filepath.Join(h.home, "Applications", "Uni VPN.app")); err == nil {
					t.Fatal("old app left")
				}
			}
		})
	}
}

func TestMacOSAppBuiltForAnotherPortIsNotUsed(t *testing.T) {
	h := newHarness(t, "darwin")
	app := filepath.Join(h.home, "Applications", "Uni VPN.app")
	touch(t, filepath.Join(app, "Contents", "MacOS", AppName), "")
	touch(t, filepath.Join(app, "Contents", "Info.plist"), "<key>UniVPNPort</key>\n\t<string>1099</string>")
	if h.install(false) {
		t.Fatal("app with the wrong port used")
	}
}

func TestPagesBecomeAppURLs(t *testing.T) {
	h := newHarness(t, "darwin")
	if h.d.AppCommand(1081, "") != nil {
		t.Fatal("app without a binary")
	}
	app := filepath.Join(h.home, "Applications", "Uni VPN.app")
	touch(t, filepath.Join(app, "Contents", "MacOS", AppName), "")
	for page, want := range map[string]string{"": "uni-vpn://open", "#settings": "uni-vpn://settings",
		"&user=ab123": "uni-vpn://open?user=ab123"} {
		if got := h.d.AppCommand(1081, page); !reflect.DeepEqual(got, []string{"open", "-a", app, want}) {
			t.Fatal(page, got)
		}
	}
}

// The updates put a newer app next to the core: it runs from then on, the one in
// /Applications (root's) stays as the Launchpad entry.
func TestMacOSPrefersTheUpdatedApp(t *testing.T) {
	h := newHarness(t, "darwin")
	touch(t, filepath.Join(h.d.PackageDir, "app", "bin", "uni-vpn-core"), "")
	touch(t, filepath.Join(h.home, "Applications", "Uni VPN.app", "Contents", "MacOS", AppName), "")
	updated := filepath.Join(h.d.Root, "desktop", "Uni VPN.app")
	touch(t, filepath.Join(updated, "Contents", "MacOS", AppName), "")
	if h.d.UpdatedMacApp() != "" {
		t.Fatal("an app the updates did not list")
	}
	touch(t, filepath.Join(h.d.Root, ".core-files"), "bin/uni-vpn-core\ndesktop/Uni VPN.app/Contents/MacOS/Uni VPN\n")
	if h.d.UpdatedMacApp() != updated {
		t.Fatal(h.d.UpdatedMacApp())
	}
	if !h.install(false) || read(filepath.Join(h.home, "Library", "LaunchAgents", LoginLabel+".plist")) !=
		LoginPlist(filepath.Join(updated, "Contents", "MacOS", AppName)) {
		t.Fatal(h.out.String())
	}
	if got := h.d.AppCommand(1081, "#settings"); !reflect.DeepEqual(got, []string{"open", "-a", updated, "uni-vpn://settings"}) {
		t.Fatal(got)
	}
	if h.d.AppPath() != filepath.Join(h.home, "Applications", "Uni VPN.app") {
		t.Fatal(h.d.AppPath())
	}
}

func TestMacOSLoginItemMovesToTheUpdatedApp(t *testing.T) {
	h := newHarness(t, "darwin")
	login := h.d.LoginPath()
	h.d.UseUpdatedApp()
	if _, err := os.Stat(login); err == nil {
		t.Fatal("login item without an app")
	}
	touch(t, login, LoginPlist("/Applications/Uni VPN.app/Contents/MacOS/Uni VPN"))
	h.d.UseUpdatedApp()
	if read(login) != LoginPlist("/Applications/Uni VPN.app/Contents/MacOS/Uni VPN") {
		t.Fatal("changed without an updated app")
	}
	touch(t, filepath.Join(h.d.Root, filepath.FromSlash(UpdatedAppFile("darwin"))), "")
	touch(t, filepath.Join(h.d.Root, ".core-files"), UpdatedAppFile("darwin")+"\n")
	h.d.UseUpdatedApp()
	if read(login) != LoginPlist(filepath.Join(h.d.Root, filepath.FromSlash(UpdatedAppFile("darwin")))) ||
		h.ran("launchctl", "bootout", "gui/501", login) {
		t.Fatal(read(login), h.commands)
	}
}

func TestRestartAppNeedsAnUpdatedApp(t *testing.T) {
	for _, goos := range []string{"darwin", "windows", "linux"} {
		h := newHarness(t, goos)
		h.d.Running = func(string) []Process { t.Fatal("looked for the shell"); return nil }
		touch(t, filepath.Join(h.d.Root, filepath.FromSlash(UpdatedAppFile(goos))), "installed by an installer")
		touch(t, h.d.LoginPath(), "")
		if h.d.RestartApp(1081) || len(h.commands) != 0 || len(h.spawns) != 0 {
			t.Fatal(goos, h.commands, h.spawns)
		}
	}
}

func TestRestartAppMacOS(t *testing.T) {
	h := newHarness(t, "darwin")
	touch(t, filepath.Join(h.d.Root, filepath.FromSlash(UpdatedAppFile("darwin"))), "")
	touch(t, filepath.Join(h.d.Root, ".core-files"), UpdatedAppFile("darwin")+"\n")
	login := filepath.Join(h.home, "Library", "LaunchAgents", LoginLabel+".plist")
	if !h.d.RestartApp(1081) || read(login) != LoginPlist(filepath.Join(h.d.Root, filepath.FromSlash(UpdatedAppFile("darwin")))) {
		t.Fatal(read(login))
	}
	if !h.ran("pkill", "-x", AppName) || !h.ran("launchctl", "bootstrap", "gui/501", login) {
		t.Fatal(h.commands)
	}
}

func TestRestartAppWindows(t *testing.T) {
	h := newHarness(t, "windows")
	touch(t, filepath.Join(h.d.Root, "desktop", "Uni VPN.exe"), "")
	touch(t, filepath.Join(h.d.Root, ".core-files"), "bin/uni-vpn-core.exe\ndesktop/Uni VPN.exe\n")
	if h.d.RestartApp(1081) {
		t.Fatal("restarted without the login item")
	}
	touch(t, h.d.LoginPath(), "")
	if !h.d.RestartApp(1081) || !h.ran("taskkill", "/F", "/IM", "Uni VPN.exe") ||
		!reflect.DeepEqual(h.spawns, [][]string{{"explorer.exe", h.d.LoginPath()}}) {
		t.Fatal(h.commands, h.spawns)
	}
}

func TestRestartAppLinuxUsesTheDesktopOfTheRunningShell(t *testing.T) {
	posixOnly(t)
	h := newHarness(t, "linux")
	script := filepath.Join(h.d.Root, "app", "linux", "uni-vpn-app.py")
	touch(t, script, "")
	touch(t, filepath.Join(h.d.Root, ".core-files"), "bin/uni-vpn-core\napp/linux/uni-vpn-app.py\n")
	env := []string{"HOME=/home/a", "WAYLAND_DISPLAY=wayland-0"}
	running := []Process{{PID: 42, Args: []string{"/usr/bin/python3", script, "--port", "1081", "--hidden"}, Env: env}}
	h.d.Running = func(arg string) []Process {
		if arg != script {
			t.Fatal(arg)
		}
		return running
	}
	var spawned [][]string
	h.d.Run = func(c service.Cmd) (service.Result, error) {
		h.commands = append(h.commands, c.Args)
		running = nil
		return service.Result{}, nil
	}
	h.d.SpawnEnv = func(args, got []string) bool {
		if !reflect.DeepEqual(got, env) {
			t.Fatal(got)
		}
		spawned = append(spawned, args)
		return true
	}
	if !h.d.RestartApp(1081) || !h.ran("pkill", "-f", script) ||
		!reflect.DeepEqual(spawned, [][]string{{"/usr/bin/python3", script, "--port", "1081", "--hidden"}}) {
		t.Fatal(h.commands, spawned)
	}

	// Without a display to show it on, the running one stays.
	h.commands, spawned = nil, nil
	running = []Process{{PID: 42, Args: []string{"/usr/bin/python3", script}, Env: []string{"DISPLAY="}}}
	if h.d.RestartApp(1081) || len(h.commands) != 0 || len(spawned) != 0 {
		t.Fatal(h.commands, spawned)
	}
}

// --- Windows ---

func TestShortcutScriptQuotesPaths(t *testing.T) {
	script := ShortcutScript(`C:\Users\O'Brien\Uni VPN.lnk`, `C:\Program Files\uni-vpn\desktop\Uni VPN.exe`, "--port 1081 --hidden",
		`C:\x.exe`)
	for _, want := range []string{`CreateShortcut('C:\Users\O''Brien\Uni VPN.lnk')`, "$s.Arguments = '--port 1081 --hidden'",
		`$s.WorkingDirectory = 'C:\Program Files\uni-vpn\desktop'`, `$s.IconLocation = 'C:\x.exe,0'`} {
		if !strings.Contains(script, want) {
			t.Fatal(want, script)
		}
	}
}

func TestWindowsRegistersTheShippedApp(t *testing.T) {
	h := newHarness(t, "windows")
	exe := h.d.WindowsExe()
	touch(t, exe, "")
	touch(t, filepath.Join(h.d.WindowsBuildDir(), PrebuiltMarker), "")
	legacy := h.d.LegacyPaths()[0]
	touch(t, legacy, "")
	h.reply = func(args []string) service.Result {
		if args[0] == "powershell.exe" {
			// The shortcut script stands in for the .lnk it would create.
			for _, path := range []string{h.d.AppPath(), h.d.LoginPath()} {
				if strings.Contains(args[len(args)-1], PSQuote(path)) {
					touch(t, path, "lnk")
				}
			}
		}
		return service.Result{}
	}
	if !h.install(true) || !strings.Contains(h.out.String(), "would add "+h.d.AppPath()) {
		t.Fatal(h.out.String())
	}
	if !h.install(false) || !reflect.DeepEqual(h.created, []string{h.d.AppPath(), h.d.LoginPath()}) {
		t.Fatal(h.created)
	}
	if _, err := os.Stat(legacy); err == nil {
		t.Fatal("old entry left")
	}
	if !h.ran("taskkill", "/F", "/IM", "Uni VPN.exe") ||
		!reflect.DeepEqual(h.spawns, [][]string{{"explorer.exe", h.d.LoginPath()}}) {
		t.Fatal(h.commands, h.spawns)
	}
	if got := h.d.AppCommand(1081, "#settings"); !reflect.DeepEqual(got, []string{exe, "--port", "1081", "--page", "#settings"}) {
		t.Fatal(got)
	}
	// Setup runs elevated: Explorer opens the app as the signed-in user.
	h.spawns = nil
	h.d.IsAdmin = func() bool { return true }
	if !h.d.OpenApp(1081, "#settings") || !reflect.DeepEqual(h.spawns, [][]string{{"explorer.exe", h.d.AppPath()}}) {
		t.Fatal(h.spawns)
	}
	h.d.Uninstall()
	if _, err := os.Stat(h.d.WindowsBuildDir()); err == nil {
		t.Fatal("desktop folder left")
	}
}

// --- Linux ---

func TestDesktopEntryQuotesAndNamesTheWindowClass(t *testing.T) {
	h := newHarness(t, "linux")
	text := h.d.DesktopEntry("/usr/bin/python3", 1081, false)
	for _, want := range []string{`Exec="/usr/bin/python3" `, "--port 1081\n", "StartupWMClass=de.davidvinu.UniVPN",
		"Icon=de.davidvinu.UniVPN"} {
		if !strings.Contains(text, want) {
			t.Fatal(want, text)
		}
	}
	if strings.Contains(text, "--hidden") {
		t.Fatal(text)
	}
	hidden := h.d.DesktopEntry("/usr/bin/python3", 1081, true)
	if !strings.Contains(hidden, "--hidden") || !strings.Contains(hidden, "NoDisplay=true") {
		t.Fatal(hidden)
	}
	if got := DesktopQuote(`a "b" $c 100%`); got != `"a \"b\" \$c 100%%"` {
		t.Fatal(got)
	}
}

func TestLinuxApp(t *testing.T) {
	posixOnly(t)
	for _, indicator := range []bool{true, false} {
		h := newHarness(t, "linux")
		python := filepath.Join(h.home, "python3")
		touch(t, python, "#!/bin/sh\n")
		h.d.Candidates = []string{filepath.Join(h.home, "missing"), python, python}
		touch(t, filepath.Join(h.d.Root, "app", "linux", "uni-vpn-app.py"), "")
		touch(t, filepath.Join(h.d.Root, "app", "icon.svg"), "<svg/>")
		h.reply = func(args []string) service.Result {
			if len(args) == 3 && args[2] == IndicatorCheck && !indicator {
				return service.Result{Code: 1}
			}
			return service.Result{}
		}
		old := filepath.Join(h.home, "data", "applications", "uni-vpn.desktop")
		touch(t, old, "old")
		if !h.install(false) {
			t.Fatal(h.out.String())
		}
		if _, err := os.Stat(old); err == nil {
			t.Fatal("old entry left")
		}
		entry := filepath.Join(h.home, "data", "applications", "de.davidvinu.UniVPN.desktop")
		login := filepath.Join(h.home, "config", "autostart", "de.davidvinu.UniVPN.desktop")
		icon := filepath.Join(h.home, "data", "icons", "hicolor", "scalable", "apps", "de.davidvinu.UniVPN.svg")
		want := []string{icon, entry}
		if indicator {
			want = append(want, login)
		}
		if !reflect.DeepEqual(h.created, want) || read(icon) != "<svg/>" {
			t.Fatal(h.created)
		}
		if indicator != (len(h.spawns) == 1) || (indicator && !slices.Contains(h.spawns[0], "--hidden")) {
			t.Fatal(h.spawns)
		}
		got := h.d.AppCommand(1081, "#settings")
		if !reflect.DeepEqual(got, []string{python, filepath.Join(h.d.Root, "app", "linux", "uni-vpn-app.py"),
			"--port", "1081", "--page", "#settings"}) {
			t.Fatal(got)
		}
	}
}

func TestLinuxWithoutTheShellScriptOpensTheBrowser(t *testing.T) {
	h := newHarness(t, "linux")
	h.d.Candidates = []string{"/bin/sh"}
	if h.install(false) || h.commands != nil {
		t.Fatal(h.commands)
	}
}

// --- open ---

func TestSetupPrefillBecomesAPage(t *testing.T) {
	if got := PageFor([]Pair{{"user", "ab 123"}, {"university", ""}}); got != "&user=ab+123" {
		t.Fatal(got)
	}
	if got := PageFor(nil); got != "" {
		t.Fatal(got)
	}
	if got := Query([]Pair{{"user", "ab123"}, {"university", "ethz"}}); got != "user=ab123&university=ethz" {
		t.Fatal(got)
	}
}

func TestNoDesktopNoApp(t *testing.T) {
	h := newHarness(t, "windows")
	touch(t, h.d.WindowsExe(), "")
	h.d.HasDesktop = func() bool { return false }
	if h.d.OpenApp(1081, "") || h.spawns != nil {
		t.Fatal(h.spawns)
	}
	h.d.HasDesktop = func() bool { return true }
	if !h.d.OpenApp(1081, "") || !reflect.DeepEqual(h.spawns, [][]string{{h.d.WindowsExe(), "--port", "1081"}}) {
		t.Fatal(h.spawns)
	}
}

func TestHasDesktop(t *testing.T) {
	for _, tc := range []struct {
		goos string
		env  map[string]string
		want bool
	}{
		{"windows", nil, true},
		{"darwin", nil, true},
		{"darwin", map[string]string{"SSH_CONNECTION": "x"}, false},
		{"linux", nil, false},
		{"linux", map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, true},
		{"linux", map[string]string{"SSH_CONNECTION": "x", "DISPLAY": ":0"}, true},
	} {
		if got := HasDesktop(tc.goos, func(k string) string { return tc.env[k] }); got != tc.want {
			t.Error(tc.goos, tc.env, got)
		}
	}
}

// posixOnly: the Linux shell's Python is found by its execute bits.
func posixOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX only")
	}
}

func TestRunningFindsTheProcessAndItsEnvironment(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc")
	}
	marker := filepath.Join(t.TempDir(), "uni-vpn-app.py")
	cmd := exec.Command("sh", "-c", "sleep 30; exit 0", marker)
	cmd.Env = []string{"WAYLAND_DISPLAY=wayland-1"}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	for i := 0; i < 50; i++ {
		if got := Running(marker); len(got) == 1 {
			if got[0].PID != cmd.Process.Pid || !slices.Contains(got[0].Env, "WAYLAND_DISPLAY=wayland-1") ||
				!reflect.DeepEqual(got[0].Args, []string{"sh", "-c", "sleep 30; exit 0", marker}) {
				t.Fatal(got)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("not found")
}
