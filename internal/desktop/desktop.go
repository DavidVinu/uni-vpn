// Package desktop sets up the Uni VPN app: a native window around the page on
// 127.0.0.1:<http_port>, plus a menu bar (macOS), notification area (Windows) or panel icon
// (Linux) that starts at login. It mirrors uni_vpn/desktop.py with one difference: the Go
// core never builds the native shells on the user's machine (no swiftc, no csc, no WebView2
// download). It registers the ready ones when they are there: the app the macOS .pkg put in
// /Applications (or one an earlier setup built), "desktop/Uni VPN.exe" on Windows, the GTK
// script app/linux/uni-vpn-app.py with a system Python that has WebKitGTK on Linux.
// Otherwise the app entry opens the page in the browser, as desktop.py does.
package desktop

import (
	"bytes"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/DavidVinu/uni-vpn/internal/platform"
	"github.com/DavidVinu/uni-vpn/internal/service"
	"github.com/DavidVinu/uni-vpn/internal/winsys"
)

const (
	AppName    = "Uni VPN"
	AppID      = "de.davidvinu.UniVPN"      // Linux: application id, desktop file and icon name
	BundleID   = "de.davidvinu.uni-vpn.app" // macOS
	LoginLabel = "de.davidvinu.uni-vpn.app" // macOS LaunchAgent that starts the menu bar item
	URLScheme  = "uni-vpn"
	// PrebuiltMarker is written next to the app by the Windows installer, which ships it ready built.
	PrebuiltMarker = ".prebuilt"
)

// GTKCheck and IndicatorCheck are run with a candidate Python, as in desktop.py.
const (
	GTKCheck = "import gi; gi.require_version('Gtk', '3.0')\n" +
		"for v in ('4.1', '4.0'):\n" +
		"    try:\n" +
		"        gi.require_version('WebKit2', v); break\n" +
		"    except ValueError:\n" +
		"        pass\n" +
		"from gi.repository import Gtk, WebKit2"
	IndicatorCheck = "import gi\n" +
		"for name in ('AyatanaAppIndicator3', 'AppIndicator3'):\n" +
		"    try:\n" +
		"        gi.require_version(name, '0.1'); __import__('gi.repository.' + name); break\n" +
		"    except (ValueError, ImportError):\n" +
		"        pass\n" +
		"else:\n" +
		"    raise SystemExit(1)"
)

const lsregister = "/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/" +
	"Support/lsregister"

// Desktop sets up and opens the app; tests replace its parts.
type Desktop struct {
	GOOS   string
	Root   string // the installation: app/ (icons, the Linux shell) and desktop/ (Windows app)
	Run    service.Runner
	Out    io.Writer
	Getenv func(string) string
	Home   func() string
	// Spawn starts a program detached from this one; false when it could not start.
	Spawn      func(args []string) bool
	HasDesktop func() bool
	IsAdmin    func() bool // Windows: setup runs elevated
	UID        int
	// PackageDir is platform.PackageDir(); macOS takes its packaged app from there.
	PackageDir string
	// Candidates are the Pythons tried for the Linux shell, in order.
	Candidates []string
}

// New acts on this machine, for the installation at root.
func New(root string) *Desktop {
	candidates := []string{"/usr/bin/python3"}
	for minor := 20; minor > 7; minor-- {
		candidates = append(candidates, fmt.Sprintf("/usr/bin/python3.%d", minor))
	}
	return &Desktop{
		GOOS: runtime.GOOS, Root: root, Run: service.Exec, Out: os.Stdout, Getenv: os.Getenv,
		Home: home, Spawn: Spawn, HasDesktop: func() bool { return HasDesktop(runtime.GOOS, os.Getenv) },
		IsAdmin: winsys.IsAdmin, UID: os.Getuid(), PackageDir: platform.PackageDir(), Candidates: candidates,
	}
}

func home() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}

// HasDesktop reports whether a window can be shown to the person running setup.
func HasDesktop(goos string, getenv func(string) string) bool {
	if goos == "windows" {
		return true
	}
	if getenv("SSH_CONNECTION") != "" && getenv("DISPLAY") == "" {
		return false
	}
	if goos == "darwin" {
		return true
	}
	return getenv("DISPLAY") != "" || getenv("WAYLAND_DISPLAY") != ""
}

func (d *Desktop) macOS() bool   { return d.GOOS == "darwin" }
func (d *Desktop) windows() bool { return d.GOOS == "windows" }

func (d *Desktop) say(format string, args ...any) {
	fmt.Fprintf(d.Out, "-> "+format+"\n", args...)
}

