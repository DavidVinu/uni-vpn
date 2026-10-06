// Package service renders, loads and controls the service: systemd --user (Linux), launchd
// (macOS), Task Scheduler (Windows). It mirrors uni_vpn/service.py; file names, labels and
// rendered texts are the same, so either core can replace the other's installation.
package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"

	"github.com/DavidVinu/uni-vpn/internal/platform"
	"github.com/DavidVinu/uni-vpn/internal/winsys"
)

const (
	Label    = "de.davidvinu.uni-vpn"
	UnitName = "uni-vpn"
)

// PassthroughEnvKeys determine config_dir()/state_dir(); otherwise the service does not get them.
var PassthroughEnvKeys = []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME"}

// Error means the service file was written, but systemctl/launchctl/schtasks failed.
// Files lists the files already created.
type Error struct {
	Msg   string
	Files []string
}

func (e *Error) Error() string { return e.Msg }

// Cmd is one command for a Runner.
type Cmd struct {
	Args     []string
	Capture  bool          // collect output; otherwise it goes to the terminal
	Timeout  time.Duration // 0: none
	NoWindow bool          // CREATE_NO_WINDOW on Windows
}

// Result is what a command left behind, like subprocess.CompletedProcess.
type Result struct {
	Code           int
	Stdout, Stderr string
}

// Runner runs a command. A non-zero exit is a Result, not an error; the error is for commands
// that could not run or timed out (Python's OSError/SubprocessError).
type Runner func(Cmd) (Result, error)

// Exec is the real Runner.
func Exec(c Cmd) (Result, error) {
	ctx := context.Background()
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, c.Args[0], c.Args[1:]...)
	if c.NoWindow {
		platform.HideWindow(cmd)
	}
	var out, errOut bytes.Buffer
	if c.Capture {
		cmd.Stdout, cmd.Stderr = &out, &errOut
	} else {
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	}
	err := cmd.Run()
	if ctx.Err() != nil {
		return Result{}, fmt.Errorf("%s: timed out after %s", c.Args[0], c.Timeout)
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

// Manager installs and controls the service; tests replace its parts.
type Manager struct {
	GOOS       string
	Run        Runner
	Out        io.Writer           // messages Python prints
	Sleep      func(time.Duration) // waits between polls
	Getenv     func(string) string
	Binary     string        // the Go binary the service runs; "" = os.Executable()
	BrewPrefix func() string // macOS: Homebrew prefix for PATH, "" when none
	// HTTPPort returns http_port from config.toml; nil or an error means the default 1081.
	HTTPPort func() (int, error)
	// Conhost is System32's conhost.exe on Windows: the task runs the binary in a console
	// that is never shown (winsys.RenderTaskBinary). "" runs the binary directly.
	Conhost string
}

// New acts on this machine.
func New() *Manager {
	return &Manager{
		GOOS:       runtime.GOOS,
		Run:        Exec,
		Out:        os.Stdout,
		Sleep:      time.Sleep,
		Getenv:     os.Getenv,
		BrewPrefix: brewPrefix,
		Conhost:    winsys.Conhost(),
	}
}

func (m *Manager) macOS() bool   { return m.GOOS == "darwin" }
func (m *Manager) windows() bool { return m.GOOS == "windows" }

func brewPrefix() string {
	for _, prefix := range []string{"/opt/homebrew", "/usr/local"} {
		if st, err := os.Stat(filepath.Join(prefix, "bin", "brew")); err == nil && st.Mode().IsRegular() &&
			st.Mode().Perm()&0o111 != 0 {
			return prefix
		}
	}
	return ""
}

func home() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}

// UnitTargetPath is where the service file goes.
func (m *Manager) UnitTargetPath() string {
	if m.windows() {
		// Task Scheduler keeps its own copy; this one records what was registered.
		return filepath.Join(platform.ConfigDir(), "uni-vpn-task.xml")
	}
	if m.macOS() {
		return filepath.Join(home(), "Library", "LaunchAgents", Label+".plist")
	}
	base := m.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(home(), ".config")
	}
	return filepath.Join(base, "systemd", "user", UnitName+".service")
}

func (m *Manager) binary() (string, error) {
	if m.Binary != "" {
		return m.Binary, nil
	}
	return os.Executable()
}

