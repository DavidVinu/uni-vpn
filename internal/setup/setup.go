// Package setup sets up and removes uni-vpn: config, the uni-vpn command, the service, the
// proxy rule, the app and the keyring entries. It mirrors uni_vpn/setup.py; the files it
// writes and records (installed-files.txt) are the same, so either core can remove what the
// other installed. The service and the uni-vpn command run the Go binary
// (<root>/bin/uni-vpn-core) directly.
package setup

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/DavidVinu/uni-vpn/internal/config"
	"github.com/DavidVinu/uni-vpn/internal/credentials"
	"github.com/DavidVinu/uni-vpn/internal/desktop"
	"github.com/DavidVinu/uni-vpn/internal/detect"
	"github.com/DavidVinu/uni-vpn/internal/messages"
	"github.com/DavidVinu/uni-vpn/internal/platform"
	"github.com/DavidVinu/uni-vpn/internal/service"
	"github.com/DavidVinu/uni-vpn/internal/sysproxy"
	"github.com/DavidVinu/uni-vpn/internal/totp"
	unis "github.com/DavidVinu/uni-vpn/internal/universities"
	"github.com/DavidVinu/uni-vpn/internal/winsys"
)

const (
	InstalledFiles   = "installed-files.txt"
	UniversityPrompt = "Your university (name or part of it, Enter for Heidelberg, other if not listed): "
	finalHint        = "\nUni VPN is in your apps (state, connect, settings), also at http://127.0.0.1:%d/\n" +
		"Restart any open browser once so that it reads the proxy rule.\n"
	manualProxyHint = "   The proxy rule could not be registered automatically (no GNOME, KDE, macOS or Windows proxy settings found).\n" +
		"   Enter it by hand in the browser: Settings -> Network/Proxy -> automatic proxy configuration (PAC):\n" +
		"   %s"
	manualProxyFailed = "   Registering the proxy rule with the system failed. Enter it by hand in the browser:\n" +
		"   Settings -> Network/Proxy -> automatic proxy configuration (PAC): %s"
)

// MFAChoices are the second factors the terminal setup offers, in order.
var MFAChoices = []desktop.Pair{{Key: "none", Value: "none"}, {Key: "totp_field", Value: "one-time code in its own field"},
	{Key: "totp_append", Value: "one-time code after the password"}, {Key: "duo_push", Value: "Duo push"}}

// MacOSBinDirs: ~/.local/bin is not on the PATH on macOS; Homebrew's bin is.
var MacOSBinDirs = []string{"/opt/homebrew/bin", "/usr/local/bin"}

// ErrNoTerminal means a question could not be asked: stdin ended.
var ErrNoTerminal = errors.New("no terminal to answer questions")

// Args are the command line options of "setup" and "uninstall".
type Args struct {
	DryRun, NoGUI, NoBrowser, Yes bool
	User, University              string
}

// Service registers the background service (service.Manager).
type Service interface {
	Install(dryRun bool) ([]string, error)
	Uninstall() (bool, error)
	UnitTargetPath() string
}

// App sets up and opens the app window (desktop.Desktop).
type App interface {
	Install(port int, dry bool, created func(string)) bool
	OpenApp(port int, page string) bool
	Uninstall()
}

// Setup runs setup and uninstall; tests replace its parts.
type Setup struct {
	GOOS   string
	Root   string // the installation, the parent of bin
	Binary string // <root>/bin/uni-vpn-core
	Out    io.Writer
	Getenv func(string) string
	Home   func() string
	// Input and Getpass ask in the terminal; ErrNoTerminal when nobody can answer.
	Input      func(prompt string) (string, error)
	Getpass    func(prompt string) (string, error)
	IsTerminal func() bool
	FindBinary func(name, override string) string
	Service    Service
	Run        service.Runner
	UID        int

	ProxyInstall   func(httpPort int) (string, error)
	ProxyUninstall func() (string, error)
	StorePassword  func(user, password string) error
	StoreTOTP      func(user, token string) error
	DeletePassword func(user string) (bool, error)
	DeleteTOTP     func(user string) (bool, error)
	// KeyringState is "present", "missing", "locked" or "error:<text>".
	KeyringState func(user string, kind credentials.Kind) string

	PortOpen   func(port int) bool
	Sleep      func(time.Duration)
	Now        func() time.Time
	OpenURL    func(url string) bool
	HasDesktop func() bool
	Probe      func(host, usergroup, group string) (detect.Detection, error)
	App        App

	IsAdmin        func() bool
	AddUserPath    func(dir string) (bool, error)
	RemoveUserPath func(dir string) error
	CiscoInstalled func() bool
	Writable       func(dir string) bool
}

