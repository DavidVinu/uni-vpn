// Package daemon is the state machine: demand, connecting, idle, error classes, resume. It
// mirrors uni_vpn/daemon.py.
//
// The Python core is single-threaded asyncio; here one mutex (mu) guards all state. Code
// that Python runs between two awaits runs with mu held, and mu is released around every
// call that Python awaits, so each interleaving the Go core allows is one asyncio allows too.
// The forwarder calls acquire and noteActivity from many goroutines at once.
package daemon

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DavidVinu/uni-vpn/internal/config"
	"github.com/DavidVinu/uni-vpn/internal/credentials"
	"github.com/DavidVinu/uni-vpn/internal/forwarder"
	"github.com/DavidVinu/uni-vpn/internal/httpapi"
	"github.com/DavidVinu/uni-vpn/internal/messages"
	"github.com/DavidVinu/uni-vpn/internal/pac"
	"github.com/DavidVinu/uni-vpn/internal/platform"
	"github.com/DavidVinu/uni-vpn/internal/pyjson"
	"github.com/DavidVinu/uni-vpn/internal/socks"
	"github.com/DavidVinu/uni-vpn/internal/sysproxy"
	"github.com/DavidVinu/uni-vpn/internal/totp"
	"github.com/DavidVinu/uni-vpn/internal/tunnel"
	unis "github.com/DavidVinu/uni-vpn/internal/universities"
	"github.com/DavidVinu/uni-vpn/internal/winsys"
	"github.com/DavidVinu/uni-vpn/internal/wintunnel"
)

// Version and Protocol are __version__ and PROTOCOL of uni_vpn/__init__.py (a test compares).
const (
	Version  = "0.1.0"
	Protocol = 1
)

// State is the daemon state; the names are the status.json values.
type State string

const (
	Idle          State = "idle"
	Offline       State = "offline"
	Blocked       State = "blocked"
	Connecting    State = "connecting"
	Connected     State = "connected"
	Disconnecting State = "disconnecting"
	AuthFailed    State = "auth_failed"
	Keyring       State = "keyring"
	Error         State = "error"
)

func (s State) final() bool { return s == AuthFailed || s == Keyring }
func (s State) retry() bool { return s == Offline || s == Blocked || s == Error }

// OTPStep is the seconds per one-time code (RFC 6238, same as openconnect).
const OTPStep = 30

// Tunnel is one openconnect run: *tunnel.Tunnel on POSIX, *wintunnel.Tunnel on Windows.
type Tunnel interface {
	Start(password []byte, totp string) error
	WaitReady(timeout time.Duration) bool
	Stop(grace time.Duration)
	Exited() <-chan struct{}
	HasExited() bool
	StoppedByUs() bool
	Classification() (tunnel.Verdict, bool)
	ReturnCode() (int, bool)
	OTPGeneratedAt() time.Time
	Port() int
	StartedAt() time.Time
	ReadyAt() time.Time
}

// Process is the installer the Repair button started.
type Process interface {
	// Poll returns the exit code once the process ended.
	Poll() (code int, done bool)
	// Wait blocks until the process ended and returns its exit code.
	Wait() int
}

// Options are the constructor arguments of the Python Daemon; nil or "" means the default.
type Options struct {
	PasswordGetter func(ctx context.Context) ([]byte, error)
	PasswordSetter func(password string) error
	TOTPGetter     func(ctx context.Context) ([]byte, error)
	TOTPSetter     func(token string) error
	Probe          func(ctx context.Context) bool
	CiscoCheck     func() bool
	TunnelFactory  func() (Tunnel, error)
	ConfigError    string
	ConfigPath     string
	// No config.toml yet: the status page shows the setup assistant instead of an error.
	NeedsSetup   bool
	LogTail      func() []string
	Wrapper      string
	TokenDir     string
	DomainsPath  string
	ProxyRefresh func(httpPort int)
	RepairStart  func() (Process, error)
	// Elevated is is_admin() on Windows, nil elsewhere (status "elevated": null).
	Elevated func() *bool
}

type lastError struct {
	message string
	at      float64
}

// Daemon is the service: forwarder, status API, connect loop and ticker.
type Daemon struct {
	log  *slog.Logger
	opts Options

	// Test hooks: the wall clock, and _otp_wait.
	wall    func() float64
	otpWait func(now float64) float64
	base    time.Time // monotonic clock origin

	mu            sync.Mutex
	cfg           *config.Config
	configError   string
	configPath    string
	needsSetup    bool
	state         State
	message       messages.Message
	since         float64
	lastError     *lastError
	failures      int
	explicit      bool
	demandUntil   float64
	lastActivity  float64
	connectCount  int
	lastOTPStep   int64
	hasOTPStep    bool
	pausedByCisco bool
	tun           Tunnel
	secretsGen    int
	disconnects   int
	changed       chan struct{}
	wake          chan struct{}
	wakeSet       bool
	loopRunning   bool
	loopCancel    context.CancelFunc
	loopDone      chan struct{}
	repairProc    Process
	http          *httpapi.Server

	// RestartRequested is set when the program files were replaced and the caller should
	// start the new code. The Go core does not update itself, so it stays false.
	RestartRequested bool

	forwarder *forwarder.Forwarder
	closeAll  func() // forwarder.CloseAll; tests replace it before Run
	stop      chan struct{}
	stopOnce  sync.Once
	started   chan struct{}
}