func (m *Manager) printf(format string, args ...any) { fmt.Fprintf(m.Out, format+"\n", args...) }

func (m *Manager) guiDomain() string { return fmt.Sprintf("gui/%d", os.Getuid()) }

func schtasks(args ...string) []string { return append([]string{"schtasks"}, args...) }

// strerror renders an error like Python's OSError.strerror.
func strerror(err error) string {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		s := strings.TrimSuffix(errno.Error(), ".")
		return strings.ToUpper(s[:1]) + s[1:]
	}
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		return "No such file or directory"
	}
	return err.Error()
}

func (m *Manager) runChecked(args []string, files []string) error {
	r, err := m.Run(Cmd{Args: args, Capture: true})
	if err != nil {
		return &Error{fmt.Sprintf("%s: %s", args[0], strerror(err)), files}
	}
	if r.Code != 0 {
		detail := strings.TrimSpace(r.Stderr + r.Stdout)
		if detail == "" {
			detail = fmt.Sprintf("Exit %d", r.Code)
		}
		return &Error{fmt.Sprintf("%s: %s", strings.Join(args, " "), detail), files}
	}
	return nil
}

func (m *Manager) port() int {
	if m.HTTPPort != nil {
		if p, err := m.HTTPPort(); err == nil {
			return p
		}
	}
	return 1081
}

// disconnectDaemon asks a running daemon to log out of the VPN. Ending the task kills it
// outright, and the session would stay open on the server until it times out.
func (m *Manager) disconnectDaemon(timeout time.Duration) {
	base := fmt.Sprintf("http://127.0.0.1:%d", m.port())
	// Never through a proxy: HTTP_PROXY or the Windows registry proxy would swallow the request.
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	req, err := http.NewRequest("POST", base+"/api/disconnect", strings.NewReader("{}"))
	if err != nil {
		return
	}
	req.Header.Set("X-Uni-VPN", "1")
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return // not running
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return // an older daemon without the API
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := client.Get(base + "/status.json")
		if err != nil {
			return
		}
		var status map[string]any
		err = json.NewDecoder(resp.Body).Decode(&status)
		resp.Body.Close()
		if err != nil || resp.StatusCode >= 400 {
			return
		}
		switch status["state"] {
		case "connected", "connecting", "disconnecting":
		default:
			return
		}
		m.Sleep(300 * time.Millisecond)
	}
}

// endTask logs out, ends the task and waits until Task Scheduler no longer counts it as
// running (with IgnoreNew a "/Run" right after "/End" is otherwise silently dropped).
func (m *Manager) endTask() (bool, error) {
	m.disconnectDaemon(15 * time.Second)
	if _, err := m.Run(Cmd{Args: schtasks("/End", "/TN", winsys.TaskName), Capture: true, NoWindow: true}); err != nil {
		return false, err
	}
	for range 20 {
		if !m.IsActive() {
			return true, nil
		}
		m.Sleep(500 * time.Millisecond)
	}
	return false, nil
}

// writeText writes like Python's Path.write_text: "\n" becomes "\r\n" on Windows.
func writeText(path, text string, utf16LE bool) error {
	if runtime.GOOS == "windows" {
		text = strings.ReplaceAll(text, "\n", "\r\n")
	}
	data := []byte(text)
	if utf16LE {
		// Python's "utf-16": a byte order mark, then little endian.
		units := utf16.Encode([]rune(text))
		data = make([]byte, 2+2*len(units))
		data[0], data[1] = 0xFF, 0xFE
		for i, u := range units {
			data[2+2*i], data[3+2*i] = byte(u), byte(u>>8)
		}
	}
	return os.WriteFile(path, data, 0o666)
}

// mkdirLast creates dir and its parents like Path.mkdir(parents=True, exist_ok=True, mode=...):
// only the last component gets mode.
func mkdirLast(dir string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o777); err != nil {
		return err
	}
	if err := os.Mkdir(dir, mode); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return nil
}