// CoreName is the Go binary's file name in bin.
func CoreName(goos string) string {
	if goos == "windows" {
		return "uni-vpn-core.exe"
	}
	return "uni-vpn-core"
}

// executable is this program, with symlinks resolved; "" when unknown.
func executable() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return exe
}

// Root is the installation this program belongs to: the parent of the folder it is in.
func Root() string {
	exe := executable()
	if exe == "" {
		return "."
	}
	return filepath.Dir(filepath.Dir(exe))
}

func home() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}

// New acts on this machine, for the installation at root.
func New(root string) *Setup {
	goos := runtime.GOOS
	// In an installation this is <root>/bin/uni-vpn-core, the program that runs.
	binary := filepath.Join(root, "bin", CoreName(goos))
	if exe := executable(); exe != "" && filepath.Dir(filepath.Dir(exe)) == root {
		binary = exe
	}
	manager := service.New()
	manager.Binary = binary
	manager.HTTPPort = func() (int, error) {
		cfg, err := config.Load("")
		if err != nil {
			return 0, err
		}
		return cfg.HTTPPort, nil
	}
	app := desktop.New(root)
	keyring := credentials.Default
	s := &Setup{
		GOOS: goos, Root: root, Binary: binary, Out: os.Stdout, Getenv: os.Getenv, Home: home,
		IsTerminal: isTerminal, FindBinary: platform.FindBinary, Service: manager, Run: service.Exec, UID: os.Getuid(),
		ProxyInstall:   func(port int) (string, error) { return sysproxy.Install(port, "") },
		ProxyUninstall: func() (string, error) { return sysproxy.Uninstall("") },
		StorePassword:  keyring.StorePassword, StoreTOTP: keyring.StoreTotp,
		DeletePassword: keyring.DeletePassword, DeleteTOTP: keyring.DeleteTotp,
		KeyringState: KeyringState, PortOpen: PortOpen, Sleep: time.Sleep, Now: time.Now,
		OpenURL: desktop.OpenURL, HasDesktop: app.HasDesktop,
		Probe: func(host, usergroup, group string) (detect.Detection, error) {
			return detect.Probe(context.Background(), host, usergroup, group, detect.Options{})
		},
		App: app, IsAdmin: winsys.IsAdmin, AddUserPath: winsys.AddUserPath, RemoveUserPath: winsys.RemoveUserPath,
		CiscoInstalled: platform.CiscoInstalled, Writable: writable,
	}
	stdin := bufio.NewReader(os.Stdin)
	s.Input = func(prompt string) (string, error) { return s.readLine(stdin, prompt, false) }
	s.Getpass = func(prompt string) (string, error) { return s.readLine(stdin, prompt, true) }
	return s
}