// New builds the daemon; Run starts it.
func New(cfg *config.Config, log *slog.Logger, opts Options) *Daemon {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	d := &Daemon{log: log, cfg: cfg, base: time.Now(), changed: make(chan struct{}), wake: make(chan struct{}),
		stop: make(chan struct{}), started: make(chan struct{})}
	d.wall = func() float64 { return float64(time.Now().UnixNano()) / 1e9 }
	if opts.PasswordGetter == nil {
		opts.PasswordGetter = func(ctx context.Context) ([]byte, error) {
			user, timeout := d.keyringArgs()
			return credentials.GetPassword(ctx, user, timeout)
		}
	}
	if opts.PasswordSetter == nil {
		opts.PasswordSetter = func(password string) error { return credentials.StorePassword(d.user(), password) }
	}
	if opts.TOTPGetter == nil {
		opts.TOTPGetter = func(ctx context.Context) ([]byte, error) {
			user, timeout := d.keyringArgs()
			return credentials.GetTotp(ctx, user, timeout)
		}
	}
	if opts.TOTPSetter == nil {
		opts.TOTPSetter = func(token string) error { return credentials.StoreTotp(d.user(), token) }
	}
	if opts.TokenDir == "" {
		opts.TokenDir = platform.StateDir()
	}
	if opts.DomainsPath == "" {
		opts.DomainsPath = pac.DomainsPath()
	}
	if opts.ProxyRefresh == nil {
		opts.ProxyRefresh = sysproxy.Refresh
	}
	if opts.Probe == nil {
		host, timeout := cfg.Host, seconds(cfg.ProbeTimeout)
		opts.Probe = func(ctx context.Context) bool { return DefaultProbe(ctx, host, timeout) }
	}
	if opts.CiscoCheck == nil {
		opts.CiscoCheck = platform.CiscoConnected
	}
	if opts.TunnelFactory == nil {
		opts.TunnelFactory = d.makeTunnel
	}
	if opts.ConfigPath == "" {
		opts.ConfigPath = cfg.Path
	}
	if opts.LogTail == nil {
		opts.LogTail = func() []string { return nil }
	}
	if opts.Wrapper == "" {
		opts.Wrapper = filepath.Join(BinDir(), "uni-vpn-ocproxy")
	}
	if opts.RepairStart == nil {
		opts.RepairStart = StartRepairProcess
	}
	if opts.Elevated == nil {
		opts.Elevated = func() *bool {
			if !platform.IsWindows {
				return nil
			}
			admin := winsys.IsAdmin()
			return &admin
		}
	}
	d.opts = opts
	d.configError, d.configPath, d.needsSetup = opts.ConfigError, opts.ConfigPath, opts.NeedsSetup
	d.state, d.message, d.since = Idle, messages.NotConnected, d.wall()
	d.lastActivity = d.mono()
	d.forwarder = forwarder.New("127.0.0.1", cfg.SocksPort, d.acquire, d.noteActivity, seconds(cfg.HalfcloseGrace), log)
	d.closeAll = d.forwarder.CloseAll
	if opts.NeedsSetup {
		d.message = messages.SetupNeeded
	} else if opts.ConfigError != "" {
		// The setup assistant is the way out: it writes a new file and keeps the broken one.
		d.logf(slog.LevelError, "Configuration error: %s", opts.ConfigError)
		d.mu.Lock()
		d.set(Error, messages.SettingsBroken)
		d.mu.Unlock()
	}
	return d
}

func seconds(f float64) time.Duration { return time.Duration(f * float64(time.Second)) }

func (d *Daemon) logf(level slog.Level, format string, args ...any) {
	d.log.Log(context.Background(), level, fmt.Sprintf(format, args...))
}

// mono is time.monotonic(): seconds on a clock that only moves forward.
func (d *Daemon) mono() float64 { return time.Since(d.base).Seconds() + 1e6 }

func (d *Daemon) user() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cfg.User
}

func (d *Daemon) keyringArgs() (string, time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cfg.User, seconds(d.cfg.KeyringTimeout)
}

// DefaultProbe is a TLS handshake with host:443 using the system trust store. False behind a
// captive portal or without network.
func DefaultProbe(ctx context.Context, host string, timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	dialer := &tls.Dialer{Config: &tls.Config{ServerName: host}}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, "443"))
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// BinDir holds the helper files of an installation (uni-vpn-ocproxy, uni-vpn-vpnc.js). The Go
// core keeps using bin/ of the installation: next to its own executable, else bin/ next to the
// directory it is in (repo/bin), else the installed program's bin/.
func BinDir() string {
	marker := "uni-vpn-ocproxy"
	if platform.IsWindows {
		marker = "uni-vpn-vpnc.js"
	}
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		dirs = append(dirs, filepath.Dir(exe), filepath.Join(filepath.Dir(filepath.Dir(exe)), "bin"))
	}
	dirs = append(dirs, filepath.Join(platform.AppInstallDir(), "bin"))
	for _, dir := range dirs {
		if st, err := os.Stat(filepath.Join(dir, marker)); err == nil && st.Mode().IsRegular() {
			return dir
		}
	}
	return dirs[0]
}

// notFoundError is Python's FileNotFoundError with only a message.
type notFoundError struct{ msg string }

func (e *notFoundError) Error() string { return e.msg }
func (e *notFoundError) Is(target error) bool {
	return target == fs.ErrNotExist
}

// socksServer adapts socks.Server to what the Windows tunnel expects.
type socksServer struct{ *socks.Server }

func (s socksServer) Update(source string, dns []string) { s.SetRoute(source, dns) }