func (d *Desktop) printf(format string, args ...any) { fmt.Fprintf(d.Out, format+"\n", args...) }

// quiet runs a helper whose failure only costs a detail (icon, cache, an old process).
func (d *Desktop) quiet(args ...string) bool {
	r, err := d.Run(service.Cmd{Args: args, Capture: true, NoWindow: d.windows()})
	return err == nil && r.Code == 0
}

func isFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// Packaged reports whether uni-vpn came from the .pkg, .deb or .rpm in packageDir, which
// bring their own app entry: the package holds the Python program or the Go core.
func Packaged(packageDir string) bool {
	if packageDir == "" {
		return false
	}
	return isFile(filepath.Join(packageDir, "app", "uni_vpn", "__init__.py")) ||
		isFile(filepath.Join(packageDir, "app", "bin", "uni-vpn-core"))
}

// SourceDir holds the icons and the Linux shell.
func (d *Desktop) SourceDir() string { return filepath.Join(d.Root, "app") }

func (d *Desktop) linuxScript() string {
	return filepath.Join(d.SourceDir(), "linux", "uni-vpn-app.py")
}

// --- where things go -------------------------------------------------------

func (d *Desktop) dataHome() string {
	if v := d.Getenv("XDG_DATA_HOME"); v != "" {
		return v
	}
	return filepath.Join(d.Home(), ".local", "share")
}

func (d *Desktop) startMenuDir() string {
	base := d.Getenv("APPDATA")
	if base == "" {
		base = d.Home()
	}
	return filepath.Join(base, "Microsoft", "Windows", "Start Menu", "Programs")
}

// AppPath is what the start menu, Launchpad or app grid shows.
func (d *Desktop) AppPath() string {
	if d.windows() {
		return filepath.Join(d.startMenuDir(), AppName+".lnk")
	}
	if d.macOS() {
		if packaged := d.PackagedApp(); packaged != "" {
			return packaged
		}
		return filepath.Join(d.Home(), "Applications", AppName+".app")
	}
	return filepath.Join(d.dataHome(), "applications", AppID+".desktop")
}

// PackagedApp is the app the macOS .pkg put in /Applications, ready built; "" otherwise.
func (d *Desktop) PackagedApp() string {
	if !d.macOS() || !Packaged(d.PackageDir) {
		return ""
	}
	app := filepath.Dir(filepath.Dir(d.PackageDir))
	if !isFile(filepath.Join(app, "Contents", "MacOS", AppName)) {
		return ""
	}
	return app
}

// LegacyPaths are app entries of earlier versions, which opened the browser.
func (d *Desktop) LegacyPaths() []string {
	if d.windows() {
		return []string{filepath.Join(d.startMenuDir(), AppName+".url")}
	}
	if d.macOS() {
		// The one setup built before the .pkg brought its own.
		if d.PackagedApp() != "" {
			return []string{filepath.Join(d.Home(), "Applications", AppName+".app")}
		}
		return nil
	}
	return []string{filepath.Join(d.dataHome(), "applications", "uni-vpn.desktop")}
}

// LoginPath starts the menu bar or tray icon at login, without a window.
func (d *Desktop) LoginPath() string {
	if d.windows() {
		return filepath.Join(d.startMenuDir(), "Startup", AppName+".lnk")
	}
	if d.macOS() {
		return filepath.Join(d.Home(), "Library", "LaunchAgents", LoginLabel+".plist")
	}
	base := d.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(d.Home(), ".config")
	}
	return filepath.Join(base, "autostart", AppID+".desktop")
}

// WindowsBuildDir holds the Windows app, next to the program in Program Files: what runs as
// the user must not be changeable by other programs of the user either.
func (d *Desktop) WindowsBuildDir() string { return filepath.Join(d.Root, "desktop") }

func (d *Desktop) WindowsExe() string { return filepath.Join(d.WindowsBuildDir(), AppName+".exe") }

func (d *Desktop) LinuxIconPath() string {
	return filepath.Join(d.dataHome(), "icons", "hicolor", "scalable", "apps", AppID+".svg")
}

// --- install ---------------------------------------------------------------

// Install sets up the app and its login item. True if the native app is in place, false if
// the app entry only opens the browser. created records each file it makes.
func (d *Desktop) Install(port int, dry bool, created func(string)) bool {
	switch {
	case d.macOS():
		return d.installMacOS(port, dry, created)
	case d.windows():
		return d.installWindows(port, dry, created)
	}
	return d.installLinux(port, dry, created)
}

