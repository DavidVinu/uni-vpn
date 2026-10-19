//go:build unix

package libtunnel

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/DavidVinu/uni-vpn/internal/octunnel"
	"github.com/DavidVinu/uni-vpn/internal/tunnel"
)

type fakeEngine struct {
	mu      sync.Mutex
	opts    []octunnel.Options
	connect func(o octunnel.Options) (Session, error)
}

func (f *fakeEngine) Connect(o octunnel.Options) (Session, error) {
	f.mu.Lock()
	seen := o
	seen.Password = bytes.Clone(o.Password) // wiped after Connect
	f.opts = append(f.opts, seen)
	f.mu.Unlock()
	return f.connect(o)
}

// fakeSession runs until cancelled, or returns code by itself after exitAfter.
type fakeSession struct {
	cancel    chan struct{}
	once      sync.Once
	exitAfter time.Duration
	code      int
	freed     chan struct{}
}

func newSession() *fakeSession {
	return &fakeSession{cancel: make(chan struct{}), freed: make(chan struct{})}
}

func (s *fakeSession) IPInfo() (netip.Addr, []netip.Addr, int) {
	return netip.MustParseAddr("10.0.0.2"), []netip.Addr{netip.MustParseAddr("10.0.0.1")}, 1400
}

func (s *fakeSession) Run(tunFD int) int {
	if s.exitAfter > 0 {
		select {
		case <-time.After(s.exitAfter):
			return s.code
		case <-s.cancel:
		}
	}
	<-s.cancel
	return 0
}

func (s *fakeSession) Cancel()    { s.once.Do(func() { close(s.cancel) }) }
func (s *fakeSession) Reconnect() {}
func (s *fakeSession) Free()      { close(s.freed) }

func newTunnel(cfg tunnel.Config, e Engine) *Tunnel {
	t := New(cfg, slog.New(slog.DiscardHandler), e)
	t.TOTPCode = func(string, time.Time) (string, error) { return "123456", nil }
	return t
}

func exited(t *testing.T, tun *Tunnel) {
	t.Helper()
	select {
	case <-tun.Exited():
	case <-time.After(5 * time.Second):
		t.Fatal("did not exit")
	}
}

func TestRunsUntilStopped(t *testing.T) {
	s := newSession()
	tun := newTunnel(tunnel.Config{Host: "vpn.example", MFA: "totp_field"},
		&fakeEngine{connect: func(octunnel.Options) (Session, error) { return s, nil }})
	if err := tun.Start([]byte("pw"), "base32:JBSWY3DP"); err != nil {
		t.Fatal(err)
	}
	if !tun.WaitReady(5 * time.Second) {
		t.Fatal("not ready")
	}
	if tun.ReadyAt().IsZero() || tun.StartedAt().IsZero() {
		t.Fatal("times")
	}
	c, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(tun.Port()))
	if err != nil {
		t.Fatal(err)
	}
	c.SetDeadline(time.Now().Add(5 * time.Second))
	c.Write([]byte{5, 1, 0})
	got := make([]byte, 2)
	if _, err := io.ReadFull(c, got); err != nil || !bytes.Equal(got, []byte{5, 0}) {
		t.Fatal(got, err)
	}
	c.Close()
	tun.Stop(2 * time.Second)
	exited(t, tun)
	<-s.freed
	if !tun.StoppedByUs() || !tun.HasExited() {
		t.Fatal("not stopped by us")
	}
	if code, ok := tun.ReturnCode(); !ok || code != 0 {
		t.Fatal(code, ok)
	}
	if tunnel.PortOpen(tun.Port()) {
		t.Fatal("port still open")
	}
}