func (d *Daemon) makeTunnel() (Tunnel, error) {
	cfg := d.cfg // called with mu held
	openconnect := platform.FindBinary("openconnect", cfg.OpenConnect)
	if openconnect == "" {
		return nil, &notFoundError{"openconnect not found"}
	}
	tc := tunnel.Config{Host: cfg.Host, UserAgent: cfg.Useragent, LoginName: cfg.LoginName(), AuthGroup: cfg.Authgroup,
		UserGroup: cfg.Usergroup, OS: cfg.OS, NoExternalAuth: cfg.NoExternalAuth, MFA: cfg.MFA,
		TOTPSeparator: cfg.TOTPSeparator, StopGrace: seconds(cfg.StopGrace)}
	if platform.IsWindows {
		w := wintunnel.New(tc, openconnect, filepath.Join(BinDir(), "uni-vpn-vpnc.js"), d.log, d.opts.TokenDir)
		w.TOTPCode = totp.CodeAt
		w.NewSocks = func(port int, source string, dns []string) (wintunnel.SocksServer, error) {
			server := socks.NewServer(port, source, dns, d.log)
			if err := server.Start(context.Background()); err != nil {
				return nil, err
			}
			return socksServer{server}, nil
		}
		return w, nil
	}
	ocproxy := platform.FindBinary("ocproxy", cfg.OCProxy)
	if ocproxy == "" {
		return nil, &notFoundError{"ocproxy not found"}
	}
	t := tunnel.New(tc, openconnect, d.opts.Wrapper, d.log, ocproxy, d.opts.TokenDir)
	t.TOTPCode = totp.CodeAt
	return t, nil
}

// set is _set; mu must be held.
func (d *Daemon) set(state State, message messages.Message) {
	if state != d.state || message != d.message {
		d.logf(slog.LevelInfo, "State %s -> %s: %s", d.state, state, message.Text)
	}
	d.state = state
	d.message = message
	d.since = d.wall()
	if state == Error || state == AuthFailed || state == Keyring {
		d.lastError = &lastError{message.Text, d.since}
	}
	close(d.changed)
	d.changed = make(chan struct{})
}

func (d *Daemon) final(state State, message messages.Message) {
	d.explicit = false
	d.set(state, message)
}

func (d *Daemon) noteActivity() {
	d.mu.Lock()
	d.noteActivityLocked()
	d.mu.Unlock()
}

func (d *Daemon) noteActivityLocked() {
	now := d.mono()
	d.lastActivity = now
	d.demandUntil = math.Max(d.demandUntil, now+d.cfg.DemandWindow)
}

func (d *Daemon) hasDemand() bool {
	return d.explicit || d.forwarder.Active() > 0 || d.mono() < d.demandUntil
}

// StateName is the current state for API answers.
func (d *Daemon) StateName() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return string(d.state)
}

// --- Status ---------------------------------------------------------------

func platformName() string {
	switch {
	case platform.IsWindows:
		return "windows"
	case platform.IsMacOS:
		return "macos"
	}
	return "linux"
}