func (s *Setup) readLine(r *bufio.Reader, prompt string, secret bool) (string, error) {
	fmt.Fprint(s.Out, prompt)
	if secret {
		if restore, ok := noEcho(); ok {
			// Ctrl+C while the echo is off must not leave the terminal without it.
			interrupted := make(chan os.Signal, 1)
			signal.Notify(interrupted, os.Interrupt)
			go func() {
				if _, ok := <-interrupted; ok {
					restore()
					fmt.Fprintln(s.Out)
					os.Exit(130)
				}
			}()
			defer func() {
				signal.Stop(interrupted)
				close(interrupted)
				restore()
				fmt.Fprintln(s.Out)
			}()
		}
	}
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		if errors.Is(err, io.EOF) {
			return "", ErrNoTerminal
		}
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// KeyringState probes the keyring like doctor.keyring_state.
func KeyringState(user string, kind credentials.Kind) string {
	_, err := credentials.Default.GetSecret(context.Background(), user, kind, 5*time.Second, nil)
	var missing *credentials.MissingError
	var locked *credentials.LockedError
	switch {
	case err == nil:
		return "present"
	case errors.As(err, &missing):
		return "missing"
	case errors.As(err, &locked):
		return "locked"
	}
	return "error:" + err.Error()
}

// PortOpen reports whether something listens on 127.0.0.1:port.
func PortOpen(port int) bool {
	c, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 250*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

func (s *Setup) windows() bool { return s.GOOS == "windows" }
func (s *Setup) macOS() bool   { return s.GOOS == "darwin" }

func (s *Setup) say(format string, args ...any) { fmt.Fprintf(s.Out, "-> "+format+"\n", args...) }
func (s *Setup) printf(format string, args ...any) {
	fmt.Fprintf(s.Out, format+"\n", args...)
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func isDir(path string) bool {
	st, err := os.Lstat(path)
	return err == nil && st.IsDir()
}

// --- installed-files.txt ---------------------------------------------------

// RecordsPath lists every file setup created, for uninstall.
func RecordsPath() string { return filepath.Join(platform.ConfigDir(), InstalledFiles) }

// Recorded returns the recorded paths.
func Recorded() []string {
	data, err := os.ReadFile(RecordsPath())
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

// Record adds paths to the list, each once.
func Record(paths []string) error {
	merged := Recorded()
	for _, p := range paths {
		if !slices.Contains(merged, p) {
			merged = append(merged, p)
		}
	}
	dir := platform.ConfigDir()
	if err := os.MkdirAll(filepath.Dir(dir), 0o777); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	newline := "\n"
	if runtime.GOOS == "windows" {
		newline = "\r\n" // Python's write_text
	}
	return os.WriteFile(RecordsPath(), []byte(strings.Join(merged, newline)+newline), 0o666)
}

// --- helpers ---------------------------------------------------------------

// WaitForPort waits until the daemon has bound the port after the service start. True as soon
// as it is reachable.
func (s *Setup) WaitForPort(port int, timeout time.Duration) bool {
	deadline := s.Now().Add(timeout)
	for {
		if s.PortOpen(port) {
			return true
		}
		if !s.Now().Before(deadline) {
			return false
		}
		s.Sleep(250 * time.Millisecond)
	}
}

func (s *Setup) neededPrograms() []string {
	out := []string{"openconnect"}
	if !s.windows() {
		out = append(out, "ocproxy")
	}
	if !s.windows() && !s.macOS() {
		out = append(out, "secret-tool")
	}
	return out
}

// CommandPath is where the uni-vpn command goes.
func (s *Setup) CommandPath() string {
	if s.windows() {
		return filepath.Join(platform.ConfigDir(), "bin", "uni-vpn.cmd")
	}
	if s.macOS() {
		for _, dir := range MacOSBinDirs {
			if isDir(dir) && s.Writable(dir) {
				return filepath.Join(dir, "uni-vpn")
			}
		}
	}
	return filepath.Join(s.Home(), ".local", "bin", "uni-vpn")
}

// CmdBytes is a batch file as cmd.exe reads it: plain ASCII as it is, anything else as UTF-8
// with chcp 65001 first.
func CmdBytes(text string) []byte {
	for i := 0; i < len(text); i++ {
		if text[i] >= 0x80 {
			return []byte("@chcp 65001 >nul\r\n" + text)
		}
	}
	return []byte(text)
}

var shQuote = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`, "`", "\\`")

// Wrapper is the uni-vpn command's text: it runs the Go binary.
func (s *Setup) Wrapper() []byte {
	if s.windows() {
		return CmdBytes("@echo off\r\n\"" + s.Binary + "\" %*\r\n")
	}
	return []byte("#!/bin/sh\nexec \"" + shQuote.Replace(s.Binary) + "\" \"$@\"\n")
}

// InstallCommand puts uni-vpn on the PATH.
func (s *Setup) InstallCommand(dry bool, created func(string)) error {
	link := s.CommandPath()
	if dry {
		s.say("would create %s as a wrapper for %s", link, s.Binary)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(link), 0o777); err != nil {
		return err
	}
	if exists(link) {
		if err := os.Remove(link); err != nil {
			return err
		}
	}
	if err := os.WriteFile(link, s.Wrapper(), 0o755); err != nil {
		return err
	}
	if err := os.Chmod(link, 0o755); err != nil {
		return err
	}
	created(link)
	s.say("Command created: %s", link)
	dir := filepath.Dir(link)
	if s.windows() {
		if added, _ := s.AddUserPath(dir); added {
			s.printf("   Open a new terminal to use the uni-vpn command")
		}
		return nil
	}
	if !slices.Contains(strings.Split(s.Getenv("PATH"), ":"), dir) {
		if s.macOS() {
			s.printf("   Note: %s is not in PATH, add it to PATH to use the uni-vpn command", dir)
		} else {
			// Ubuntu's ~/.profile adds ~/.local/bin at login once it exists.
			s.printf("   Note: %s is not in PATH yet, the uni-vpn command works after logging out and in", dir)
		}
	}
	return nil
}

// TOTPHint says where the TOTP secret comes from.
func TOTPHint(cfg *config.Config) string {
	portal := ""
	if cfg.MFAPortalURL != "" {
		portal = " (" + cfg.MFAPortalURL + ")"
	}
	return "   Second factor: in your university's MFA portal" + portal + ", add another time-based token (TOTP) for\n" +
		"   this computer and copy its secret, the otpauth:// line or the letters after secret=.\n" +
		"   The app on your phone stays as a second token."
}

func (s *Setup) ask(prompt string) (string, error) {
	text, err := s.Input(prompt)
	return strings.TrimSpace(text), err
}

// AskOther handles "not listed": the VPN address, then what the gateway's login form shows.
// ok is false when nothing fits.
func (s *Setup) AskOther() (university string, overrides []config.Setting, ok bool, err error) {
	text, err := s.Input("VPN address (for example vpn.example.edu): ")
	if err != nil {
		return "", nil, false, err
	}
	host, usergroup, ferr := detect.SplitAddress(text)
	if ferr != nil {
		s.printf("   %s", ferr)
		return "", nil, false, nil
	}
	result, perr := s.Probe(host, usergroup, "")
	if perr != nil {
		s.printf("   %s", perr)
		return "", nil, false, nil
	}
	overrides = []config.Setting{{Key: "host", Value: host}}
	if usergroup != "" {
		overrides = append(overrides, config.Setting{Key: "usergroup", Value: usergroup})
	}
	if result.Error != "" {
		s.printf("   %s", result.Error)
	}
	if len(result.Groups) > 1 {
		s.printf("   Groups: %s", strings.Join(result.Groups, ", "))
		group, err := s.ask(fmt.Sprintf("Group [%s]: ", result.Group))
		if err != nil {
			return "", nil, false, err
		}
		if group == "" {
			group = result.Group
		}
		if group != result.Group {
			if result, perr = s.Probe(host, usergroup, group); perr != nil {
				s.printf("   %s", perr)
				return "", nil, false, nil
			}
		}
		overrides = append(overrides, config.Setting{Key: "authgroup", Value: group})
	}
	if result.SAML {
		s.printf("   %s", messages.SAMLRequired.Text)
		return "", nil, false, nil
	}
	guess := result.Suggestion().MFA
	choices := make([]string, len(MFAChoices))
	for i, c := range MFAChoices {
		choices[i] = fmt.Sprintf("%s (%s)", c.Key, c.Value)
	}
	s.printf("   Second factor: %s", strings.Join(choices, ", "))
	mfa, err := s.ask(fmt.Sprintf("Second factor [%s]: ", guess))
	if err != nil {
		return "", nil, false, err
	}
	if mfa == "" {
		mfa = guess
	}
	if !slices.ContainsFunc(MFAChoices, func(c desktop.Pair) bool { return c.Key == mfa }) {
		s.printf("   Unknown second factor %s", unis.Repr(mfa))
		return "", nil, false, nil
	}
	return unis.OtherID, append(overrides, config.Setting{Key: "mfa", Value: mfa}), true, nil
}

// ChooseUniversity is the assistant's university step for the terminal.
func (s *Setup) ChooseUniversity() (university string, overrides []config.Setting, ok bool, err error) {
	for range 3 {
		text, err := s.ask(UniversityPrompt)
		if err != nil {
			return "", nil, false, err
		}
		if text == "" {
			return unis.DefaultID, nil, true, nil
		}
		if lower := strings.ToLower(text); lower == unis.OtherID || lower == "not listed" {
			return s.AskOther()
		}
		found := unis.Search(text)
		if len(found) == 1 {
			if found[0].MFA == "saml" {
				s.printf("   %s: %s", found[0].Name, messages.SAMLRequired.Text)
				return "", nil, false, nil
			}
			s.say("%s", found[0].Name)
			return found[0].ID, nil, true, nil
		}
		if len(found) > 0 {
			names := make([]string, 0, 8)
			for _, p := range found[:min(8, len(found))] {
				names = append(names, fmt.Sprintf("%s (%s)", p.Name, p.ID))
			}
			s.printf("   Which one? %s", strings.Join(names, ", "))
		} else {
			s.printf("   Not in the list. Type other to enter the VPN address")
		}
	}
	s.printf("No university chosen")
	return "", nil, false, nil
}

// --- setup -------------------------------------------------------------------

// Setup sets everything up. The error is for what Python lets through to the command line:
// a *config.ConfigError, a *credentials.KeyringError, ErrNoTerminal, or a file that could not
// be written.
func (s *Setup) Setup(a Args) (int, error) {
	var recordErr error
	created := func(path string) {
		// Record right away so that an abort further down leaves nothing unrecorded behind.
		if err := Record([]string{path}); err != nil && recordErr == nil {
			recordErr = err
		}
	}
	code, err := s.setup(a, created)
	if err == nil {
		err = recordErr
	}
	return code, err
}

func defaultWithPorts(path string) *config.Config {
	cfg := config.Default()
	ports := config.PortsFromBroken(path)
	if p, ok := ports["socks_port"]; ok {
		cfg.SocksPort = p
	}
	if p, ok := ports["http_port"]; ok {
		cfg.HTTPPort = p
	}
	return &cfg
}

func (s *Setup) setup(a Args, created func(string)) (int, error) {
	dry := a.DryRun
	// With a desktop the browser does the rest (setup assistant); otherwise ask here.
	gui := !dry && !a.NoGUI && s.HasDesktop()

	needed := s.neededPrograms()
	var missing []string
	for _, name := range needed {
		if s.FindBinary(name, "") == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		if !dry {
			s.printf("Missing: %s. To install it, start the installer again.", strings.Join(missing, ", "))
			return 1, nil
		}
		s.say("would require: %s (start the installer again)", strings.Join(missing, ", "))
	} else {
		s.say("%s found", strings.Join(needed[:min(2, len(needed))], " and "))
	}

	cfgPath := config.DefaultPath()
	user := strings.TrimSpace(a.User)
	university := strings.TrimSpace(a.University)
	if university != "" {
		profile, ok := unis.Get(university)
		if !ok || university == unis.OtherID {
			s.printf("Unknown university %s, see uni_vpn/universities.json", unis.Repr(university))
			return 1, nil
		}
		if profile.MFA == "saml" {
			s.printf("%s: %s", profile.Name, messages.SAMLRequired.Text)
			return 1, nil
		}
	}
	var broken error
	configured := exists(cfgPath)
	var cfg *config.Config
	switch {
	case configured:
		loaded, err := config.Load(cfgPath)
		var ce *config.ConfigError
		if errors.As(err, &ce) {
			// An update must still go through; the file stays as it is and the daemon reports the error.
			broken = err
			cfg = defaultWithPorts(cfgPath) // the ports the daemon uses
			s.printf("Config %s is invalid (%s); the setup assistant asks again, the file is kept", cfgPath, err)
		} else if err != nil {
			return 1, err
		} else {
			cfg = loaded
			s.say("Config found: %s (%s, university ID %s)", cfgPath, cfg.UniversityName, cfg.User)
			if university != "" && university != cfg.University {
				s.printf("   Keeping %s. To switch, set university = \"%s\" in %s", cfg.UniversityName, university, cfgPath)
			}
		}
		if user != "" && broken == nil && user != cfg.User {
			if !config.ValidUser(user) {
				s.printf("Not a university ID: %s", unis.Repr(user))
				return 1, nil
			}
			if dry {
				s.say("would change the university ID to %s", user)
			} else {
				if err := config.SetUser(cfgPath, user); err != nil {
					return 1, err
				}
				if cfg, err = config.Load(cfgPath); err != nil {
					return 1, err
				}
				s.say("University ID changed to %s", user)
			}
		}
	case gui:
		// The assistant writes config.toml together with the secrets; a given ID is only prefilled.
		if user != "" && !config.ValidUser(user) {
			s.printf("Not a university ID: %s", unis.Repr(user))
			return 1, nil
		}
		def := config.Default()
		cfg = &def
	default:
		var overrides []config.Setting
		if university == "" && dry {
			university = unis.DefaultID
		} else if university == "" {
			chosen, extra, ok, err := s.ChooseUniversity()
			if err != nil {
				return 1, err
			}
			if !ok {
				return 1, nil
			}
			university, overrides = chosen, extra
		}
		if user == "" {
			if dry && !s.IsTerminal() {
				user = "example"
			} else {
				var err error
				if user, err = s.ask("University ID (e.g. ab123): "); err != nil {
					return 1, err
				}
			}
		}
		if user == "" {
			s.printf("No university ID given")
			return 1, nil
		}
		if !config.ValidUser(user) {
			s.printf("Not a university ID: %s", unis.Repr(user))
			return 1, nil
		}
		if dry {
			profile, _ := unis.Get(university)
			s.say("would create %s for %s with university ID %s", cfgPath, profile.Name, user)
			c, err := config.ProfileConfig(university, nil)
			if err != nil {
				return 1, err
			}
			c.User = user
			cfg = &c
		} else {
			if err := config.WriteInitial(cfgPath, user, university, overrides); err != nil {
				return 1, err
			}
			created(cfgPath)
			var err error
			if cfg, err = config.Load(cfgPath); err != nil {
				return 1, err
			}
			s.say("Config created: %s", cfgPath)
		}
	}
	if gui && !exists(cfgPath) {
		// The setup assistant writes it; record it now so uninstall removes it.
		created(cfgPath)
	}

	if err := s.InstallCommand(dry, created); err != nil {
		return 1, err
	}

	if !s.macOS() && !s.windows() {
		openconnect := s.FindBinary("openconnect", cfg.OpenConnect)
		if openconnect == "" {
			openconnect = "/usr/sbin/openconnect"
		}
		newFile, err := s.EnsureApportIgnore(openconnect, dry)
		if err != nil {
			return 1, err
		}
		if newFile != "" {
			created(newFile)
		}
		if !dry {
			s.say("Crash reports for openconnect disabled (~/.apport-ignore.xml)")
		}
	}

	files, err := s.Service.Install(dry)
	var se *service.Error
	if errors.As(err, &se) {
		for _, path := range se.Files {
			created(path)
		}
		s.printf("Service could not be loaded: %s", se.Msg)
		return 1, nil
	}
	if err != nil {
		for _, path := range files {
			created(path)
		}
		return 1, err
	}
	if !dry {
		for _, path := range files {
			created(path)
		}
		s.say("Service set up and started")
	}

	pac := sysproxy.PacURL(cfg.HTTPPort)
	if dry {
		s.say("would register the proxy rule %s with the system (the previous setting is backed up)", pac)
	} else {
		result, err := s.ProxyInstall(cfg.HTTPPort)
		if err != nil {
			return 1, err
		}
		switch result {
		case "unavailable":
			s.printf(manualProxyHint, pac)
		case "failed":
			s.printf(manualProxyFailed, pac)
		case "replaced":
			s.say("Proxy rule registered with the system; an existing proxy setting was replaced (backed up, uninstall restores it)")
		default:
			s.say("Proxy rule registered with the system (Chrome, Edge and Firefox read it on their own)")
		}
	}

	s.App.Install(cfg.HTTPPort, dry, created)

	if s.CiscoInstalled() {
		s.printf("   Note: Cisco Secure Client is installed. Uni VPN pauses while Cisco is connected.")
		s.printf("   Tip: in Cisco Secure Client, turn off connecting automatically at start.")
	}

	if broken != nil && !gui {
		// Nothing to ask for without a valid university ID; the service is in place again.
		s.printf("\n%s Open http://127.0.0.1:%d/ in a browser.", messages.SettingsBroken.Text, cfg.HTTPPort)
		return 0, nil
	}

	link := fmt.Sprintf("http://127.0.0.1:%d/", cfg.HTTPPort)
	if gui {
		var given []desktop.Pair
		if !configured {
			given = []desktop.Pair{{Key: "user", Value: user}, {Key: "university", Value: university}}
		}
		if query := desktop.Query(given); query != "" {
			link += "?" + query
		}
		// The service has started, but the daemon needs a moment until bind().
		if !s.WaitForPort(cfg.HTTPPort, 15*time.Second) {
			s.printf("\nUni VPN did not start. Restart the computer, then open Uni VPN.")
			return 1, nil
		}
		if a.NoBrowser {
			// install.ps1 runs this elevated and opens the browser itself, unelevated (not on update).
			if configured {
				s.printf("\nStatus page: %s", link)
			} else {
				s.printf("\nFinish in the browser (%s).", link)
			}
			return 0, nil
		}
		if s.App.OpenApp(cfg.HTTPPort, desktop.PageFor(given)) {
			s.printf("\nFinish in the Uni VPN window that just opened.")
			s.printf("Restart any open browser once so that it reads the proxy rule.")
			return 0, nil
		}
		if s.OpenURL(link) {
			s.printf("\nFinish in the browser window that just opened (%s).", link)
			s.printf("Restart any other open browser once so that it reads the proxy rule.")
			return 0, nil
		}
		gui = false
		if !exists(cfgPath) || broken != nil {
			s.printf("\nOpen %s in a browser to finish the setup.", link)
			return 0, nil
		}
	}

	if dry {
		extra := ""
		if cfg.NeedsTOTP() {
			extra = " and the TOTP secret"
		}
		s.say("would ask for the university password%s and store it in the keyring", extra)
	} else if err := s.askSecrets(cfg); err != nil {
		return 1, err
	}
	fmt.Fprintf(s.Out, finalHint+"\n", cfg.HTTPPort)
	return 0, nil
}

func (s *Setup) askSecrets(cfg *config.Config) error {
	if s.KeyringState(cfg.User, credentials.KindPassword) == "present" {
		s.say("Password is already in the keyring")
	} else {
		password, err := s.Getpass(fmt.Sprintf("University password for %s (stored only in the keyring): ", cfg.User))
		if err != nil {
			return err
		}
		if password != "" {
			if err := s.StorePassword(cfg.User, password); err != nil {
				return err
			}
			s.say("Password stored in the keyring")
		} else {
			s.printf("   No password entered. Add it later in the app, under Settings.")
		}
	}
	if !cfg.NeedsTOTP() {
		return nil
	}
	if s.KeyringState(cfg.User, credentials.KindTOTP) == "present" {
		s.say("TOTP secret is already in the keyring")
		return nil
	}
	s.printf("%s", TOTPHint(cfg))
	text, err := s.Getpass(fmt.Sprintf("TOTP secret for %s (otpauth URL or Base32, input stays hidden): ", cfg.User))
	if err != nil {
		return err
	}
	if strings.TrimSpace(text) == "" {
		s.printf("   No secret entered. Add it later in the app, under Settings.")
		return nil
	}
	token, err := totp.Normalize(text)
	if err != nil {
		s.printf("   %s. Add it later in the app, under Settings.", err)
		return nil
	}
	if err := s.StoreTOTP(cfg.User, token); err != nil {
		return err
	}
	code, _ := totp.Code(token)
	s.say("TOTP secret stored in the keyring. Check code now: %s (must match the app)", code)
	return nil
}

// --- uninstall -----------------------------------------------------------------

// Uninstall removes the proxy rule, the service and every recorded file, and asks about the
// keyring entries.
func (s *Setup) Uninstall(a Args) (int, error) {
	user := ""
	if cfg, err := config.Load(config.DefaultPath()); err == nil {
		user = cfg.User
	}
	if a.DryRun {
		s.say("would remove the proxy rule from the system, remove the service and delete these files:")
		for _, path := range Recorded() {
			s.printf("   %s", path)
		}
		return 0, nil
	}
	if s.windows() && !s.IsAdmin() {
		// The elevated task cannot be removed without; stop before anything else is gone.
		script := filepath.Join(s.Root, "install.ps1")
		if exists(script) {
			s.printf("Removing needs administrator rights: powershell -ExecutionPolicy Bypass -File \"%s\" -Uninstall", script)
		} else {
			s.printf("Removing needs administrator rights: right-click Terminal or PowerShell, choose Run as administrator, then run uni-vpn uninstall there")
		}
		return 1, nil
	}
	failed := false
	proxy, err := s.ProxyUninstall()
	if err != nil {
		return 1, err
	}
	if proxy == "failed" {
		failed = true
		s.printf("Proxy setting could not be restored, the backup stays in %s", sysproxy.BackupPath())
	} else if proxy == "restored" {
		s.say("Proxy setting restored")
	} else {
		s.say("Proxy setting was not set by uni-vpn")
	}
	if removed, err := s.Service.Uninstall(); err != nil || !removed {
		failed = true
		s.printf("Service could not be removed")
	} else {
		s.say("Service removed")
	}
	s.App.Uninstall()
	for _, path := range Recorded() {
		st, err := os.Lstat(path)
		if err != nil {
			continue
		}
		if st.IsDir() {
			os.RemoveAll(path) // macOS app entry
			s.say("deleted: %s", path)
			continue
		}
		if err := os.Remove(path); err != nil {
			s.printf("   Could not delete %s: %s", path, strerror(err))
			continue
		}
		s.say("deleted: %s", path)
	}
	if s.windows() {
		s.RemoveUserPath(filepath.Dir(s.CommandPath()))
	}
	os.Remove(RecordsPath())
	os.Remove(filepath.Join(platform.ConfigDir(), "daemon.lock")) // Windows: still locked by a daemon that did not stop
	if user != "" {
		answer := "y"
		if !a.Yes {
			text, err := s.Input(fmt.Sprintf("Delete the password and TOTP secret for %s from the keyring? [y/N] ", user))
			if err != nil {
				s.printf("") // no terminal
				text = "n"
			}
			answer = strings.ToLower(strings.TrimSpace(text))
		}
		if slices.Contains([]string{"j", "ja", "y", "yes"}, answer) {
			deleted, err := s.DeletePassword(user)
			if err != nil {
				return 1, err
			}
			s.say("%s", pick(deleted, "Password deleted", "Password was not in the keyring"))
			if deleted, err = s.DeleteTOTP(user); err != nil {
				return 1, err
			}
			s.say("%s", pick(deleted, "TOTP secret deleted", "TOTP secret was not in the keyring"))
		}
	}
	app := realpath(s.Root)
	// The copy get.sh/get.ps1 made goes too; a git clone is the user's own. After a failure it
	// stays, so that the uninstall can run again.
	if !failed && app == realpath(platform.AppInstallDir()) && !exists(filepath.Join(app, ".git")) {
		if s.windows() {
			moveAside(s.Binary)
		}
		os.RemoveAll(app)
		if exists(app) {
			// For example the folder a shell is in, or a file still open on Windows.
			s.printf("   Could not delete everything in %s, remove it by hand", app)
		} else {
			s.say("deleted: %s", app)
			os.Remove(filepath.Dir(app))
		}
		s.printf("Left in place: packages (openconnect, ocproxy) and the log in %s", platform.StateDir())
	} else {
		s.printf("Left in place: packages (openconnect, ocproxy), the repo %s and the log in %s", app, platform.StateDir())
	}
	if failed {
		return 1, nil
	}
	return 0, nil
}

func pick(ok bool, yes, no string) string {
	if ok {
		return yes
	}
	return no
}

func realpath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return abs
}

// strerror renders an error like Python's OSError.strerror.
func strerror(err error) string {
	var pe *os.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	text := err.Error()
	if text == "" {
		return text
	}
	return strings.ToUpper(text[:1]) + text[1:]
}
