// Package libtunnel is the daemon's Tunnel with openconnect inside the core's own process:
// libopenconnect (internal/octunnel) writes the tunnel to one end of a socketpair, a
// userspace network stack (internal/netstack) reads the other, and the SOCKS server
// (internal/socks) dials through that stack, where the openconnect program would have
// started ocproxy. iOS needs it because it runs no child processes; Linux runs it with the
// build tag libopenconnect for the tests.
package libtunnel

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/DavidVinu/uni-vpn/internal/netstack"
	"github.com/DavidVinu/uni-vpn/internal/octunnel"
	"github.com/DavidVinu/uni-vpn/internal/socks"
	"github.com/DavidVinu/uni-vpn/internal/tunnel"
)

// Engine starts openconnect sessions; octunnel in a real build, a fake in the tests.
type Engine interface {
	Connect(o octunnel.Options) (Session, error)
}

// Session is what the tunnel needs of *octunnel.Session.
type Session interface {
	IPInfo() (netip.Addr, []netip.Addr, int)
	Run(tunFD int) int
	Cancel()
	Reconnect()
	Free()
}

// defaultMTU when the gateway names none (openconnect's own default).
const defaultMTU = 1406

const tailLen = 20

// Tunnel is one openconnect session; like tunnel.Tunnel it is used once: Start, WaitReady, Stop.
type Tunnel struct {
	Cfg      tunnel.Config
	Log      *slog.Logger
	TOTPCode tunnel.CodeFunc

	engine     Engine
	classifier *tunnel.Classifier
	cancel     chan struct{}
	cancelOnce sync.Once
	ready      chan struct{}
	exited     chan struct{}

	mu             sync.Mutex
	started        bool
	session        Session
	port           int
	returnCode     *int
	classification *tunnel.Verdict
	tail           []string
	otpGeneratedAt time.Time
	stoppedByUs    bool
	startedAt      time.Time
	readyAt        time.Time
}

// New returns a tunnel for cfg that runs its sessions with engine.
func New(cfg tunnel.Config, log *slog.Logger, engine Engine) *Tunnel {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Tunnel{Cfg: cfg, Log: log, engine: engine, classifier: tunnel.NewClassifier(cfg.MFA),
		cancel: make(chan struct{}), ready: make(chan struct{}), exited: make(chan struct{})}
}

func (t *Tunnel) logf(level slog.Level, format string, args ...any) {
	t.Log.Log(context.Background(), level, fmt.Sprintf(format, args...))
}

// options are the program's command line and stdin as library options. stdin holds the
// lines tunnel.StdinBytes computes: the password, then Duo's "push".
func (t *Tunnel) options(stdin []byte, totp string) octunnel.Options {
	lines := bytes.Split(bytes.TrimSuffix(stdin, []byte("\n")), []byte("\n"))
	o := octunnel.Options{
		Host: t.Cfg.Host, UserAgent: t.Cfg.UserAgent, User: t.Cfg.LoginName, AuthGroup: t.Cfg.AuthGroup,
		UserGroup: t.Cfg.UserGroup, OS: t.Cfg.OS, NoExternalAuth: t.Cfg.NoExternalAuth,
		Password: lines[0], ReconnectTimeout: 60, Line: t.handleLine, Cancel: t.cancel,
	}
	if len(lines) > 1 {
		o.SecondPassword = string(lines[1])
	}
	if totp != "" && t.Cfg.MFA == "totp_field" {
		o.TOTPSecret = totp
	}
	// libopenconnect reports the system it was built for (apple-ios on a phone); the profiles'
	// user agents name a desktop, as on Android.
	if o.OS == "" {
		o.OS = "linux-64"
	}
	return o
}

// Start logs in and brings the tunnel up in the background. totp is the normalized secret,
// "" for none.
func (t *Tunnel) Start(password []byte, totp string) error {
	helper := tunnel.New(t.Cfg, "", "", t.Log, "", os.TempDir())
	helper.TOTPCode = t.TOTPCode
	stdin, err := helper.StdinBytes(password, totp)
	if err != nil {
		return err
	}
	port, err := tunnel.FreePort()
	if err != nil {
		return err
	}
	t.mu.Lock()
	t.port = port
	t.started = true
	t.startedAt = time.Now()
	if at := helper.OTPGeneratedAt(); !at.IsZero() {
		t.otpGeneratedAt = at
	}
	t.mu.Unlock()
	go func() {
		code := t.run(t.options(stdin, totp), stdin, port)
		t.mu.Lock()
		t.returnCode = &code
		t.mu.Unlock()
		t.logf(slog.LevelInfo, "openconnect exited with code %d", code)
		close(t.exited)
	}()
	return nil
}