func (m *Manager) installWindows(target, binary string, dryRun bool) ([]string, error) {
	if !platform.AdminOnly(binary) {
		// Runs elevated: anything the user can change could gain administrator rights.
		m.printf("   Warning: %s is not in Program Files, use install.ps1 to install", binary)
	}
	// The working directory is the installation, the parent of bin, as for the Python core.
	text := winsys.RenderTaskBinary(m.Conhost, binary, winsys.CurrentUser(), filepath.Dir(filepath.Dir(binary)))
	if dryRun {
		m.printf("-> would register the scheduled task %s (%s) and start it", winsys.TaskName, target)
		return []string{target}, nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o777); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(platform.StateDir(), 0o777); err != nil {
		return nil, err
	}
	if err := writeText(target, text, true); err != nil {
		return nil, err
	}
	files := []string{target}
	if err := m.runChecked(schtasks("/Create", "/TN", winsys.TaskName, "/XML", target, "/F"), files); err != nil {
		return files, err
	}
	// Like "systemctl restart": a running old daemon makes way for the new code.
	stopped, err := m.endTask()
	if err != nil {
		return files, err
	}
	if !stopped {
		// With IgnoreNew, "/Run" would be dropped silently and the old code keep running.
		return files, &Error{"The running service did not stop, log out and in, then run the installer again", files}
	}
	return files, m.runChecked(schtasks("/Run", "/TN", winsys.TaskName), files)
}

// Install writes the service file for the Go binary and loads the service. Returns the files
// written (also with an *Error, which means the file is there but loading failed).
func (m *Manager) Install(dryRun bool) ([]string, error) {
	target := m.UnitTargetPath()
	binary, err := m.binary()
	if err != nil {
		return nil, err
	}
	if m.windows() {
		return m.installWindows(target, binary, dryRun)
	}
	brew := ""
	if m.macOS() && m.BrewPrefix != nil {
		brew = m.BrewPrefix()
	}
	text, err := RenderUnit(m.macOS(), binary, platform.StateDir(), brew, PassthroughEnv(m.Getenv))
	if err != nil {
		return nil, err
	}
	if dryRun {
		m.printf("-> would write %s and load the service", target)
		return []string{target}, nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o777); err != nil {
		return nil, err
	}
	if err := mkdirLast(platform.StateDir(), 0o700); err != nil {
		return nil, err
	}
	if err := writeText(target, text, false); err != nil {
		return nil, err
	}
	files := []string{target}
	if m.macOS() {
		if _, err := m.Run(Cmd{Args: []string{"launchctl", "bootout", m.guiDomain(), target}, Capture: true}); err != nil {
			return files, err
		}
		// Right after bootout the old job may still be going away ("Bootstrap failed: 5").
		for attempt := 0; ; attempt++ {
			err := m.runChecked([]string{"launchctl", "bootstrap", m.guiDomain(), target}, files)
			if err == nil {
				break
			}
			if attempt == 4 {
				return files, err
			}
			m.Sleep(time.Second)
		}
		return files, nil
	}
	for _, args := range [][]string{
		{"systemctl", "--user", "daemon-reload"},
		{"systemctl", "--user", "enable", "--now", UnitName},
		// "enable --now" leaves a running service alone; after install.sh the new code should run.
		{"systemctl", "--user", "restart", UnitName},
	} {
		if err := m.runChecked(args, files); err != nil {
			return files, err
		}
	}
	return files, nil
}

// Uninstall stops and removes the service. False if it could not be removed (also without
// systemctl/launchctl); the error is for a file that could not be deleted or a schtasks that
// could not run.
func (m *Manager) Uninstall() (bool, error) {
	target := m.UnitTargetPath()
	if m.windows() {
		if _, err := m.endTask(); err != nil {
			return false, err
		}
		result, err := m.Run(Cmd{Args: schtasks("/Delete", "/TN", winsys.TaskName, "/F"), Capture: true, NoWindow: true})
		if err != nil {
			return false, err
		}
		query, err := m.Run(Cmd{Args: schtasks("/Query", "/TN", winsys.TaskName), Capture: true, NoWindow: true})
		if err != nil {
			return false, err
		}
		if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
		return result.Code == 0 || query.Code != 0, nil
	}
	var args []string
	if m.macOS() {
		args = []string{"launchctl", "bootout", m.guiDomain(), target}
	} else {
		args = []string{"systemctl", "--user", "disable", "--now", UnitName}
	}
	// No systemctl/launchctl: the service may still be loaded, so report it, but remove the file.
	_, err := m.Run(Cmd{Args: args, Capture: true})
	stopped := err == nil
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if !m.macOS() && stopped {
		if _, err := m.Run(Cmd{Args: []string{"systemctl", "--user", "daemon-reload"}, Capture: true}); err != nil {
			return false, err
		}
	}
	return stopped, nil
}

