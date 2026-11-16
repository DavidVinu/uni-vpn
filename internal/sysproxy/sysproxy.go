// Package sysproxy registers the proxy rule with the system: GNOME (gsettings) or KDE on Linux,
// networksetup on macOS, the registry on Windows. It mirrors uni_vpn/sysproxy.py.
//
// Chrome and Firefox read the system's "automatic proxy configuration" setting and fetch the
// PAC file from the daemon; Chrome on Xfce and LXQt reads it from the session files instead
// (session.go). The previous state is backed up and restored on removal; the
// backup file is shared with the Python core, so either core restores what the other saved.
package sysproxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/DavidVinu/uni-vpn/internal/platform"
	"github.com/DavidVinu/uni-vpn/internal/winsys"
)

const (
	Schema   = "org.gnome.system.proxy"
	KDEGroup = "Proxy Settings"
	KDEPAC   = "2"
	Timeout  = 20 * time.Second
)

var kdeKeys = [2]string{"ProxyType", "Proxy Config Script"}

// Result is what a command left behind, like subprocess.CompletedProcess.
type Result struct {
	Code           int
	Stdout, Stderr string
}

// Runner runs a command and captures its output. A non-zero exit is a Result, not an error;
// the error is for commands that could not run or timed out (Python's OSError/SubprocessError).
type Runner func(args []string, timeout time.Duration) (Result, error)

// Exec is the real Runner.
func Exec(args []string, timeout time.Duration) (Result, error) {
	ctx := context.Background()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	platform.HideWindow(cmd)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	if ctx.Err() != nil {
		return Result{}, fmt.Errorf("%s: timed out after %s", args[0], timeout)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return Result{exitErr.ExitCode(), out.String(), errOut.String()}, nil
	}
	if err != nil {
		return Result{}, err
	}
	return Result{0, out.String(), errOut.String()}, nil
}

// CommandError is a setting command that exited non-zero (Python's CalledProcessError).
type CommandError struct {
	Args   []string
	Result Result
}

func (e *CommandError) Error() string {
	return fmt.Sprintf("Command '%s' returned non-zero exit status %d.", strings.Join(e.Args, " "), e.Result.Code)
}

// System is the environment the functions act on; tests replace its parts.
type System struct {
	GOOS       string
	Run        Runner
	Getenv     func(string) string
	FindBinary func(name string) string // "" when missing
	WinGet     func() (string, error)   // AutoConfigURL
	WinSet     func(url string) error
	Now        func() time.Time
	// Elevate runs setting commands with an administrator's name and password, which macOS
	// asks for: a standard account may not change the network settings. Nil where nobody
	// asked for the change (the core's own looks at the rule).
	Elevate func(cmds [][]string) error
}

// Default acts on this machine.
func Default() *System {
	return &System{
		GOOS:       runtime.GOOS,
		Run:        Exec,
		Getenv:     os.Getenv,
		FindBinary: func(name string) string { return platform.FindBinary(name, "") },
		WinGet:     winsys.ProxyGet,
		WinSet:     winsys.ProxySet,
		Now:        time.Now,
	}
}

func (s *System) macOS() bool   { return s.GOOS == "darwin" }
func (s *System) windows() bool { return s.GOOS == "windows" }

func PacURL(httpPort int) string { return fmt.Sprintf("http://127.0.0.1:%d/proxy.pac", httpPort) }

// PacURLVersion carries a version so browsers notice a change and reload the PAC.
func PacURLVersion(httpPort int, version int64) string {
	return PacURL(httpPort) + "?v=" + strconv.FormatInt(version, 10)
}

func IsOurs(url string, httpPort int) bool {
	base, _, _ := strings.Cut(url, "?")
	return url != "" && base == PacURL(httpPort)
}

func BackupPath() string { return filepath.Join(platform.ConfigDir(), "proxy-backup.json") }

func (s *System) run(args ...string) (Result, error) { return s.Run(args, Timeout) }

// adminNeeded: networksetup refused a standard account ("** Error: Command requires admin
// privileges.", exit status 14).
func adminNeeded(err error) bool {
	var ce *CommandError
	return errors.As(err, &ce) && (ce.Result.Code == 14 ||
		strings.Contains(ce.Result.Stdout+ce.Result.Stderr, "requires admin privileges"))
}