// browserEntry is the fallback: an app entry that opens the page in the default browser.
func (d *Desktop) browserEntry(port int, dry bool, created func(string)) bool {
	link := fmt.Sprintf("http://127.0.0.1:%d/", port)
	path := d.AppPath()
	if !d.macOS() {
		path = d.LegacyPaths()[0]
	}
	if dry {
		d.say("would add %s to open %s", path, link)
		return false
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		d.printf("   App entry could not be created: %s", err)
		return false
	}
	var err error
	switch {
	case d.windows():
		err = os.WriteFile(path, []byte("[InternetShortcut]\r\nURL="+link+"\r\n"), 0o666)
	case d.macOS():
		os.RemoveAll(path)
		r, rerr := d.Run(service.Cmd{Args: []string{"osacompile", "-o", path, "-e", `open location "` + link + `"`}, Capture: true})
		if rerr != nil || r.Code != 0 {
			detail := strings.TrimSpace(r.Stderr)
			if rerr != nil {
				detail = rerr.Error()
			}
			d.printf("   App entry could not be created: %s", detail)
			return false
		}
	default:
		err = os.WriteFile(path, []byte("[Desktop Entry]\nType=Application\nName=Uni VPN\nComment=University VPN for selected websites\n"+
			"Exec=xdg-open "+link+"\nIcon=network-vpn\nCategories=Network;\nTerminal=false\n"), 0o666)
	}
	if err != nil {
		d.printf("   App entry could not be created: %s", err)
		return false
	}
	created(path)
	d.say("App entry created: %s", path)
	return false
}

// macOS ----------------------------------------------------------------------