// IsActive reports whether the service runs.
func (m *Manager) IsActive() bool {
	if m.windows() {
		// The state name from PowerShell is not localized, unlike the schtasks output.
		r, err := m.Run(Cmd{Args: []string{"powershell", "-NoProfile", "-NonInteractive", "-Command",
			fmt.Sprintf("(Get-ScheduledTask -TaskName '%s').State", winsys.TaskName)},
			Capture: true, Timeout: 15 * time.Second, NoWindow: true})
		return err == nil && strings.TrimSpace(r.Stdout) == "Running"
	}
	if m.macOS() {
		r, err := m.Run(Cmd{Args: []string{"launchctl", "print", m.guiDomain() + "/" + Label},
			Capture: true, Timeout: 5 * time.Second})
		return err == nil && r.Code == 0 && strings.Contains(r.Stdout, "state = running")
	}
	r, err := m.Run(Cmd{Args: []string{"systemctl", "--user", "is-active", UnitName}, Capture: true, Timeout: 5 * time.Second})
	return err == nil && strings.TrimSpace(r.Stdout) == "active"
}

// Actions are the verbs Control takes.
var Actions = []string{"start", "stop", "restart", "enable", "disable", "status"}

// Control runs start, stop, restart, enable, disable or status with the output on the
// terminal, and returns the exit code.
func (m *Manager) Control(action string) (int, error) {
	known := false
	for _, a := range Actions {
		known = known || a == action
	}
	if !known {
		return 1, fmt.Errorf("unknown action %q", action)
	}
	target := m.UnitTargetPath()
	_, statErr := os.Stat(target)
	if m.windows() {
		if statErr != nil {
			m.printf("Service file is missing (%s), please run install.ps1", target)
			return 1, nil
		}
		start := schtasks("/Run", "/TN", winsys.TaskName)
		if action == "stop" || action == "restart" || action == "disable" {
			stopped, err := m.endTask()
			if err != nil {
				return 1, err
			}
			if !stopped && action != "disable" {
				m.printf("The service did not stop (administrator rights needed?)")
				return 1, nil
			}
		}
		steps := map[string][][]string{
			"start": {start}, "stop": {}, "restart": {start},
			"enable":  {schtasks("/Change", "/TN", winsys.TaskName, "/ENABLE"), start},
			"disable": {schtasks("/Change", "/TN", winsys.TaskName, "/DISABLE")},
			"status":  {schtasks("/Query", "/TN", winsys.TaskName, "/V", "/FO", "LIST")},
		}[action]
		rc := 0
		for _, args := range steps {
			if rc != 0 {
				break // Python's `rc or run(...)`: after a failure the rest is skipped
			}
			r, err := m.Run(Cmd{Args: args})
			if err != nil {
				return 1, err
			}
			rc = r.Code
		}
		return rc, nil
	}
	var commands map[string][]string
	if m.macOS() {
		domain := m.guiDomain()
		commands = map[string][]string{
			"start":   {"launchctl", "bootstrap", domain, target},
			"stop":    {"launchctl", "bootout", domain, target},
			"restart": {"launchctl", "kickstart", "-k", domain + "/" + Label},
			"enable":  {"launchctl", "bootstrap", domain, target},
			"disable": {"launchctl", "bootout", domain, target},
			"status":  {"launchctl", "print", domain + "/" + Label},
		}
	} else {
		commands = map[string][]string{
			"start":   {"systemctl", "--user", "start", UnitName},
			"stop":    {"systemctl", "--user", "stop", UnitName},
			"restart": {"systemctl", "--user", "restart", UnitName},
			"enable":  {"systemctl", "--user", "enable", "--now", UnitName},
			"disable": {"systemctl", "--user", "disable", "--now", UnitName},
			"status":  {"systemctl", "--user", "status", "--no-pager", UnitName},
		}
	}
	if statErr != nil {
		m.printf("Service file is missing (%s), please run install.sh", target)
		return 1, nil
	}
	r, err := m.Run(Cmd{Args: commands[action]})
	if err != nil {
		return 1, err
	}
	return r.Code, nil
}