// macSet runs networksetup commands in order, past one that fails; the error is the first.
// Once networksetup refuses a standard account, the rest go to Elevate at once (one password),
// where it is set.
func (s *System) macSet(cmds [][]string) error {
	var first error
	for i, c := range cmds {
		err := s.set(c...)
		if err != nil && adminNeeded(err) && s.Elevate != nil {
			if err := s.Elevate(cmds[i:]); err != nil && first == nil {
				first = err
			}
			return first
		}
		if first == nil {
			first = err
		}
	}
	return first
}

// macAllOurs: every service that has a rule on has ours.
func macAllOurs(now *Snapshot, httpPort int) bool {
	if now == nil || now.Mac == nil {
		return false
	}
	enabled := 0
	for _, svc := range now.Mac.Services {
		if svc.Enabled {
			if !IsOurs(svc.URL, httpPort) {
				return false
			}
			enabled++
		}
	}
	return enabled > 0
}

// set runs a command that changes the setting: an error when it fails.
func (s *System) set(args ...string) error {
	r, err := s.run(args...)
	if err != nil {
		return err
	}
	if r.Code != 0 {
		return &CommandError{args, r}
	}
	return nil
}

func (s *System) gsettingsGet(key string) (string, bool) {
	r, err := s.run("gsettings", "get", Schema, key)
	if err != nil || r.Code != 0 {
		return "", false
	}
	return strings.Trim(strings.TrimSpace(r.Stdout), "'"), true
}

// splitLines splits like Python's str.splitlines for the line endings tools print.
func splitLines(s string) []string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// services: the enabled network services of macOS.
func (s *System) services() ([]string, error) { return s.listServices(false) }

// listServices: the network services, with the disabled ones too if disabled.
func (s *System) listServices(disabled bool) ([]string, error) {
	r, err := s.run("networksetup", "-listallnetworkservices")
	if err != nil {
		return nil, err
	}
	var names []string
	lines := splitLines(r.Stdout)
	if len(lines) > 0 {
		lines = lines[1:]
	}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		name, off := strings.CutPrefix(line, "*") // * = disabled service
		if name != "" && (disabled || !off) {
			names = append(names, name)
		}
	}
	return names, nil
}

// kdeTools finds kreadconfig/kwriteconfig of Plasma 6 or 5, only on a KDE desktop unless
// desktopOnly is false (restoring a backup, for example over SSH).
func (s *System) kdeTools(desktopOnly bool) (read, write string, ok bool) {
	if desktopOnly && !strings.Contains(strings.ToUpper(s.Getenv("XDG_CURRENT_DESKTOP")), "KDE") {
		return "", "", false
	}
	for _, version := range []string{"6", "5"} {
		read, write = s.FindBinary("kreadconfig"+version), s.FindBinary("kwriteconfig"+version)
		if read != "" && write != "" {
			return read, write, true
		}
	}
	return "", "", false
}

func (s *System) kdeGet() *KDE {
	read, _, ok := s.kdeTools(true)
	if !ok {
		return nil
	}
	var values [2]string
	for i, key := range kdeKeys {
		r, err := s.run(read, "--file", "kioslaverc", "--group", KDEGroup, "--key", key)
		if err != nil {
			return nil
		}
		if r.Code == 0 {
			values[i] = strings.TrimSpace(r.Stdout)
		}
	}
	if values[0] == "" {
		values[0] = "0"
	}
	return &KDE{Type: values[0], URL: values[1]}
}

func (s *System) kdeSet(proxyType, url string, desktopOnly bool) error {
	_, write, ok := s.kdeTools(desktopOnly)
	if !ok {
		if !desktopOnly {
			return errors.New("kwriteconfig6 or kwriteconfig5 not found")
		}
		return nil
	}
	if err := s.set(write, "--file", "kioslaverc", "--group", KDEGroup, "--key", "Proxy Config Script", url); err != nil {
		return err
	}
	if err := s.set(write, "--file", "kioslaverc", "--group", KDEGroup, "--key", "ProxyType", proxyType); err != nil {
		return err
	}
	// Running KDE programs re-read kioslaverc on this signal; Chrome watches the file itself.
	_, _ = s.run("dbus-send", "--type=signal", "/KIO/Scheduler", "org.kde.KIO.Scheduler.reparseSlaveConfiguration",
		"string:")
	return nil
}

var (
	reURL     = regexp.MustCompile(`(?m)^URL:\s*(.*)$`)
	reEnabled = regexp.MustCompile(`(?m)^Enabled:\s*(\w+)`)
)