// idleMinutes is the value as Python has it: config.toml's 15 is an int, the default 15.0 a float.
func idleMinutes(cfg *config.Config) any {
	if cfg.IdleMinutesInt {
		return int64(cfg.IdleMinutes)
	}
	return cfg.IdleMinutes
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Status is the /status.json object, keys in Python's order.
func (d *Daemon) Status() (pyjson.Object, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	cfg := d.cfg
	domains, err := pac.ReadDomains(d.opts.DomainsPath, cfg.DefaultDomains)
	if err != nil {
		return nil, err
	}
	var lastErr any
	if d.lastError != nil {
		lastErr = pyjson.O("message", d.lastError.message, "at", d.lastError.at)
	}
	tail := d.opts.LogTail()
	if len(tail) > 30 {
		tail = tail[len(tail)-30:]
	}
	pacRefresh := "auto"
	if platform.IsMacOS {
		pacRefresh = "manual"
	}
	var elevated any
	if e := d.opts.Elevated(); e != nil {
		elevated = *e
	}
	return pyjson.O(
		"protocol", Protocol,
		"version", Version,
		// No self-update in the Go core: no commit, nothing pending.
		"commit", nil,
		"auto_update", cfg.AutoUpdate,
		"update_pending", nil,
		"state", string(d.state),
		"message", d.message.Text,
		// For translations and the app's fix button, see messages.
		"message_id", nullable(d.message.ID),
		"action", nullable(d.actionLocked()),
		"since", d.since,
		"host", cfg.Host,
		"user", cfg.User,
		"university", cfg.University,
		"university_name", cfg.UniversityName,
		"mfa", cfg.MFA,
		"mfa_portal_url", cfg.MFAPortalURL,
		"mfa_steps", append([]string{}, cfg.MFASteps...),
		"socks_port", cfg.SocksPort,
		"http_port", cfg.HTTPPort,
		"idle_minutes", idleMinutes(cfg),
		"active_connections", d.forwarder.Active(),
		"bytes_in", d.forwarder.BytesIn(),
		"bytes_out", d.forwarder.BytesOut(),
		"connects", d.connectCount,
		"last_error", lastErr,
		"domains", domains,
		"pac_url", sysproxy.PacURL(cfg.HTTPPort),
		"pac_refresh", pacRefresh,
		"log_tail", append([]string{}, tail...),
		"setup_needed", d.needsSetup || d.configError != "",
		"repairing", d.repairingLocked(),
		// A connect loop is running (also while retrying in offline, blocked or error).
		"busy", d.loopRunning,
		"error_kind", nullable(d.errorKindLocked()),
		"platform", platformName(),
		"elevated", elevated,
	), nil
}

// actionLocked is the fix the app offers for the current message, "" if there is none.
func (d *Daemon) actionLocked() string {
	if d.repairingLocked() {
		return ""
	}
	return d.message.Action
}

// errorKindLocked: which factor the current error is about, so the page can offer the right fix.
func (d *Daemon) errorKindLocked() string {
	if !d.state.final() {
		return ""
	}
	if a := d.actionLocked(); a == "password" || a == "totp" {
		return a
	}
	return ""
}

// --- Repair -----------------------------------------------------------------

func (d *Daemon) repairingLocked() bool {
	if d.repairProc == nil {
		return false
	}
	_, done := d.repairProc.Poll()
	return !done
}

// StartRepair runs the installer again (the Repair button). It reinstalls what is missing and
// restarts the service; the operating system asks for permission where it needs to.
func (d *Daemon) StartRepair() {
	d.mu.Lock()
	if d.repairingLocked() {
		d.mu.Unlock()
		return
	}
	d.mu.Unlock()
	d.logf(slog.LevelInfo, "Repair requested")
	process, err := d.opts.RepairStart()
	d.mu.Lock()
	defer d.mu.Unlock()
	if err != nil {
		d.logf(slog.LevelError, "Repair could not start: %s", pyErr(err))
		d.set(d.state, messages.RepairFailed)
		return
	}
	d.repairProc = process
	d.set(d.state, messages.Repairing)
	go d.watchRepair(process)
}

func (d *Daemon) watchRepair(process Process) {
	// A successful repair restarts the service, so normally this daemon ends before it.
	code := process.Wait()
	d.logf(slog.LevelInfo, "Repair finished with exit code %d", code)
	d.mu.Lock()
	defer d.mu.Unlock()
	if code != 0 && d.message == messages.Repairing {
		d.set(d.state, messages.RepairFailed)
	} else if code == 0 && d.message == messages.Repairing {
		d.set(Idle, messages.NotConnected)
	}
}

// --- Setup and secrets -----------------------------------------------------

// CompleteSetup is the setup assistant, on first run or from Settings: config.toml, the
// secrets, then a test connection. token "" means none. The error is a *unis.FieldError
// naming the step to change, a *httpapi.ValueError, or any other failure.
func (d *Daemon) CompleteSetup(user, password, token, university string, overrides []config.Setting) error {
	user = httpapi.PyStrip(user)
	if !config.ValidUser(user) {
		return &unis.FieldError{Field: "user", Message: "Invalid university ID"}
	}
	checked, err := config.CheckOverrides(overrides)
	if err != nil {
		return err
	}
	profile, err := config.ProfileConfig(university, checked)
	if err != nil {
		return err
	}
	if profile.MFA == "saml" {
		return &unis.FieldError{Field: "university", Message: tunnel.SAMLRequired}
	}
	if profile.NeedsTOTP() && token == "" {
		return &unis.FieldError{Field: "totp", Message: "Paste the secret first"}
	}
	d.mu.Lock()
	path := d.configPath
	if path == "" {
		path = config.DefaultPath()
	}
	// Secrets first: if the keyring refuses, no config.toml exists yet and the assistant
	// stays the way in after a restart.
	previousUser, previousUniversity := d.cfg.User, d.cfg.University
	// Settings, Account runs the assistant again: the tunnel of the old account goes.
	teardown := !d.needsSetup && d.tun != nil
	d.mu.Unlock()
	if teardown {
		d.RequestDisconnect()
	}
	d.mu.Lock()
	d.cfg.User = user
	configError := d.configError
	ports := []config.Setting{}
	defaults := config.Default()
	if d.cfg.SocksPort != defaults.SocksPort {
		ports = append(ports, config.Setting{Key: "socks_port", Value: d.cfg.SocksPort})
	}
	if d.cfg.HTTPPort != defaults.HTTPPort {
		ports = append(ports, config.Setting{Key: "http_port", Value: d.cfg.HTTPPort})
	}
	d.mu.Unlock()
	err = func() error {
		if err := d.opts.PasswordSetter(password); err != nil {
			return err
		}
		if profile.NeedsTOTP() {
			if err := d.opts.TOTPSetter(token); err != nil {
				return err
			}
		}
		broken := configError != "" && exists(path)
		if broken {
			// Unreadable: start over, but keep the old file for whoever wants to look at it.
			backup := path + ".broken"
			if err := os.Rename(path, backup); err != nil {
				return err
			}
			d.logf(slog.LevelWarn, "Unreadable %s moved to %s", path, backup)
		}
		if exists(path) {
			if university != previousUniversity {
				var stale []string
				for _, key := range unis.ProfileFields {
					if !slices.ContainsFunc(checked, func(s config.Setting) bool { return s.Key == key }) {
						stale = append(stale, key)
					}
				}
				if err := config.RemoveKeys(path, stale); err != nil {
					return err
				}
			}
			values := append([]config.Setting{{Key: "user", Value: user}, {Key: "university", Value: university}}, checked...)
			return config.SetValues(path, values)
		}
		if err := config.WriteInitial(path, user, university, checked); err != nil {
			return err
		}
		// Ports the old file set stay: the system's proxy rule and this daemon use them.
		if broken && len(ports) > 0 {
			return config.SetValues(path, ports)
		}
		return nil
	}()
	if err != nil {
		d.mu.Lock()
		d.cfg.User = previousUser
		d.mu.Unlock()
		var ce *config.ConfigError
		if errors.As(err, &ce) {
			return &httpapi.ValueError{Msg: ce.Error()}
		}
		return errors.New(pyErr(err))
	}
	d.mu.Lock()
	config.CopyProfile(d.cfg, &profile)
	d.configPath = path
	d.logf(slog.LevelInfo, "Setup completed for %s (%s)", user, profile.UniversityName)
	d.needsSetup = false
	d.configError = ""
	d.mu.Unlock()
	d.secretsChanged()
	return nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// NoteCodeShown: a check code was shown, the user may type it into the MFA portal, which
// uses it up.
func (d *Daemon) NoteCodeShown() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lastOTPStep, d.hasOTPStep = int64(math.Floor(d.wall()/OTPStep)), true
}

// secretsChanged: new password or TOTP secret, an attempt still running with the old ones
// starts over.
func (d *Daemon) secretsChanged() {
	d.mu.Lock()
	d.secretsGen++
	tun := d.tun
	if tun != nil && d.state == Connecting && !tun.HasExited() {
		d.logf(slog.LevelInfo, "Secrets changed while connecting, starting over")
		disconnects := d.disconnects
		grace := seconds(d.cfg.StopGrace)
		d.mu.Unlock()
		tun.Stop(grace)
		d.mu.Lock()
		if d.disconnects != disconnects {
			d.mu.Unlock()
			return // a Disconnect during the stop wins
		}
	}
	d.requestConnectLocked()
	d.mu.Unlock()
}

// PAC is the proxy rule for the browsers.
func (d *Daemon) PAC() (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	domains, err := pac.ReadDomains(d.opts.DomainsPath, d.cfg.DefaultDomains)
	if err != nil {
		return "", err
	}
	return pac.BuildPAC(domains, d.cfg.SocksPort), nil
}

// SetDomains saves the website list. Invalid lines are a *httpapi.ValueError.
func (d *Daemon) SetDomains(text string) ([]string, error) {
	domains, errs := pac.ParseDomainList(text)
	if len(errs) > 0 {
		return nil, &httpapi.ValueError{Msg: strings.Join(errs, "\n")}
	}
	if err := pac.WriteDomains(d.opts.DomainsPath, domains); err != nil {
		return nil, errors.New(pyErr(err))
	}
	list := strings.Join(domains, ", ")
	if list == "" {
		list = "(empty)"
	}
	d.logf(slog.LevelInfo, "Domain list saved: %s", list)
	d.mu.Lock()
	port := d.cfg.HTTPPort
	d.mu.Unlock()
	d.opts.ProxyRefresh(port)
	return domains, nil
}

// SetAutoUpdate stores the setting. The Go core has no updater, so nothing else happens.
func (d *Daemon) SetAutoUpdate(enabled bool) error {
	d.mu.Lock()
	path, needsSetup := d.configPath, d.needsSetup
	d.mu.Unlock()
	if needsSetup || path == "" || !exists(path) {
		return &httpapi.ValueError{Msg: "Finish the setup first"}
	}
	if err := config.SetValues(path, []config.Setting{{Key: "auto_update", Value: enabled}}); err != nil {
		var ce *config.ConfigError
		if errors.As(err, &ce) {
			return err
		}
		return errors.New(pyErr(err))
	}
	d.mu.Lock()
	d.cfg.AutoUpdate = enabled
	d.mu.Unlock()
	word := "off"
	if enabled {
		word = "on"
	}
	d.logf(slog.LevelInfo, "Automatic updates %s", word)
	return nil
}

// --- Lifecycle ---------------------------------------------------------------

// Started is closed once Run has opened its ports.
func (d *Daemon) Started() <-chan struct{} { return d.started }

// HTTP is the status API, nil if its port was not available.
func (d *Daemon) HTTP() *httpapi.Server {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.http
}

// Run serves until Stop.
func (d *Daemon) Run() {
	if stale := tunnel.RemoveStaleTokenFiles(d.opts.TokenDir); stale > 0 {
		d.logf(slog.LevelWarn, "Removed %d stale secret file(s)", stale)
	}
	if platform.IsWindows {
		wintunnel.RemoveStaleStateFiles(d.opts.TokenDir)
	}
	d.mu.Lock()
	httpPort := d.cfg.HTTPPort
	d.mu.Unlock()
	server := httpapi.New(d, "127.0.0.1", httpPort, d.log)
	if err := server.Start(); err != nil {
		d.logf(slog.LevelError, "Status port %d not available: %s", httpPort, bindErrorString(err, httpPort))
		server = nil
	}
	d.mu.Lock()
	d.http = server
	d.mu.Unlock()
	d.bindForwarder()
	tickerDone := make(chan struct{})
	go func() { d.ticker(); close(tickerDone) }()
	close(d.started)
	<-d.stop
	<-tickerDone
	d.mu.Lock()
	if d.loopRunning {
		d.loopCancel()
	}
	loopDone, tun, grace := d.loopDone, d.tun, seconds(d.cfg.StopGrace)
	d.mu.Unlock()
	if tun != nil {
		tun.Stop(grace)
	}
	d.forwarder.Stop()
	if server != nil {
		server.Stop()
	}
	if loopDone != nil {
		select {
		case <-loopDone:
		case <-time.After(5 * time.Second):
		}
	}
}

// Stop ends Run.
func (d *Daemon) Stop() { d.stopOnce.Do(func() { close(d.stop) }) }

// bindForwarder opens the browser's port. If another program holds it, the ticker tries again
// until it is free.
func (d *Daemon) bindForwarder() {
	d.mu.Lock()
	retry := d.message == messages.PortInUse
	port := d.cfg.SocksPort
	d.mu.Unlock()
	err := d.forwarder.Start(context.Background())
	d.mu.Lock()
	defer d.mu.Unlock()
	if err != nil {
		if !retry {
			d.logf(slog.LevelError, "SOCKS port %d not available: %s", port, bindErrorString(err, port))
			d.set(Error, messages.PortInUse)
		}
		return
	}
	if retry {
		d.logf(slog.LevelInfo, "SOCKS port %d is free again", port)
		d.set(Idle, messages.NotConnected)
	}
}

// --- Demand ------------------------------------------------------------------

// acquire is called by the forwarder for each browser connection. Returns the ocproxy port.
func (d *Daemon) acquire(ctx context.Context) (int, bool) {
	d.mu.Lock()
	d.noteActivityLocked()
	if d.configError != "" {
		d.mu.Unlock()
		return 0, false
	}
	deadline := d.mono() + d.cfg.ClientWait
	for {
		state := d.state
		if state == Connected && d.tun != nil {
			port := d.tun.Port()
			d.mu.Unlock()
			return port, true
		}
		if state.final() {
			d.mu.Unlock()
			return 0, false
		}
		if state.retry() {
			d.ensureLoop()
			d.mu.Unlock()
			return 0, false
		}
		if state == Idle {
			d.ensureLoop()
		}
		remaining := deadline - d.mono()
		if remaining <= 0 {
			d.mu.Unlock()
			return 0, false
		}
		changed := d.changed
		d.mu.Unlock()
		timer := time.NewTimer(seconds(remaining))
		select {
		case <-changed:
			timer.Stop()
		case <-timer.C:
			return 0, false
		case <-ctx.Done():
			timer.Stop()
			return 0, false
		}
		d.mu.Lock()
	}
}

// ensureLoop starts the connect loop unless it runs; mu must be held. The loop takes mu
// before its first step, like a task that starts once the caller yields.
func (d *Daemon) ensureLoop() {
	if d.loopRunning {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	d.loopRunning, d.loopCancel, d.loopDone = true, cancel, done
	go func() {
		d.mu.Lock()
		d.connectLoop(ctx)
		// Done in the same step as the last state change, like an asyncio task.
		d.loopRunning = false
		d.mu.Unlock()
		cancel()
		close(done)
	}()
}

// sleep is _sleep: wait until the time passed or wakeUp is called; mu is held on entry and exit.
func (d *Daemon) sleep(ctx context.Context, secs float64) {
	d.wake = make(chan struct{})
	d.wakeSet = false
	wake := d.wake
	d.mu.Unlock()
	timer := time.NewTimer(seconds(math.Max(secs, 0)))
	select {
	case <-wake:
	case <-timer.C:
	case <-ctx.Done():
	}
	timer.Stop()
	d.mu.Lock()
}

func (d *Daemon) wakeUp() {
	if !d.wakeSet {
		d.wakeSet = true
		close(d.wake)
	}
}

// RequestConnect is "connect now".
func (d *Daemon) RequestConnect() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.requestConnectLocked()
}

func (d *Daemon) requestConnectLocked() {
	if d.configError != "" {
		return
	}
	d.explicit = true
	d.failures = 0
	if d.state.final() || d.state.retry() {
		d.set(Idle, messages.NotConnected)
	}
	d.noteActivityLocked()
	d.ensureLoop()
	d.wakeUp()
}

// RequestDisconnect tears the tunnel down, or ends the attempt in progress.
func (d *Daemon) RequestDisconnect() {
	d.mu.Lock()
	d.disconnects++
	d.explicit = false
	d.demandUntil = 0
	d.wakeUp()
	if tun := d.tun; tun != nil {
		d.set(Disconnecting, messages.Disconnecting)
		grace := seconds(d.cfg.StopGrace)
		d.mu.Unlock()
		d.closeAll()
		tun.Stop(grace) // the loop may have dropped d.tun meanwhile
		return
	}
	if d.loopRunning {
		d.loopCancel()
		done := d.loopDone
		d.mu.Unlock()
		<-done
		d.mu.Lock()
	}
	if !d.state.final() {
		d.set(Idle, messages.NotConnected)
	}
	d.mu.Unlock()
}

// SetPassword stores a new password and connects with it.
func (d *Daemon) SetPassword(password string) error {
	if err := d.opts.PasswordSetter(password); err != nil {
		return err
	}
	d.logf(slog.LevelInfo, "Password stored in the keyring")
	d.secretsChanged()
	return nil
}

// SetTOTP stores a new TOTP secret and connects with it.
func (d *Daemon) SetTOTP(token string) error {
	if err := d.opts.TOTPSetter(token); err != nil {
		return err
	}
	d.logf(slog.LevelInfo, "TOTP secret stored in the keyring")
	d.NoteCodeShown() // the page shows the new check code
	d.secretsChanged()
	return nil
}

// --- Connecting ----------------------------------------------------------------

// unlocked runs f without mu, like an await.
func (d *Daemon) unlocked(f func()) {
	d.mu.Unlock()
	defer d.mu.Lock()
	f()
}

func (d *Daemon) ciscoCheck(ctx context.Context) bool {
	// In a goroutine: "vpn state" takes 2 s on macOS, and a cancel must not wait for it.
	result := make(chan bool, 1)
	go func() { result <- d.opts.CiscoCheck() }()
	select {
	case r := <-result:
		return r
	case <-ctx.Done():
		return false
	}
}

func isASCII(b []byte) (int, bool) {
	for i, c := range b {
		if c >= 0x80 {
			return i, false
		}
	}
	return 0, true
}

// connectLoop is _connect_loop; mu is held on entry and exit, and released around every
// blocking call. After each of them a cancelled ctx ends the loop at once, like a
// CancelledError raised at the await.
func (d *Daemon) connectLoop(ctx context.Context) {
	for d.hasDemand() {
		cfg := d.cfg
		if e := d.opts.Elevated(); e != nil && !*e {
			d.final(Error, messages.NotElevated)
			return
		}
		if cfg.MFA == "saml" {
			d.final(AuthFailed, messages.SAMLRequired)
			return
		}
		var cisco bool
		d.unlocked(func() { cisco = d.ciscoCheck(ctx) })
		if ctx.Err() != nil {
			return
		}
		if cisco {
			d.set(Blocked, messages.Blocked)
			d.sleep(ctx, cfg.RetryInterval)
			if ctx.Err() != nil {
				return
			}
			continue
		}
		var online bool
		d.unlocked(func() { online = d.opts.Probe(ctx) })
		if ctx.Err() != nil {
			return
		}
		if !online {
			d.set(Offline, messages.Offline)
			d.sleep(ctx, cfg.RetryInterval)
			if ctx.Err() != nil {
				return
			}
			continue
		}
		if wait := d.otpWaitAt(d.wall()); wait > 0 {
			d.set(Connecting, messages.WaitingForCode)
			d.sleep(ctx, wait)
			if ctx.Err() != nil {
				return
			}
			continue // checks Cisco, network and demand again, then fetches the secrets
		}
		generation := d.secretsGen
		var password []byte
		var err error
		d.unlocked(func() { password, err = d.opts.PasswordGetter(ctx) })
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			var missing *credentials.MissingError
			var locked *credentials.LockedError
			switch {
			case errors.As(err, &missing):
				d.final(Keyring, messages.PasswordMissing)
			case errors.As(err, &locked):
				d.final(Keyring, messages.KeyringLocked)
			default:
				d.logf(slog.LevelError, "Password not readable: %s", err)
				d.final(Keyring, messages.PasswordUnreadable)
			}
			return
		}
		secret := ""
		if cfg.NeedsTOTP() {
			var raw []byte
			d.unlocked(func() { raw, err = d.opts.TOTPGetter(ctx) })
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				var missing *credentials.MissingError
				var locked *credentials.LockedError
				switch {
				case errors.As(err, &missing):
					d.final(Keyring, messages.TOTPMissing)
				case errors.As(err, &locked):
					d.final(Keyring, messages.KeyringLocked)
				default:
					d.logf(slog.LevelError, "TOTP secret not readable: %s", err)
					d.final(Keyring, messages.TOTPUnreadable)
				}
				return
			}
			if pos, ok := isASCII(raw); !ok {
				d.logf(slog.LevelError, "TOTP secret not readable: 'ascii' codec can't decode byte 0x%02x in position %d: "+
					"ordinal not in range(128)", raw[pos], pos)
				d.final(Keyring, messages.TOTPUnreadable)
				return
			}
			secret = httpapi.PyStrip(string(raw))
			if secret == "" {
				d.final(Keyring, messages.TOTPMissing)
				return
			}
		}

		d.set(Connecting, messages.Connecting)
		tun, err := d.opts.TunnelFactory()
		if err == nil {
			d.tun = tun
			d.unlocked(func() { err = tun.Start(password, secret) })
		}
		if err != nil {
			d.tun = nil
			var pe *tunnel.PasswordEncodingError
			var te *tunnel.TOTPSecretError
			switch {
			case errors.As(err, &pe):
				d.logf(slog.LevelError, "%s", err)
				d.final(Keyring, messages.PasswordUnsupported)
			case errors.As(err, &te):
				// Like openconnect's "Soft token string is invalid" with totp_field.
				d.logf(slog.LevelError, "TOTP secret unusable: %s", err)
				d.final(AuthFailed, messages.TOTPUnusable)
			default:
				d.logf(slog.LevelError, "Could not start openconnect: %s", pyErr(err))
				if errors.Is(err, fs.ErrNotExist) {
					d.final(Error, messages.ProgramMissing)
				} else {
					d.final(Error, messages.StartFailed)
				}
			}
			return
		}
		if ctx.Err() != nil {
			return
		}
		d.connectCount++
		// openconnect generates the code right after starting; record the window now, because
		// the output reader can lag behind the port check (macOS CI).
		d.lastOTPStep, d.hasOTPStep = int64(math.Floor(d.wall()/OTPStep)), true

		var ready bool
		d.unlocked(func() { ready = tun.WaitReady(seconds(cfg.ReadyTimeout)) })
		if ctx.Err() != nil {
			return
		}
		if at := tun.OTPGeneratedAt(); !at.IsZero() {
			d.lastOTPStep = int64(math.Floor(float64(at.UnixNano()) / 1e9 / OTPStep))
		}
		if !ready && generation != d.secretsGen {
			// The secrets changed while connecting: this result says nothing about the new ones.
			if !tun.HasExited() {
				d.unlocked(func() { tun.Stop(seconds(cfg.StopGrace)) })
				if ctx.Err() != nil {
					return
				}
			}
			d.tun = nil
			if tun.StoppedByUs() && d.state == Disconnecting {
				d.afterStop()
			}
			continue
		}
		if !ready {
			if tun.StoppedByUs() {
				d.tun = nil
				d.afterStop()
				continue // a RequestConnect in the meantime takes effect via hasDemand()
			}
			var state State
			var message messages.Message
			if !tun.HasExited() {
				d.unlocked(func() { tun.Stop(seconds(cfg.StopGrace)) })
				if ctx.Err() != nil {
					return
				}
				state, message = Error, messages.TooSlow
			} else if v, ok := tun.Classification(); ok {
				state, message = State(v.State), messages.ByText(v.Message)
			} else if code, _ := tun.ReturnCode(); code == 1 {
				d.logf(slog.LevelError, "Login failed (openconnect exit code 1)")
				state, message = AuthFailed, messages.AuthRejected
			} else {
				d.logf(slog.LevelError, "openconnect exited with code %s", returnCode(tun))
				state, message = Error, messages.ConnectFailed
			}
			d.tun = nil
			if state == AuthFailed {
				d.final(state, message)
				return
			}
			d.set(Error, message)
			if !d.backoff(ctx) {
				if ctx.Err() != nil {
					return
				}
				break
			}
			continue
		}

		d.failures = 0
		d.logf(slog.LevelInfo, "Tunnel ready after %.1f s", elapsed(tun.StartedAt(), tun.ReadyAt()))
		d.set(Connected, messages.Connected)
		// The "connect now" request is fulfilled. From here on only real use counts, the idle
		// timer handles the rest.
		d.explicit = false
		d.noteActivityLocked()
		d.unlocked(func() {
			select {
			case <-tun.Exited():
			case <-ctx.Done():
			}
		})
		if ctx.Err() != nil {
			return
		}
		d.tun = nil
		d.unlocked(d.closeAll)
		if ctx.Err() != nil {
			return
		}
		if tun.StoppedByUs() {
			d.afterStop()
			continue // a RequestConnect in the meantime takes effect via hasDemand()
		}
		v, classified := tun.Classification()
		if !classified {
			d.logf(slog.LevelWarn, "Tunnel dropped (exit code %s)", returnCode(tun))
		}
		message := messages.ConnectionLost
		if classified {
			message = messages.ByText(v.Message)
		}
		d.lastError = &lastError{message.Text, d.wall()}
		if !d.hasDemand() {
			d.set(Idle, message)
			return
		}
		d.set(Error, message)
		if !d.backoff(ctx) {
			if ctx.Err() != nil {
				return
			}
			break
		}
	}
	// `blocked` stays until the ticker sees Cisco disconnected, so the popup keeps explaining
	// why nothing works.
	switch d.state {
	case Offline, Error, Connecting, Disconnecting:
		d.set(Idle, messages.NotConnected)
	}
}

