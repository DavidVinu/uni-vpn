//go:build !windows

// The daemon tests drive the POSIX tunnel with the fake openconnect, like tests/test_daemon.py (posix_only).

package daemon

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DavidVinu/uni-vpn/internal/config"
	"github.com/DavidVinu/uni-vpn/internal/messages"
	"github.com/DavidVinu/uni-vpn/internal/tunnel"
	"github.com/DavidVinu/uni-vpn/internal/tunnel/fakeoc"
)

// The tests are a port of tests/test_daemon.py and tests/test_httpapi.py: the same harness
// (fake openconnect, injected keyring, probe and Cisco check) and the same cases.

func TestMain(m *testing.M) {
	fakeoc.MaybeRun()
	os.Exit(m.Run())
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type harness struct {
	t           *testing.T
	cfg         *config.Config
	pwfile      string
	tokenfile   string
	tokenDir    string
	domainsPath string
	logs        *syncBuffer
	wallOffset  atomic.Int64 // seconds added to the wall clock (Python mocks time.time)
	probeResult atomic.Bool
	cisco       atomic.Bool

	mu          sync.Mutex
	password    []byte
	passwordErr error
	totp        []byte
	totpErr     error
	stored      []string
	storedTOTP  []string
	refreshed   []int
	totpSetter  func(string) error

	d    *Daemon
	done chan struct{}
}

func freePort(t *testing.T) int {
	p, err := tunnel.FreePort()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func newHarness(t *testing.T) *harness {
	tmp := t.TempDir()
	h := &harness{t: t, pwfile: filepath.Join(tmp, "pw"), tokenfile: filepath.Join(tmp, "token"),
		tokenDir: filepath.Join(tmp, "state"), domainsPath: filepath.Join(tmp, "domains.txt"), logs: &syncBuffer{},
		password: []byte("pw-s3cret"), totp: []byte("base32:GEZDGNBVGY3TQOJQ")}
	if err := os.Mkdir(h.tokenDir, 0o700); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(fakeoc.EnvVar, "openconnect")
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	t.Setenv("FAKE_PASSWORD_FILE", h.pwfile)
	t.Setenv("FAKE_TOKEN_FILE", h.tokenfile)
	t.Setenv("FAKE_MODE", "ok")
	t.Setenv("FAKE_DELAY", "0.2")
	t.Setenv("FAKE_ARGS_FILE", "")
	h.probeResult.Store(true)
	// ocproxy: any executable path will do, the fake never starts the wrapper.
	cfg := config.Default()
	cfg.User, cfg.Host, cfg.OpenConnect, cfg.OCProxy = "u", "vpn.example", exe, exe
	cfg.SocksPort, cfg.HTTPPort = freePort(t), freePort(t)
	for cfg.HTTPPort == cfg.SocksPort { // the system may hand out a just freed port again
		cfg.HTTPPort = freePort(t)
	}
	cfg.IdleMinutes, cfg.ReadyTimeout, cfg.ClientWait, cfg.StopGrace = 0.01, 4, 2, 1
	cfg.HalfcloseGrace, cfg.DemandWindow, cfg.RetryInterval, cfg.Tick = 0.5, 0.3, 0.2, 0.1
	cfg.Backoff = []float64{0.1, 0.1}
	h.cfg = &cfg
	t.Cleanup(h.teardown)
	return h
}

func (h *harness) teardown() {
	if h.d == nil {
		return
	}
	h.d.Stop()
	select {
	case <-h.done:
	case <-time.After(10 * time.Second):
		h.t.Error("daemon did not stop")
	}
}

// start is start_daemon; edit adjusts options or the daemon before it runs.
func (h *harness) start(edit func(*Options), before ...func(*Daemon)) *Daemon {
	opts := Options{
		PasswordGetter: func(context.Context) ([]byte, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.password, h.passwordErr
		},
		PasswordSetter: func(pw string) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.stored = append(h.stored, pw)
			return nil
		},
		TOTPGetter: func(context.Context) ([]byte, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.totp, h.totpErr
		},
		TOTPSetter: func(token string) error {
			h.mu.Lock()
			setter := h.totpSetter
			h.mu.Unlock()
			if setter != nil {
				return setter(token)
			}
			h.mu.Lock()
			defer h.mu.Unlock()
			h.storedTOTP = append(h.storedTOTP, token)
			return nil
		},
		Probe:      func(context.Context) bool { return h.probeResult.Load() },
		CiscoCheck: func() bool { return h.cisco.Load() },
		TokenDir:   h.tokenDir, DomainsPath: h.domainsPath,
		ProxyRefresh: func(port int) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.refreshed = append(h.refreshed, port)
		},
		RepairStart: func() (Process, error) { panic("no repair in tests") },
		Elevated:    func() *bool { return nil },
	}
	if edit != nil {
		edit(&opts)
	}
	log := slog.New(slog.NewTextHandler(io.MultiWriter(h.logs), &slog.HandlerOptions{Level: slog.LevelInfo}))
	d := New(h.cfg, log, opts)
	d.wall = func() float64 { return float64(time.Now().UnixNano())/1e9 + float64(h.wallOffset.Load()) }
	// The tests reconnect every second; really waiting for the next one-time code window
	// (up to 30 s) would slow them down. Only the OTP tests use the original.
	d.otpWait = func(float64) float64 { return 0 }
	for _, f := range before {
		f(d)
	}
	h.d = d
	h.done = make(chan struct{})
	go func() { d.Run(); close(h.done) }()
	select {
	case <-d.Started():
	case <-time.After(3 * time.Second):
		h.t.Fatal("daemon did not start")
	}
	return d
}

func (h *harness) pwLines() []string {
	data, err := os.ReadFile(h.pwfile)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func (h *harness) client() net.Conn {
	h.t.Helper()
	c, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(h.cfg.SocksPort)))
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { c.Close() })
	return c
}

func (d *Daemon) snapshot() (State, messages.Message) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.state, d.message
}

func (d *Daemon) tunnelNow() Tunnel {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.tun
}

func waitState(t *testing.T, d *Daemon, want State, timeout ...time.Duration) {
	t.Helper()
	limit := 6 * time.Second
	if len(timeout) > 0 {
		limit = timeout[0]
	}
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if s, _ := d.snapshot(); s == want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	s, m := d.snapshot()
	t.Fatalf("state is %s (%s), expected %s", s, m.Text, want)
}

// echo writes data and reads it back through the tunnel.
func echo(t *testing.T, c net.Conn, data string) {
	t.Helper()
	if _, err := c.Write([]byte(data)); err != nil {
		t.Fatal(err)
	}
	c.SetReadDeadline(time.Now().Add(4 * time.Second))
	buf := make([]byte, len(data))
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != data {
		t.Fatalf("%q %v", buf, err)
	}
}

// readEOF expects the connection to be closed without data within timeout.
func readEOF(t *testing.T, c net.Conn, timeout time.Duration) {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(timeout))
	buf := make([]byte, 10)
	n, err := c.Read(buf)
	if n != 0 || err == nil {
		t.Fatalf("read %q %v, expected EOF", buf[:n], err)
	}
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("connection not closed in time")
	}
}

func waitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return cond()
}
