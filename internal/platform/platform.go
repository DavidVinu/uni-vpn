// Package platform holds operating system specifics: paths, binaries, Cisco detection.
// It mirrors uni_vpn/platform.py; paths and names must stay identical so both cores share
// one installation.
package platform

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const App = "uni-vpn"

var (
	IsMacOS   = runtime.GOOS == "darwin"
	IsWindows = runtime.GOOS == "windows"
)

const Security = "/usr/bin/security"

func programFiles() []string {
	var out []string
	seen := map[string]bool{}
	for _, name := range []string{"ProgramFiles", "ProgramFiles(x86)", "ProgramW6432"} {
		if v := os.Getenv(name); v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		out = []string{`C:\Program Files`, `C:\Program Files (x86)`}
	}
	return out
}

// CiscoVPN is the Cisco Secure Client command line program.
func CiscoVPN() string {
	if !IsWindows {
		return "/opt/cisco/secureclient/bin/vpn"
	}
	bases := programFiles()
	for _, base := range bases {
		p := filepath.Join(base, "Cisco", "Cisco Secure Client", "vpncli.exe")
		if isFile(p) {
			return p
		}
	}
	return filepath.Join(bases[len(bases)-1], "Cisco", "Cisco Secure Client", "vpncli.exe")
}

// SearchDirs are looked through after PATH (never PATH on Windows).
func SearchDirs() []string {
	if IsWindows {
		var out []string
		for _, base := range programFiles() {
			for _, name := range []string{"OpenConnect-GUI", "OpenConnect"} {
				out = append(out, filepath.Join(base, name))
			}
		}
		return out
	}
	return []string{"/usr/sbin", "/usr/bin", "/opt/homebrew/bin", "/opt/homebrew/sbin",
		"/usr/local/bin", "/usr/local/sbin", "/bin", "/sbin"}
}

func home() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}

func localAppData() string {
	if v := os.Getenv("LOCALAPPDATA"); v != "" {
		return v
	}
	return filepath.Join(home(), "AppData", "Local")
}

func ConfigDir() string {
	if IsWindows && os.Getenv("XDG_CONFIG_HOME") == "" {
		return filepath.Join(localAppData(), App)
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(home(), ".config")
	}
	return filepath.Join(base, App)
}

func StateDir() string {
	xdg := os.Getenv("XDG_STATE_HOME")
	if IsWindows && xdg == "" {
		return filepath.Join(localAppData(), App, "logs")
	}
	if IsMacOS && xdg == "" {
		return filepath.Join(home(), "Library", "Logs", App)
	}
	if xdg == "" {
		xdg = filepath.Join(home(), ".local", "state")
	}
	return filepath.Join(xdg, App)
}

func LogFile() string  { return filepath.Join(StateDir(), "daemon.log") }
func LockFile() string { return filepath.Join(ConfigDir(), "daemon.lock") }

// AdminOnly reports whether path lies inside Program Files, which only administrators can
// change. The elevated Windows service must not run programs any user process could replace.
func AdminOnly(path string) bool {
	full, err := filepath.EvalSymlinks(path)
	if err != nil {
		full = path
	}
	full = strings.ToLower(filepath.Clean(full))
	for _, base := range programFiles() {
		b, err := filepath.EvalSymlinks(base)
		if err != nil {
			b = base
		}
		if strings.HasPrefix(full, strings.ToLower(filepath.Clean(b))+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

func executable(p string) bool {
	st, err := os.Stat(p)
	if err != nil || !st.Mode().IsRegular() {
		return false
	}
	return IsWindows || st.Mode().Perm()&0o111 != 0
}

// FindBinary looks up a program: the override from config.toml, then PATH, then SearchDirs.
func FindBinary(name, override string) string {
	if IsWindows && override != "" && !AdminOnly(override) {
		// Neither PATH nor config.toml (both writable by the user) for the elevated service.
		override = ""
	}
	if override != "" {
		if executable(override) {
			return override
		}
		return ""
	}
	if !IsWindows {
		if found, err := exec.LookPath(name); err == nil {
			return found
		}
	}
	names := []string{name}
	if IsWindows && !strings.HasSuffix(strings.ToLower(name), ".exe") {
		names = append(names, name+".exe")
	}
	for _, dir := range SearchDirs() {
		for _, n := range names {
			if c := filepath.Join(dir, n); executable(c) {
				return c
			}
		}
	}
	return ""
}

// TunnelHelpers are the programs the tunnel needs besides openconnect.
func TunnelHelpers() []string {
	if IsWindows {
		return nil
	}
	return []string{"ocproxy"}
}

func CiscoInstalled() bool { return executable(CiscoVPN()) }

// CiscoConnected reports whether the Cisco client holds the VPN. On Linux it always creates
// cscotun0 when connected; "vpn state" takes 2.2 s and would delay every connect.
func CiscoConnected() bool {
	if !IsWindows {
		if _, err := os.Stat("/sys/class/net/cscotun0"); err == nil {
			return true
		}
	}
	if !IsMacOS && !IsWindows {
		return false
	}
	if !CiscoInstalled() {
		return false
	}
	cmd := exec.Command(CiscoVPN(), "state")
	hideWindow(cmd)
	out, err := runTimeout(cmd, 5*time.Second)
	if err != nil {
		return false
	}
	return strings.Contains(out, "state: Connected")
}

func runTimeout(cmd *exec.Cmd, d time.Duration) (string, error) {
	var buf strings.Builder
	cmd.Stdout = &buf
	if err := cmd.Start(); err != nil {
		return "", err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return buf.String(), err
	case <-time.After(d):
		_ = cmd.Process.Kill()
		<-done
		return buf.String(), os.ErrDeadlineExceeded
	}
}

// AppInstallDir is where the installers put the program.
func AppInstallDir() string {
	if IsWindows {
		return filepath.Join(programFiles()[0], App)
	}
	if IsMacOS {
		return filepath.Join(home(), "Library", "Application Support", App, "app")
	}
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		base = filepath.Join(home(), ".local", "share")
	}
	return filepath.Join(base, App, "app")
}