// LoginPlist is the LaunchAgent that starts the menu bar item, as plistlib.dump writes it.
func LoginPlist(executable string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + LoginLabel + `</string>
	<key>LimitLoadToSessionType</key>
	<string>Aqua</string>
	<key>ProcessType</key>
	<string>Interactive</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + winsys.XMLEscape(executable) + `</string>
		<string>--hidden</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
</dict>
</plist>
`
}

// builtApp is a native app an earlier setup built in ~/Applications (not the browser entry,
// whose program is called "applet"), made for this port.
func (d *Desktop) builtApp(port int) string {
	app := filepath.Join(d.Home(), "Applications", AppName+".app")
	if !isFile(filepath.Join(app, "Contents", "MacOS", AppName)) {
		return ""
	}
	info, err := os.ReadFile(filepath.Join(app, "Contents", "Info.plist"))
	want := "<key>UniVPNPort</key>\n\t<string>" + strconv.Itoa(port) + "</string>"
	if err != nil || !bytes.Contains(info, []byte(want)) {
		return ""
	}
	return app
}

func (d *Desktop) installMacOS(port int, dry bool, created func(string)) bool {
	app := d.PackagedApp()
	if app == "" {
		app = d.builtApp(port)
	}
	if app == "" {
		if !dry {
			d.printf("   The app window is not installed, the app opens in the browser for now")
		}
		return d.browserEntry(port, dry, created)
	}
	if dry {
		d.say("would start %s at login (%s)", app, d.LoginPath())
		return true
	}
	d.stopRunning()
	if d.PackagedApp() != "" {
		for _, legacy := range d.LegacyPaths() {
			os.RemoveAll(legacy)
		}
	}
	return d.registerMacOS(app, created)
}

func (d *Desktop) registerMacOS(app string, created func(string)) bool {
	login := d.LoginPath()
	d.quiet(lsregister, "-f", app)
	d.say("App ready: %s", app)
	if err := os.MkdirAll(filepath.Dir(login), 0o777); err != nil {
		d.printf("   The menu bar item could not be set up: %s", err)
		return true
	}
	if err := os.WriteFile(login, []byte(LoginPlist(filepath.Join(app, "Contents", "MacOS", AppName))), 0o666); err != nil {
		d.printf("   The menu bar item could not be set up: %s", err)
		return true
	}
	created(login)
	domain := fmt.Sprintf("gui/%d", d.UID)
	d.quiet("launchctl", "bootout", domain, login)
	d.quiet("launchctl", "bootstrap", domain, login)
	d.say("Menu bar item starts at login")
	return true
}

func (d *Desktop) stopRunning() {
	switch {
	case d.macOS():
		d.quiet("launchctl", "bootout", fmt.Sprintf("gui/%d", d.UID), d.LoginPath())
		d.quiet("pkill", "-x", AppName)
	case d.windows():
		d.quiet("taskkill", "/F", "/IM", AppName+".exe")
	default:
		d.quiet("pkill", "-f", d.linuxScript())
	}
}

// Windows --------------------------------------------------------------------

// PSQuote quotes a value for PowerShell.
func PSQuote(text string) string { return "'" + strings.ReplaceAll(text, "'", "''") + "'" }

// ShortcutScript creates a .lnk with PowerShell.
func ShortcutScript(path, target, arguments, icon string) string {
	return fmt.Sprintf("$s = (New-Object -ComObject WScript.Shell).CreateShortcut(%s); "+
		"$s.TargetPath = %s; $s.Arguments = %s; "+
		"$s.WorkingDirectory = %s; $s.IconLocation = %s; "+
		"$s.Description = %s; $s.Save()",
		PSQuote(path), PSQuote(target), PSQuote(arguments), PSQuote(winDir(target)), PSQuote(icon+",0"), PSQuote(AppName))
}

// winDir is the folder of a Windows path, also when the tests run elsewhere.
func winDir(path string) string {
	i := strings.LastIndexAny(path, `\/`)
	if i <= 0 {
		return filepath.Dir(path)
	}
	if i == 2 && path[1] == ':' {
		return path[:3]
	}
	return path[:i]
}

func (d *Desktop) installWindows(port int, dry bool, created func(string)) bool {
	exe := d.WindowsExe()
	// Shipped by the Windows installer (PrebuiltMarker) or the release files.
	ready := isFile(exe)
	if dry {
		if !ready {
			return d.browserEntry(port, dry, created)
		}
		d.say("would add %s and start it at login (%s)", d.AppPath(), d.LoginPath())
		return true
	}
	if !ready {
		d.printf("   The app window is not installed, the app opens in the browser for now")
		return d.browserEntry(port, dry, created)
	}
	// Restart the running copy.
	d.stopRunning()
	return d.windowsEntries(port, created)
}

func (d *Desktop) windowsEntries(port int, created func(string)) bool {
	exe := d.WindowsExe()
	for _, legacy := range d.LegacyPaths() {
		os.Remove(legacy)
	}
	for _, entry := range [][2]string{{d.AppPath(), fmt.Sprintf("--port %d", port)},
		{d.LoginPath(), fmt.Sprintf("--port %d --hidden", port)}} {
		path := entry[0]
		os.MkdirAll(filepath.Dir(path), 0o777)
		script := ShortcutScript(path, exe, entry[1], exe)
		if !d.quiet("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script) {
			d.printf("   Shortcut %s could not be created", path)
			continue
		}
		created(path)
	}
	d.say("App entry in the start menu, icon in the notification area starts at login")
	if exists(d.LoginPath()) {
		// Back in the notification area right away, as the signed-in user (setup runs elevated).
		d.Spawn([]string{"explorer.exe", d.LoginPath()})
	}
	return true
}

// Linux ----------------------------------------------------------------------

// GTKPython is a Python with PyGObject, GTK 3 and WebKitGTK: the distribution's own. "" when
// there is none.
func (d *Desktop) GTKPython() string {
	seen := map[string]bool{}
	for _, candidate := range d.Candidates {
		if seen[candidate] {
			continue
		}
		seen[candidate] = true
		st, err := os.Stat(candidate)
		if err != nil || st.IsDir() || st.Mode().Perm()&0o111 == 0 {
			continue
		}
		r, err := d.Run(service.Cmd{Args: []string{candidate, "-c", GTKCheck}, Capture: true, Timeout: 20 * time.Second})
		if err == nil && r.Code == 0 {
			return candidate
		}
	}
	return ""
}

// HasIndicator reports whether python can show a panel icon.
func (d *Desktop) HasIndicator(python string) bool {
	r, err := d.Run(service.Cmd{Args: []string{python, "-c", IndicatorCheck}, Capture: true, Timeout: 20 * time.Second})
	return err == nil && r.Code == 0
}

// DesktopQuote quotes an argument for Exec= in a desktop entry.
func DesktopQuote(text string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", "$", `\$`).Replace(text)
	return `"` + strings.ReplaceAll(escaped, "%", "%%") + `"`
}

// DesktopEntry is the app entry (or with hidden the login item) for the GTK shell.
func (d *Desktop) DesktopEntry(python string, port int, hidden bool) string {
	command := fmt.Sprintf("%s %s --port %d", DesktopQuote(python), DesktopQuote(d.linuxScript()), port)
	if hidden {
		command += " --hidden"
	}
	lines := []string{"[Desktop Entry]", "Type=Application", "Name=" + AppName, "Comment=University VPN for selected websites",
		"Exec=" + command, "Icon=" + AppID, "StartupWMClass=" + AppID, "Categories=Network;",
		"Terminal=false", "StartupNotify=true"}
	if hidden {
		lines = append(lines, "NoDisplay=true", "X-GNOME-Autostart-enabled=true")
	}
	return strings.Join(lines, "\n") + "\n"
}

func (d *Desktop) linuxPython() string {
	if !isFile(d.linuxScript()) {
		return ""
	}
	return d.GTKPython()
}

func (d *Desktop) installLinux(port int, dry bool, created func(string)) bool {
	python := d.linuxPython()
	if python == "" {
		if !dry {
			d.printf("   WebKitGTK is missing, the app opens in the browser for now")
		}
		return d.browserEntry(port, dry, created)
	}
	if dry {
		d.say("would add %s to open the app with %s", d.AppPath(), python)
		return true
	}
	icon := d.LinuxIconPath()
	if data, err := os.ReadFile(filepath.Join(d.SourceDir(), "icon.svg")); err == nil {
		if os.MkdirAll(filepath.Dir(icon), 0o777) == nil && os.WriteFile(icon, data, 0o666) == nil {
			created(icon)
		}
	}
	for _, legacy := range d.LegacyPaths() {
		os.Remove(legacy)
	}
	path := d.AppPath()
	err := os.MkdirAll(filepath.Dir(path), 0o777)
	if err == nil {
		err = os.WriteFile(path, []byte(d.DesktopEntry(python, port, false)), 0o666)
	}
	if err != nil {
		d.printf("   App entry could not be created: %s", err)
		return false
	}
	created(path)
	d.say("App entry created: %s", path)
	d.quiet("update-desktop-database", filepath.Dir(path))
	d.stopRunning()
	if d.HasIndicator(python) {
		login := d.LoginPath()
		os.MkdirAll(filepath.Dir(login), 0o777)
		if os.WriteFile(login, []byte(d.DesktopEntry(python, port, true)), 0o666) == nil {
			created(login)
		}
		d.Spawn([]string{python, d.linuxScript(), "--port", strconv.Itoa(port), "--hidden"})
		d.say("Panel icon starts at login")
	}
	return true
}

// --- open --------------------------------------------------------------------

// AppCommand shows the app window with a page: "", "#settings", or "&user=ab123". nil when
// there is no app.
func (d *Desktop) AppCommand(port int, page string) []string {
	if d.macOS() {
		if !exists(filepath.Join(d.AppPath(), "Contents", "MacOS", AppName)) {
			return nil
		}
		var link string
		if page == "#settings" {
			link = URLScheme + "://settings"
		} else {
			link = URLScheme + "://open"
			if strings.HasPrefix(page, "&") {
				link += "?" + strings.TrimLeft(page, "&")
			}
		}
		return []string{"open", link}
	}
	if d.windows() {
		exe := d.WindowsExe()
		if !exists(exe) {
			return nil
		}
		command := []string{exe, "--port", strconv.Itoa(port)}
		if page != "" {
			command = append(command, "--page", page)
		}
		return command
	}
	if !exists(d.AppPath()) {
		return nil
	}
	python := d.linuxPython()
	if python == "" {
		return nil
	}
	command := []string{python, d.linuxScript(), "--port", strconv.Itoa(port)}
	if page != "" {
		command = append(command, "--page", page)
	}
	return command
}

// OpenApp shows the app window. False when there is no app or no desktop; then the browser
// is the way.
func (d *Desktop) OpenApp(port int, page string) bool {
	if !d.HasDesktop() {
		return false
	}
	command := d.AppCommand(port, page)
	if command == nil {
		return false
	}
	if d.windows() && d.IsAdmin() {
		// The app must not run elevated: Explorer starts the shortcut as the signed-in user.
		// It cannot pass the page along, the assistant prefills less.
		if !exists(d.AppPath()) {
			return false
		}
		return d.Spawn([]string{"explorer.exe", d.AppPath()})
	}
	return d.Spawn(command)
}

// Pair is one value of the setup assistant's prefill, in order.
type Pair struct{ Key, Value string }

// Query encodes the pairs with a value like urllib.parse.urlencode.
func Query(pairs []Pair) string {
	var parts []string
	for _, p := range pairs {
		if p.Value != "" {
			parts = append(parts, url.QueryEscape(p.Key)+"="+url.QueryEscape(p.Value))
		}
	}
	return strings.Join(parts, "&")
}

// PageFor turns the setup assistant's prefill (user, university) into a page for OpenApp.
func PageFor(pairs []Pair) string {
	if text := Query(pairs); text != "" {
		return "&" + text
	}
	return ""
}

// Uninstall stops the running app; the files themselves are in the installed-files list.
func (d *Desktop) Uninstall() {
	d.stopRunning()
	if d.windows() {
		os.RemoveAll(d.WindowsBuildDir())
	}
}