// Current returns the current setting, or nil when the system has no known proxy management.
func (s *System) Current() *Snapshot {
	if s.windows() {
		url, err := s.WinGet()
		if err != nil {
			return nil
		}
		return &Snapshot{Windows: &Windows{URL: url}}
	}
	if s.macOS() {
		names, err := s.services()
		if err != nil {
			return nil
		}
		mac := &Mac{Services: []Service{}}
		for _, name := range names {
			r, err := s.run("networksetup", "-getautoproxyurl", name)
			if err != nil {
				return nil
			}
			url := ""
			if m := reURL.FindStringSubmatch(r.Stdout); m != nil {
				url = strings.TrimSpace(m[1])
			}
			if url == "(null)" {
				url = ""
			}
			m := reEnabled.FindStringSubmatch(r.Stdout)
			mac.Services = append(mac.Services, Service{Name: name, URL: url,
				Enabled: m != nil && strings.ToLower(m[1]) == "yes"})
		}
		return &Snapshot{Mac: mac}
	}
	var snap Snapshot
	if mode, ok := s.gsettingsGet("mode"); ok {
		url, _ := s.gsettingsGet("autoconfig-url")
		snap.GNOME = &GNOME{Mode: mode, URL: url}
	}
	snap.KDE = s.kdeGet()
	if snap.GNOME == nil && snap.KDE == nil {
		return nil
	}
	return &snap
}

// Apply points the system at url.
func (s *System) Apply(url string) error {
	if s.windows() {
		return s.WinSet(url)
	}
	if s.macOS() {
		names, err := s.services()
		if err != nil {
			return err
		}
		var cmds [][]string
		for _, name := range names {
			cmds = append(cmds, []string{"networksetup", "-setautoproxyurl", name, url})
		}
		return s.macSet(cmds)
	}
	if _, ok := s.gsettingsGet("mode"); ok {
		if err := s.set("gsettings", "set", Schema, "autoconfig-url", url); err != nil {
			return err
		}
		if err := s.set("gsettings", "set", Schema, "mode", "auto"); err != nil {
			return err
		}
	}
	return s.kdeSet(KDEPAC, url, true)
}

// Restore puts a saved setting back.
func (s *System) Restore(saved *Snapshot) error {
	if s.windows() {
		url := ""
		if saved.Windows != nil {
			url = saved.Windows.URL
		}
		return s.WinSet(url)
	}
	if s.macOS() {
		if saved.Mac == nil {
			return nil
		}
		// A service gone since (a VPN app removed, a service renamed) has nothing to put back.
		// None of them keeps the rule on the others: uninstalling would fail at it every time.
		names, err := s.listServices(true)
		known := err == nil && len(names) > 0 // else each is tried
		var cmds [][]string
		for _, e := range saved.Mac.Services {
			if known && !slices.Contains(names, e.Name) {
				continue
			}
			if e.URL != "" {
				cmds = append(cmds, []string{"networksetup", "-setautoproxyurl", e.Name, e.URL})
			}
			if !e.Enabled || e.URL == "" {
				// Off, or its own address switched off, which Install wrote ours over (setting
				// an address also switches the rule on).
				cmds = append(cmds, []string{"networksetup", "-setautoproxystate", e.Name, "off"})
			}
		}
		return s.macSet(cmds)
	}
	if g := saved.GNOME; g != nil {
		mode := g.Mode
		if mode == "" {
			mode = "none"
		}
		if err := s.set("gsettings", "set", Schema, "mode", mode); err != nil {
			return err
		}
		if err := s.set("gsettings", "set", Schema, "autoconfig-url", g.URL); err != nil {
			return err
		}
	}
	if k := saved.KDE; k != nil {
		typ := k.Type
		if typ == "" {
			typ = "0"
		}
		return s.kdeSet(typ, k.URL, false)
	}
	return nil
}