// run is the openconnect program with ocproxy: the session, then the stack and the SOCKS
// server on port. It returns the program's exit code.
func (t *Tunnel) run(o octunnel.Options, stdin []byte, port int) int {
	session, err := t.engine.Connect(o)
	clear(stdin)
	if err != nil {
		var exit *octunnel.ExitError
		if errors.As(err, &exit) {
			return exit.Code
		}
		t.logf(slog.LevelError, "openconnect: %s", err)
		return 1
	}
	defer session.Free()
	t.mu.Lock()
	t.session = session
	stopped := t.stoppedByUs
	t.mu.Unlock()
	if stopped {
		session.Cancel() // Run returns at once
	}

	addr, dns, mtu := session.IPInfo()
	if !addr.Is4() {
		t.logf(slog.LevelError, "The gateway assigned no IPv4 address")
		return 1
	}
	if mtu <= 0 {
		mtu = defaultMTU
	}
	mine, theirs, err := socketPair()
	if err != nil {
		t.logf(slog.LevelError, "No socket pair for the tunnel: %s", err)
		return 1
	}
	defer closeFD(theirs)
	stack, err := netstack.New(mine, addr, dns, mtu)
	if err != nil {
		t.logf(slog.LevelError, "Network stack failed: %s", err)
		return 1
	}
	defer stack.Close()
	servers := make([]string, len(dns))
	for i, d := range dns {
		servers[i] = d.String()
	}
	server := socks.NewServer(port, "", servers, t.Log)
	server.Net = stack
	if err := server.Start(context.Background()); err != nil {
		t.logf(slog.LevelError, "SOCKS server on port %d failed: %s", port, err)
		return 1
	}
	defer server.Stop()
	t.logf(slog.LevelInfo, "Tunnel address %s, DNS %s, MTU %d", addr, strings.Join(servers, " "), mtu)
	close(t.ready)
	return session.Run(theirs)
}

// handleLine is tunnel.Tunnel's: the tail, the log and the classifier see each output line.
func (t *Tunnel) handleLine(line string) {
	if len(line) > tunnel.MaxLine {
		line = line[:tunnel.MaxLine]
	}
	text := strings.TrimRightFunc(line, unicode.IsSpace)
	t.mu.Lock()
	t.tail = append(t.tail, text)
	if len(t.tail) > tailLen {
		t.tail = t.tail[len(t.tail)-tailLen:]
	}
	t.mu.Unlock()
	t.logf(slog.LevelInfo, "openconnect: %s", text)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.classification == nil {
		if v, ok := t.classifier.Feed(text); ok {
			t.classification = &v
		}
	}
	if t.classifier.OTPGenerated && t.otpGeneratedAt.IsZero() {
		t.otpGeneratedAt = time.Now()
	}
}

// WaitReady waits until the SOCKS server listens, the session ended or timeout passed.
func (t *Tunnel) WaitReady(timeout time.Duration) bool {
	select {
	case <-t.ready:
	case <-t.exited:
		return false
	case <-time.After(timeout):
		return false
	}
	if t.HasExited() {
		return false
	}
	t.mu.Lock()
	t.readyAt = time.Now()
	t.mu.Unlock()
	return true
}

// Stop cancels the session (the program's SIGTERM) and waits up to grace for it to end.
func (t *Tunnel) Stop(grace time.Duration) {
	t.mu.Lock()
	if t.HasExited() {
		t.mu.Unlock()
		return // ended on its own: not stopped by us
	}
	t.stoppedByUs = true
	started, session := t.started, t.session
	t.mu.Unlock()
	t.cancelOnce.Do(func() { close(t.cancel) })
	if session != nil {
		session.Cancel()
	}
	if !started {
		return
	}
	select {
	case <-t.exited:
	case <-time.After(grace):
		t.logf(slog.LevelWarn, "openconnect does not respond to cancel")
	}
}

// Reconnect drops and remakes the connection, like SIGUSR2 to the program.
func (t *Tunnel) Reconnect() {
	t.mu.Lock()
	session := t.session
	t.mu.Unlock()
	if session != nil && !t.HasExited() {
		session.Reconnect()
	}
}

// Exited is closed once the session ended and its resources are released.
func (t *Tunnel) Exited() <-chan struct{} { return t.exited }

func (t *Tunnel) HasExited() bool {
	select {
	case <-t.exited:
		return true
	default:
		return false
	}
}

func (t *Tunnel) StoppedByUs() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stoppedByUs
}

func (t *Tunnel) Classification() (tunnel.Verdict, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.classification == nil {
		return tunnel.Verdict{}, false
	}
	return *t.classification, true
}

// ReturnCode is the program's exit code; ok is false while the session runs.
func (t *Tunnel) ReturnCode() (int, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.returnCode == nil {
		return 0, false
	}
	return *t.returnCode, true
}

func (t *Tunnel) OTPGeneratedAt() time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.otpGeneratedAt
}

// Port is the SOCKS port, 0 before Start.
func (t *Tunnel) Port() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.port
}

func (t *Tunnel) StartedAt() time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.startedAt
}

func (t *Tunnel) ReadyAt() time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.readyAt
}

// StderrTail is the last 20 output lines.
func (t *Tunnel) StderrTail() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.tail...)
}