func elapsed(start, ready time.Time) float64 {
	if start.IsZero() || ready.IsZero() {
		return 0
	}
	return ready.Sub(start).Seconds()
}

func returnCode(tun Tunnel) string {
	if code, ok := tun.ReturnCode(); ok {
		return strconv.Itoa(code)
	}
	return "None"
}

func (d *Daemon) afterStop() {
	if d.pausedByCisco {
		d.pausedByCisco = false
		d.set(Blocked, messages.Blocked)
	} else {
		d.set(Idle, messages.Disconnected)
	}
}

// otpWaitAt: seconds until the next one-time code window, 0 if the current code is still unused.
func (d *Daemon) otpWaitAt(now float64) float64 {
	if d.otpWait != nil {
		return d.otpWait(now)
	}
	return d.realOTPWait(now)
}

func (d *Daemon) realOTPWait(now float64) float64 {
	if !d.cfg.NeedsTOTP() {
		return 0
	}
	if !d.hasOTPStep || int64(math.Floor(now/OTPStep)) != d.lastOTPStep {
		return 0
	}
	return OTPStep - math.Mod(now, OTPStep) + 0.5
}

func (d *Daemon) backoff(ctx context.Context) bool {
	delays := d.cfg.Backoff
	delay := delays[min(d.failures, len(delays)-1)] * (0.8 + 0.4*rand.Float64())
	d.failures++
	d.logf(slog.LevelInfo, "Retrying in %.1f s", delay)
	d.sleep(ctx, delay)
	return ctx.Err() == nil && d.hasDemand()
}