func TestLoginFailureIsClassifiedLikeTheProgram(t *testing.T) {
	lines := []string{"POST https://vpn.example/", "Login failed.", "Password:***",
		"User input required in non-interactive mode", "Failed to complete authentication"}
	tun := newTunnel(tunnel.Config{Host: "vpn.example", MFA: "totp_field"},
		&fakeEngine{connect: func(o octunnel.Options) (Session, error) {
			for _, l := range lines {
				o.Line(l)
			}
			return nil, &octunnel.ExitError{Code: 1, Reason: "authentication"}
		}})
	if err := tun.Start([]byte("pw"), ""); err != nil {
		t.Fatal(err)
	}
	if tun.WaitReady(5 * time.Second) {
		t.Fatal("ready")
	}
	exited(t, tun)
	c := tunnel.NewClassifier("totp_field")
	var want tunnel.Verdict
	for _, l := range lines {
		if v, ok := c.Feed(l); ok {
			want = v
			break
		}
	}
	if got, ok := tun.Classification(); !ok || got != want {
		t.Fatal(got, want)
	}
	if code, ok := tun.ReturnCode(); !ok || code != 1 {
		t.Fatal(code)
	}
	if tun.StoppedByUs() {
		t.Fatal("stopped by us")
	}
}

func TestTunnelEndsByItself(t *testing.T) {
	s := newSession()
	s.exitAfter, s.code = 200*time.Millisecond, 1
	tun := newTunnel(tunnel.Config{Host: "vpn.example"},
		&fakeEngine{connect: func(octunnel.Options) (Session, error) { return s, nil }})
	if err := tun.Start([]byte("pw"), ""); err != nil {
		t.Fatal(err)
	}
	exited(t, tun)
	if code, ok := tun.ReturnCode(); !ok || code != 1 {
		t.Fatal(code)
	}
	if tun.StoppedByUs() {
		t.Fatal("stopped by us")
	}
	if tunnel.PortOpen(tun.Port()) {
		t.Fatal("port still open")
	}
}

type ipv6Session struct{ *fakeSession }

func (ipv6Session) IPInfo() (netip.Addr, []netip.Addr, int) {
	return netip.MustParseAddr("fd00::2"), nil, 1400
}

// The daemon reads an exit code 1 without a classified line as a refused password: a failure
// on this side of the tunnel must not end with it.
func TestLocalFailureIsNoRefusedLogin(t *testing.T) {
	cases := map[string]func(tun *Tunnel) (Session, error){
		"no IPv4 address": func(*Tunnel) (Session, error) { return ipv6Session{newSession()}, nil },
		"SOCKS port taken": func(tun *Tunnel) (Session, error) {
			ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(tun.Port()))
			if err != nil {
				t.Error(err)
				return nil, err
			}
			t.Cleanup(func() { ln.Close() })
			return newSession(), nil
		},
		"library error": func(*Tunnel) (Session, error) { return nil, errors.New("no memory") },
	}
	for name, connect := range cases {
		var tun *Tunnel
		tun = newTunnel(tunnel.Config{Host: "vpn.example"},
			&fakeEngine{connect: func(octunnel.Options) (Session, error) { return connect(tun) }})
		if err := tun.Start([]byte("pw"), ""); err != nil {
			t.Fatal(err)
		}
		exited(t, tun)
		if _, ok := tun.Classification(); ok {
			t.Fatal(name, "classified")
		}
		if code, ok := tun.ReturnCode(); !ok || code != exitLocal {
			t.Fatal(name, code)
		}
	}
}

func TestStopDuringLogin(t *testing.T) {
	tun := newTunnel(tunnel.Config{Host: "vpn.example"},
		&fakeEngine{connect: func(o octunnel.Options) (Session, error) {
			<-o.Cancel
			o.Line("Failed to complete authentication")
			return nil, &octunnel.ExitError{Code: 1}
		}})
	if err := tun.Start([]byte("pw"), ""); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	tun.Stop(2 * time.Second)
	exited(t, tun)
	if !tun.StoppedByUs() {
		t.Fatal("not stopped by us")
	}
}