// Refresh sets a new URL version: GNOME, KDE and Windows announce the change, Chrome and
// Firefox reload the PAC. On macOS a new address is the only way: the system keeps the PAC it
// loaded per address until the user signs out, for Safari too. Errors are ignored.
func (s *System) Refresh(httpPort int) {
	version := s.Now().Unix()
	// A new address each time, also for more changes within one second: past the one set now.
	fresh := func(now string) string { return PacURLVersion(httpPort, past(version, now)) }
	if s.windows() {
		if now, err := s.WinGet(); err == nil && IsOurs(now, httpPort) {
			_ = s.WinSet(fresh(now))
		}
		return
	}
	if s.macOS() {
		if snap := s.Current(); snap != nil && snap.Mac != nil {
			for _, svc := range snap.Mac.Services {
				if svc.Enabled && IsOurs(svc.URL, httpPort) {
					_ = s.set("networksetup", "-setautoproxyurl", svc.Name, fresh(svc.URL))
				}
			}
		}
		return
	}
	if mode, _ := s.gsettingsGet("mode"); mode == "auto" {
		if now, _ := s.gsettingsGet("autoconfig-url"); IsOurs(now, httpPort) {
			if _, err := s.run("gsettings", "set", Schema, "autoconfig-url", fresh(now)); err != nil {
				return
			}
		}
	}
	if kde := s.kdeGet(); kde != nil && kde.Type == KDEPAC && IsOurs(kde.URL, httpPort) {
		_ = s.kdeSet(KDEPAC, fresh(kde.URL), true)
	}
}

// Cover gives our rule to macOS network services added since the install (a dock's Ethernet,
// an iPhone over USB): each service has its own setting, and the browsers follow the one in
// use. Only while another service uses our rule, and only services without an automatic proxy
// that the backup does not know, each backed up first, so that uninstalling turns it off again.
// Our rule switched off (the uni-vpn of another account waits for the port, Withdraw) is
// switched on again, while this account's install has its backup. On Linux the session files
// get our lines while the rule is ours (installed by an earlier version). Errors are ignored.
// backup "" means BackupPath().
func (s *System) Cover(httpPort int, backup string) {
	if s.linux() {
		// Installed before Chrome on Xfce and LXQt got the rule from the session files.
		if s.State(httpPort) == "ok" {
			s.setSession(httpPort)
		}
		return
	}
	if !s.macOS() {
		return
	}
	now := s.Current()
	if now == nil || now.Mac == nil {
		return
	}
	ours := ""
	for _, svc := range now.Mac.Services {
		if svc.Enabled && IsOurs(svc.URL, httpPort) {
			ours = svc.URL
			break
		}
	}
	if ours == "" {
		// Switched off by the uni-vpn of another account of this Mac, which waits for the status
		// port this one holds (Withdraw): the setting is the computer's, so it is on again here.
		for _, svc := range now.Mac.Services {
			if !svc.Enabled && IsOurs(svc.URL, httpPort) {
				if _, err := os.Stat(backupOr(backup)); err == nil {
					_, _ = s.Install(httpPort, backup)
				}
				return
			}
		}
		return
	}
	unset := func(svc Service) bool { return !svc.Enabled && svc.URL == "" }
	for _, name := range backUpNew(backup, now, httpPort, unset) {
		_ = s.set("networksetup", "-setautoproxyurl", name, ours)
	}
}

// backUpNew adds the macOS services in now that the backup does not know and that keep
// passes, as they are (off where they use our rule), so that uninstalling restores them too.
// It returns their names; none when the backup could not be read or written.
func backUpNew(backup string, now *Snapshot, httpPort int, keep func(Service) bool) []string {
	if backup == "" {
		backup = BackupPath()
	}
	data, err := os.ReadFile(backup)
	if err != nil {
		return nil
	}
	var saved Snapshot
	if err := saved.UnmarshalJSON(data); err != nil || saved.Mac == nil || now.Mac == nil {
		return nil
	}
	known := map[string]bool{}
	for _, svc := range saved.Mac.Services {
		known[svc.Name] = true
	}
	var added []string
	for _, svc := range now.Mac.Services {
		if known[svc.Name] || !keep(svc) {
			continue
		}
		if IsOurs(svc.URL, httpPort) {
			svc = Service{Name: svc.Name}
		}
		saved.Mac.Services = append(saved.Mac.Services, svc)
		added = append(added, svc.Name)
	}
	if len(added) == 0 {
		return nil
	}
	data, _ = saved.MarshalJSON()
	if err := os.WriteFile(backup, data, 0o666); err != nil {
		return nil
	}
	return added
}

// past: version, or past the version in url when that is as new or newer.
func past(version int64, url string) int64 {
	if _, set, ok := strings.Cut(url, "?v="); ok {
		if n, err := strconv.ParseInt(set, 10, 64); err == nil && n >= version {
			return n + 1
		}
	}
	return version
}