// --- Idle and resume -------------------------------------------------------------

func (d *Daemon) ticker() {
	// A clock that stops during sleep: the difference to the wall clock reveals a resume.
	awake := d.mono
	if platform.IsWindows {
		awake = winsys.AwakeSeconds
	}
	lastAwake, lastWall := awake(), d.wall()
	lastCisco := d.mono()
	lastBind := lastCisco
	d.mu.Lock()
	tick := seconds(d.cfg.Tick)
	d.mu.Unlock()
	timer := time.NewTimer(tick)
	defer timer.Stop()
	for {
		select {
		case <-d.stop:
			return
		case <-timer.C:
		}
		timer.Reset(tick)
		mono, wall, nowAwake := d.mono(), d.wall(), awake()
		jump := (wall - lastWall) - (nowAwake - lastAwake)
		lastAwake, lastWall = nowAwake, wall
		d.mu.Lock()
		cfg := d.cfg
		if d.forwarder.Addr() == nil && mono-lastBind >= cfg.RetryInterval {
			lastBind = mono
			d.unlocked(d.bindForwarder)
		}
		grace := seconds(cfg.StopGrace)
		if mono-lastCisco >= cfg.RetryInterval && (d.state == Connected || d.state == Blocked) {
			lastCisco = mono
			var cisco bool
			d.unlocked(func() { cisco = d.opts.CiscoCheck() })
			tun := d.tun
			if cisco && d.state == Connected && tun != nil {
				d.logf(slog.LevelInfo, "Cisco Secure Client connected, tearing down the tunnel")
				d.pausedByCisco = true
				d.set(Blocked, messages.Blocked)
				d.unlocked(func() {
					d.closeAll()
					tun.Stop(grace)
				})
			} else if !cisco && d.state == Blocked && !d.loopRunning {
				d.set(Idle, messages.NotConnected)
			}
		}
		if jump > 30 {
			d.logf(slog.LevelInfo, "Resume detected (clock jumped by %.0f s)", jump)
			if tun := d.tun; tun != nil {
				// Stop cleanly instead of SIGUSR2: the state machine then goes through
				// disconnecting -> idle and reconnects if there is demand.
				d.logf(slog.LevelInfo, "Resume detected, reconnecting the tunnel")
				d.set(Disconnecting, messages.Disconnecting)
				d.unlocked(func() {
					d.closeAll()
					tun.Stop(grace)
				})
			}
		}
		if d.state == Connected && d.tun != nil && mono-d.lastActivity > cfg.IdleMinutes*60 {
			d.logf(slog.LevelInfo, "Idle for %.0f s, tearing down the tunnel", mono-d.lastActivity)
			d.explicit = false
			d.demandUntil = 0
			tun := d.tun
			d.set(Disconnecting, messages.Disconnecting)
			d.unlocked(func() {
				d.closeAll()
				tun.Stop(grace)
			})
		}
		d.mu.Unlock()
	}
}