// The library's external-browser mode prints its failure and then waits for a browser until
// cancelled: the web sign-in verdict ends the login at once, as the program's run does.
func TestWebSignInEndsTheLogin(t *testing.T) {
	tun := newTunnel(tunnel.Config{Host: "vpn.example", MFA: "duo_push"},
		&fakeEngine{connect: func(o octunnel.Options) (Session, error) {
			o.Line("Failed to spawn external browser for https://vpn.example/+CSCOE+/saml/sp/login")
			<-o.Cancel
			o.Line("Socket accept cancelled")
			return nil, &octunnel.ExitError{Code: 1}
		}})
	if err := tun.Start([]byte("pw"), ""); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if tun.WaitReady(5 * time.Second) {
		t.Fatal("ready")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Error("took", d)
	}
	exited(t, tun)
	if v, _ := tun.Classification(); v != (tunnel.Verdict{State: tunnel.StateAuthFailed, Message: tunnel.SAMLRequired}) || tun.StoppedByUs() {
		t.Fatal(v, tun.StoppedByUs())
	}
}

// Once the tunnel is up, a line that reads like a web sign-in does not end it.
func TestWebSignInLineAfterReadyKeepsTheTunnel(t *testing.T) {
	s := newSession()
	var line func(string)
	tun := newTunnel(tunnel.Config{Host: "vpn.example"},
		&fakeEngine{connect: func(o octunnel.Options) (Session, error) { line = o.Line; return s, nil }})
	if err := tun.Start([]byte("pw"), ""); err != nil {
		t.Fatal(err)
	}
	if !tun.WaitReady(5 * time.Second) {
		t.Fatal("not ready")
	}
	line("Failed to spawn external browser")
	time.Sleep(100 * time.Millisecond)
	if tun.HasExited() {
		t.Fatal("ended")
	}
	tun.Stop(2 * time.Second)
	exited(t, tun)
}

// What the program reads from stdin and its command line, by second factor.
func TestLoginOptions(t *testing.T) {
	cases := []struct {
		mfa, totp  string
		password   string
		second     string
		secret     string
		otpCounted bool
	}{
		{"totp_field", "base32:JBSWY3DP", "pw", "", "base32:JBSWY3DP", false},
		{"totp_field", tunnel.OncePrefix + "123456", "pw", "123456", "", false},
		{"totp_append", "base32:JBSWY3DP", "pw,123456", "", "", true},
		{"duo_push", "", "pw", "push", "", false},
		{"none", "", "pw", "", "", false},
	}
	for _, c := range cases {
		e := &fakeEngine{connect: func(octunnel.Options) (Session, error) { return nil, &octunnel.ExitError{Code: 1} }}
		tun := newTunnel(tunnel.Config{Host: "vpn.example", UserAgent: "UA", LoginName: "ab123@x", AuthGroup: "G",
			UserGroup: "ug", MFA: c.mfa, TOTPSeparator: ","}, e)
		if err := tun.Start([]byte("pw"), c.totp); err != nil {
			t.Fatal(err)
		}
		exited(t, tun)
		o := e.opts[0]
		if string(o.Password) != c.password || o.SecondPassword != c.second || o.TOTPSecret != c.secret ||
			o.Host != "vpn.example" || o.UserAgent != "UA" || o.User != "ab123@x" || o.AuthGroup != "G" ||
			o.UserGroup != "ug" || o.OS != "linux-64" || o.ReconnectTimeout != 60 {
			t.Errorf("%s: %+v", c.mfa, o)
		}
		if got := !tun.OTPGeneratedAt().IsZero(); got != c.otpCounted {
			t.Errorf("%s: OTP generated %v", c.mfa, got)
		}
	}
}

func TestPasswordIsWipedAfterLogin(t *testing.T) {
	var password []byte
	e := &fakeEngine{connect: func(o octunnel.Options) (Session, error) {
		password = o.Password
		return nil, &octunnel.ExitError{Code: 1}
	}}
	tun := newTunnel(tunnel.Config{Host: "vpn.example"}, e)
	if err := tun.Start([]byte("secret"), ""); err != nil {
		t.Fatal(err)
	}
	exited(t, tun)
	if !bytes.Equal(password, make([]byte, 6)) {
		t.Fatalf("%q", password)
	}
}

func TestTOTPAppendWithoutSecret(t *testing.T) {
	tun := newTunnel(tunnel.Config{Host: "vpn.example", MFA: "totp_append"}, &fakeEngine{})
	var te *tunnel.TOTPSecretError
	if err := tun.Start([]byte("pw"), ""); err == nil || !errors.As(err, &te) {
		t.Fatal(err)
	}
}