// urls: the proxy rule addresses in a snapshot.
func (snap *Snapshot) urls() []string {
	var urls []string
	if snap.Windows != nil {
		urls = append(urls, snap.Windows.URL)
	}
	if snap.Mac != nil {
		for _, svc := range snap.Mac.Services {
			urls = append(urls, svc.URL)
		}
	}
	if snap.GNOME != nil {
		urls = append(urls, snap.GNOME.URL)
	}
	if snap.KDE != nil {
		urls = append(urls, snap.KDE.URL)
	}
	return urls
}

// Unversioned: the system uses our rule at its plain address, as older installers wrote it,
// so the browsers have the rule of whichever version answered then.
func (s *System) Unversioned(httpPort int) bool {
	plain := PacURL(httpPort)
	if s.macOS() {
		if snap := s.Current(); snap != nil && snap.Mac != nil {
			for _, svc := range snap.Mac.Services {
				if svc.Enabled && svc.URL == plain {
					return true
				}
			}
		}
		return false
	}
	if s.windows() {
		now, err := s.WinGet()
		return err == nil && now == plain
	}
	if mode, _ := s.gsettingsGet("mode"); mode == "auto" {
		if now, _ := s.gsettingsGet("autoconfig-url"); now == plain {
			return true
		}
	}
	kde := s.kdeGet()
	return kde != nil && kde.Type == KDEPAC && kde.URL == plain
}

func (s *System) isForeign(before *Snapshot, httpPort int) bool {
	if s.windows() {
		return before.Windows != nil && before.Windows.URL != "" && !IsOurs(before.Windows.URL, httpPort)
	}
	if s.macOS() {
		if before.Mac == nil {
			return false
		}
		for _, e := range before.Mac.Services {
			if e.Enabled && !IsOurs(e.URL, httpPort) {
				return true
			}
		}
		return false
	}
	foreign := before.GNOME != nil && before.GNOME.Mode != "none" && !IsOurs(before.GNOME.URL, httpPort)
	if k := before.KDE; k != nil && k.Type != "" && k.Type != "0" && !IsOurs(k.URL, httpPort) {
		foreign = true
	}
	return foreign
}

// Install backs up the current setting (unless a backup exists) and points the system at our
// PAC, on Linux also in the session files. Returns "ok", "replaced" (a foreign setting was
// replaced), "unavailable" or "failed"; the error is for a backup that could not be written.
// backup "" means BackupPath().
func (s *System) Install(httpPort int, backup string) (string, error) {
	if backup == "" {
		backup = BackupPath()
	}
	before := s.Current()
	if before == nil {
		return "unavailable", nil
	}
	if _, err := os.Stat(backup); err != nil {
		if err := mkdirLast(filepath.Dir(backup), 0o700); err != nil {
			return "", err
		}
		data, err := before.MarshalJSON()
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(backup, data, 0o666); err != nil {
			return "", err
		}
	} else {
		// Repair, or the installer again: macOS services added since are backed up too.
		backUpNew(backup, before, httpPort, func(Service) bool { return true })
	}
	// A new address: macOS keeps the rule it loaded per address until the user signs out, and
	// the core answering now is the one setup just restarted.
	version := s.Now().Unix()
	for _, url := range before.urls() {
		if IsOurs(url, httpPort) {
			version = past(version, url)
		}
	}
	apply := s
	if s.macOS() && s.Elevate != nil && macAllOurs(before, httpPort) {
		// A standard account where the rule is ours already (an administrator's account set it
		// up): a new address helps only Safari in a session that loaded the old rule, which is
		// not worth an administrator's password.
		quiet := *s
		quiet.Elevate = nil
		apply = &quiet
	}
	if err := apply.Apply(PacURLVersion(httpPort, version)); err != nil && (apply == s || !adminNeeded(err)) {
		return "failed", nil
	}
	if s.linux() {
		s.setSession(httpPort)
	}
	if s.isForeign(before, httpPort) {
		return "replaced", nil
	}
	return "ok", nil
}

// backupOr is backup, or BackupPath() for "".
func backupOr(backup string) string {
	if backup == "" {
		return BackupPath()
	}
	return backup
}

// WithdrawnName is the file in the state folder that says the rule is off while another
// account of this computer holds the status port (the core withdrew it, or setup left it off):
// the core switches it on again once the port is this account's.
const WithdrawnName = "rule-withdrawn"

