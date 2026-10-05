// Package sysproxy registers the proxy rule with the system: GNOME (gsettings) or KDE on Linux,
// networksetup on macOS, the registry on Windows. It mirrors uni_vpn/sysproxy.py.
//
// Chrome and Firefox read the system's "automatic proxy configuration" setting and fetch the
// PAC file from the daemon. The previous state is backed up and restored on removal; the
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

func (s *System) services() ([]string, error) {
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
		if line != "" && !strings.HasPrefix(line, "*") { // * = disabled service
			names = append(names, line)
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
		for _, name := range names {
			if err := s.set("networksetup", "-setautoproxyurl", name, url); err != nil {
				return err
			}
		}
		return nil
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
		for _, e := range saved.Mac.Services {
			var err error
			if e.Enabled && e.URL != "" {
				err = s.set("networksetup", "-setautoproxyurl", e.Name, e.URL)
			} else {
				err = s.set("networksetup", "-setautoproxystate", e.Name, "off")
			}
			if err != nil {
				return err
			}
		}
		return nil
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
// Firefox reload the PAC. Errors are ignored.
func (s *System) Refresh(httpPort int) {
	if s.macOS() {
		return // networksetup requires admin rights; the user restarts the browser
	}
	url := PacURLVersion(httpPort, s.Now().Unix())
	if s.windows() {
		if now, err := s.WinGet(); err == nil && IsOurs(now, httpPort) {
			_ = s.WinSet(url)
		}
		return
	}
	if mode, _ := s.gsettingsGet("mode"); mode == "auto" {
		if now, _ := s.gsettingsGet("autoconfig-url"); IsOurs(now, httpPort) {
			if _, err := s.run("gsettings", "set", Schema, "autoconfig-url", url); err != nil {
				return
			}
		}
	}
	if kde := s.kdeGet(); kde != nil && IsOurs(kde.URL, httpPort) {
		_ = s.kdeSet(KDEPAC, url, true)
	}
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
// PAC. Returns "ok", "replaced" (a foreign setting was replaced), "unavailable" or "failed";
// the error is for a backup that could not be written. backup "" means BackupPath().
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
	}
	if err := s.Apply(PacURL(httpPort)); err != nil {
		return "failed", nil
	}
	if s.isForeign(before, httpPort) {
		return "replaced", nil
	}
	return "ok", nil
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

// Uninstall restores the backup. Returns "restored", "unset" (no backup, so uni-vpn did not
// set it) or "failed" (the backup is kept); the error is for a backup that could not be removed.
func (s *System) Uninstall(backup string) (string, error) {
	if backup == "" {
		backup = BackupPath()
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
func Install(httpPort int, backup string) (string, error) { return Default().Install(httpPort, backup) }
func Uninstall(backup string) (string, error)             { return Default().Uninstall(backup) }
func State(httpPort int) string                           { return Default().State(httpPort) }