// Withdraw switches the automatic proxy setting off where it uses our rule: another account of
// this computer holds the status port, so what answers there is not this account's uni-vpn,
// and the browsers would take their rule from it. Install puts the rule back. Reports whether
// it switched anything off. On Linux and Windows the setting is this account's. On macOS it is
// the computer's: the address stays there, switched off, and the uni-vpn that holds the port
// switches it on again (Cover). Errors are ignored.
func (s *System) Withdraw(httpPort int) bool {
	now := s.Current()
	if now == nil {
		return false
	}
	if s.windows() {
		return now.Windows != nil && IsOurs(now.Windows.URL, httpPort) && s.WinSet("") == nil
	}
	off := false
	if s.macOS() {
		for _, svc := range now.Mac.Services {
			if svc.Enabled && IsOurs(svc.URL, httpPort) && s.set("networksetup", "-setautoproxystate", svc.Name, "off") == nil {
				off = true
			}
		}
		return off
	}
	if g := now.GNOME; g != nil && g.Mode == "auto" && IsOurs(g.URL, httpPort) {
		off = s.set("gsettings", "set", Schema, "mode", "none") == nil
	}
	if k := now.KDE; k != nil && k.Type == KDEPAC && IsOurs(k.URL, httpPort) && s.kdeSet("0", k.URL, true) == nil {
		off = true
	}
	// Chrome on Xfce and LXQt keeps the address until the next login: from then on not.
	if s.clearSession() {
		off = true
	}
	return off
}

// mkdirLast creates dir and its parents like Path.mkdir(parents=True, mode=...): only the
// last component gets mode.
func mkdirLast(dir string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o777); err != nil {
		return err
	}
	if err := os.Mkdir(dir, mode); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return nil
}

// Uninstall restores the backup and takes our lines out of the session files. Returns
// "restored", "unset" (no backup, so uni-vpn did not set it) or "failed" (the backup is kept);
// the error is for a backup that could not be removed.
func (s *System) Uninstall(backup string) (string, error) {
	if backup == "" {
		backup = BackupPath()
	}
	if s.linux() {
		s.clearSession()
	}
	data, err := os.ReadFile(backup)
	if err != nil {
		return "unset", nil
	}
	var saved Snapshot
	if err := saved.UnmarshalJSON(data); err != nil {
		return "unset", nil
	}
	if err := s.Restore(&saved); err != nil {
		return "failed", nil
	}
	if err := os.Remove(backup); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return "restored", nil
}

// State is "ok" (our PAC active), "unset", "foreign" (another proxy setting) or "unavailable".
func (s *System) State(httpPort int) string {
	now := s.Current()
	if now == nil {
		return "unavailable"
	}
	if s.windows() {
		url := now.Windows.URL
		if IsOurs(url, httpPort) {
			return "ok"
		}
		if url != "" {
			return "foreign"
		}
		return "unset"
	}
	if s.macOS() {
		enabled := 0
		for _, e := range now.Mac.Services {
			if e.Enabled {
				enabled++
				if IsOurs(e.URL, httpPort) {
					return "ok"
				}
			}
		}
		if enabled > 0 {
			return "foreign"
		}
		return "unset"
	}
	if k := now.KDE; k != nil {
		// On a KDE desktop Chrome and Firefox follow kioslaverc, not GNOME.
		if k.Type == KDEPAC && IsOurs(k.URL, httpPort) {
			return "ok"
		}
		if k.Type == "" || k.Type == "0" {
			return "unset"
		}
		return "foreign"
	}
	if now.GNOME.Mode == "auto" && IsOurs(now.GNOME.URL, httpPort) {
		return "ok"
	}
	if now.GNOME.Mode == "none" {
		return "unset"
	}
	return "foreign"
}

// Package-level shortcuts acting on this machine.

func Current() *Snapshot                                  { return Default().Current() }
func Apply(url string) error                              { return Default().Apply(url) }
func Restore(saved *Snapshot) error                       { return Default().Restore(saved) }
func Refresh(httpPort int)                                { Default().Refresh(httpPort) }
func Unversioned(httpPort int) bool                       { return Default().Unversioned(httpPort) }
func Cover(httpPort int)                                  { Default().Cover(httpPort, "") }
func Withdraw(httpPort int) bool                          { return Default().Withdraw(httpPort) }
func Install(httpPort int, backup string) (string, error) { return Default().Install(httpPort, backup) }
func Uninstall(backup string) (string, error)             { return Default().Uninstall(backup) }
func State(httpPort int) string                           { return Default().State(httpPort) }
