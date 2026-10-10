//go:build !windows

// The daemon tests drive the POSIX tunnel with the fake openconnect, like tests/test_daemon.py (posix_only).

package daemon

import (
	"context"
	"encoding/json"
	"math"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DavidVinu/uni-vpn/internal/config"
	"github.com/DavidVinu/uni-vpn/internal/credentials"
	"github.com/DavidVinu/uni-vpn/internal/messages"
	"github.com/DavidVinu/uni-vpn/internal/pyjson"
	"github.com/DavidVinu/uni-vpn/internal/tunnel"
)

func statusValue(t *testing.T, d *Daemon, key string) any {
	t.Helper()
	s, err := d.Status()
	if err != nil {
		t.Fatal(err)
	}
	v, ok := s.Get(key)
	if !ok {
		t.Fatalf("no %q in status", key)
	}
	return v
}

// --- Connect -------------------------------------------------------------------

func TestFirstConnectionStartsTunnelAndEchoes(t *testing.T) {
	h := newHarness(t)
	d := h.start(nil)
	c := h.client()
	echo(t, c, "hello")
	if s, _ := d.snapshot(); s != Connected {
		t.Fatal(s)
	}
	if !slices.Equal(h.pwLines(), []string{"pw-s3cret"}) {
		t.Fatal(h.pwLines())
	}
	if statusValue(t, d, "active_connections") != 1 {
		t.Fatal(statusValue(t, d, "active_connections"))
	}
}

func TestSecondConnectionDoesNotStartSecondProcess(t *testing.T) {
	h := newHarness(t)
	h.start(nil)
	c1, c2 := h.client(), h.client()
	echo(t, c1, "x")
	echo(t, c2, "x")
	if len(h.pwLines()) != 1 {
		t.Fatal(h.pwLines())
	}
}

func TestClientWaitExceededThenConnected(t *testing.T) {
	h := newHarness(t)
	t.Setenv("FAKE_DELAY", "1.2")
	h.cfg.ClientWait = 0.5
	d := h.start(nil)
	c := h.client()
	started := time.Now()
	readEOF(t, c, 3*time.Second)
	if time.Since(started) > 1100*time.Millisecond {
		t.Fatal(time.Since(started))
	}
	waitState(t, d, Connected)
}

func TestDisconnectRequestThenReconnect(t *testing.T) {
	h := newHarness(t)
	d := h.start(nil)
	c := h.client()
	echo(t, c, "x")
	d.RequestDisconnect()
	waitState(t, d, Idle)
	readEOF(t, c, 3*time.Second)
	if d.tunnelNow() != nil {
		t.Fatal("tunnel left")
	}
	c2 := h.client()
	echo(t, c2, "y")
	if len(h.pwLines()) != 2 {
		t.Fatal(h.pwLines())
	}
}

func TestDisconnectWhileConnectingDoesNotRestart(t *testing.T) {
	h := newHarness(t)
	t.Setenv("FAKE_DELAY", "3")
	d := h.start(nil)
	c := h.client()
	waitState(t, d, Connecting)
	// Disconnect only once the process has read the password: `connecting` is set before the start.
	waitFor(3*time.Second, func() bool { return len(h.pwLines()) >= 1 })
	d.RequestDisconnect()
	waitState(t, d, Idle)
	readEOF(t, c, 2*time.Second)
	time.Sleep(500 * time.Millisecond)
	if s, _ := d.snapshot(); s != Idle {
		t.Fatal(s)
	}
	if d.forwarder.Active() != 0 || len(h.pwLines()) != 1 {
		t.Fatal(d.forwarder.Active(), h.pwLines())
	}
}

func TestExplicitConnectWithoutClient(t *testing.T) {
	h := newHarness(t)
	d := h.start(nil)
	d.RequestConnect()
	waitState(t, d, Connected)
	if len(h.pwLines()) != 1 {
		t.Fatal(h.pwLines())
	}
	// The "connect now" request is fulfilled, after that only real use counts.
	d.mu.Lock()
	explicit := d.explicit
	d.mu.Unlock()
	if explicit {
		t.Fatal("still explicit")
	}
}

func TestConnectRequestDuringDisconnectingReconnects(t *testing.T) {
	h := newHarness(t)
	d := h.start(nil)
	d.RequestConnect()
	waitState(t, d, Connected)
	done := make(chan struct{})
	go func() { d.RequestDisconnect(); close(done) }()
	// RequestDisconnect has set the state and is waiting for the process.
	waitFor(time.Second, func() bool { d.mu.Lock(); defer d.mu.Unlock(); return d.disconnects == 1 })
	d.RequestConnect()
	<-done
	waitState(t, d, Connected)
	if len(h.pwLines()) != 2 {
		t.Fatal(h.pwLines())
	}
}

// --- Idle --------------------------------------------------------------------

func TestIdleDisconnectsAndClosesClients(t *testing.T) {
	h := newHarness(t)
	d := h.start(nil)
	c := h.client()
	echo(t, c, "x")
	waitState(t, d, Idle, 4*time.Second)
	if d.tunnelNow() != nil {
		t.Fatal("tunnel left")
	}
	readEOF(t, c, 3*time.Second)
}

func TestIgnoreSigtermIsKilled(t *testing.T) {
	h := newHarness(t)
	t.Setenv("FAKE_MODE", "ignore_sigterm")
	h.cfg.StopGrace = 0.3
	d := h.start(nil)
	c := h.client()
	echo(t, c, "x")
	c.Close()
	waitState(t, d, Idle, 5*time.Second)
	if d.tunnelNow() != nil {
		t.Fatal("tunnel left")
	}
}

// --- Failures --------------------------------------------------------------------

func TestAuthFailNoRetryUntilExplicitConnect(t *testing.T) {
	h := newHarness(t)
	t.Setenv("FAKE_MODE", "auth_fail")
	d := h.start(nil)
	c := h.client()
	readEOF(t, c, 4*time.Second)
	waitState(t, d, AuthFailed)
	if _, m := d.snapshot(); m.Action != "password" {
		t.Fatal(m)
	}
	time.Sleep(600 * time.Millisecond)
	if len(h.pwLines()) != 1 {
		t.Fatal(h.pwLines())
	}
	c2 := h.client()
	readEOF(t, c2, 2*time.Second)
	if len(h.pwLines()) != 1 {
		t.Fatal(h.pwLines())
	}
	d.RequestConnect()
	time.Sleep(800 * time.Millisecond)
	if len(h.pwLines()) != 2 {
		t.Fatal(h.pwLines())
	}
}

func TestInputRequiredMessage(t *testing.T) {
	h := newHarness(t)
	t.Setenv("FAKE_MODE", "input_required")
	d := h.start(nil)
	d.RequestConnect()
	waitState(t, d, AuthFailed)
	if _, m := d.snapshot(); m != messages.AuthRejected {
		t.Fatal(m)
	}
}

func (d *Daemon) lastErrorMessage() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.lastError == nil {
		return ""
	}
	return d.lastError.message
}

func TestTunnelDiesWithDemandReconnects(t *testing.T) {
	h := newHarness(t)
	t.Setenv("FAKE_MODE", "exit_after_ready")
	t.Setenv("FAKE_EXIT_AFTER", "0.4")
	h.cfg.DemandWindow = 1.5 // demand comes from usage, not from RequestConnect
	d := h.start(nil)
	c := h.client()
	echo(t, c, "x")
	if s, _ := d.snapshot(); s != Connected {
		t.Fatal(s)
	}
	if !waitFor(4*time.Second, func() bool { return len(h.pwLines()) >= 2 }) {
		t.Fatal(h.pwLines())
	}
	if d.lastErrorMessage() != messages.ConnectionLost.Text {
		t.Fatal(d.lastErrorMessage())
	}
}

func TestTunnelDiesWithoutDemandGoesIdle(t *testing.T) {
	h := newHarness(t)
	t.Setenv("FAKE_MODE", "exit_after_ready")
	t.Setenv("FAKE_EXIT_AFTER", "0.8")
	h.cfg.IdleMinutes = 1 // the idle timer must not fire first here
	d := h.start(nil)
	c := h.client()
	echo(t, c, "x")
	c.Close()
	waitState(t, d, Idle, 4*time.Second)
	time.Sleep(500 * time.Millisecond)
	if len(h.pwLines()) != 1 || d.lastErrorMessage() != messages.ConnectionLost.Text {
		t.Fatal(h.pwLines(), d.lastErrorMessage())
	}
}

func TestPasswordMissing(t *testing.T) {
	h := newHarness(t)
	h.passwordErr = credentials.ErrPasswordMissing
	d := h.start(nil)
	d.RequestConnect()
	waitState(t, d, Keyring)
	if _, m := d.snapshot(); m != messages.PasswordMissing || h.pwLines() != nil {
		t.Fatal(m, h.pwLines())
	}
}

func TestTOTPSecretReachesOpenconnect(t *testing.T) {
	h := newHarness(t)
	d := h.start(nil)
	d.RequestConnect()
	waitState(t, d, Connected)
	data, _ := os.ReadFile(h.tokenfile)
	if string(data) != "base32:GEZDGNBVGY3TQOJQ\n" {
		t.Fatalf("%q", data)
	}
	if entries, _ := os.ReadDir(h.tokenDir); len(entries) != 0 {
		t.Fatal(entries)
	}
}

func TestTOTPMissing(t *testing.T) {
	h := newHarness(t)
	h.totpErr = credentials.ErrTotpMissing
	d := h.start(nil)
	d.RequestConnect()
	waitState(t, d, Keyring)
	if _, m := d.snapshot(); m != messages.TOTPMissing {
		t.Fatal(m)
	}
	if h.pwLines() != nil {
		t.Fatal("openconnect must not start without a secret")
	}
}

func TestSetTOTPStoresAndConnects(t *testing.T) {
	h := newHarness(t)
	h.totpErr = credentials.ErrTotpMissing
	d := h.start(nil)
	d.RequestConnect()
	waitState(t, d, Keyring)
	h.mu.Lock()
	h.totpErr, h.totp = nil, []byte("base32:GEZDGNBVGY3TQOJQ")
	h.mu.Unlock()
	if err := d.SetTOTP("base32:GEZDGNBVGY3TQOJQ"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(h.storedTOTP, []string{"base32:GEZDGNBVGY3TQOJQ"}) {
		t.Fatal(h.storedTOTP)
	}
	waitState(t, d, Connected)
}

func TestRejectedTOTPIsFinalAuthFailure(t *testing.T) {
	h := newHarness(t)
	t.Setenv("FAKE_MODE", "totp_rejected")
	d := h.start(nil)
	d.RequestConnect()
	waitState(t, d, AuthFailed)
	if _, m := d.snapshot(); m != messages.TOTPRejected {
		t.Fatal(m)
	}
	time.Sleep(500 * time.Millisecond)
	if len(h.pwLines()) != 1 {
		t.Fatal("no automatic second attempt", h.pwLines())
	}
}

func TestSuccessfulLoginRecordsOTPWindow(t *testing.T) {
	h := newHarness(t)
	d := h.start(nil)
	before := time.Now().Unix() / 30
	d.RequestConnect()
	waitState(t, d, Connected)
	d.mu.Lock()
	step, has := d.lastOTPStep, d.hasOTPStep
	d.mu.Unlock()
	if !has || (step != before && step != before+1) {
		t.Fatal(step, before)
	}
}

func TestOTPWaitOnlyWithinSameWindow(t *testing.T) {
	h := newHarness(t)
	d := h.start(nil)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.realOTPWait(1000) != 0 {
		t.Fatal()
	}
	d.lastOTPStep, d.hasOTPStep = 1000/30, true // window 990..1020
	for now, want := range map[float64]float64{1000: 20.5, 1019: 1.5, 1020: 0, 1100: 0} {
		if got := d.realOTPWait(now); math.Abs(got-want) > 0.05 {
			t.Fatal(now, got, want)
		}
	}
}

func TestReconnectInSameWindowWaitsForNextCode(t *testing.T) {
	// Measured 2026-09-08: the same one-time code is valid only once. Reconnecting 3 s after
	// the login failed with "Login failed". So the daemon waits for the window to pass.
	h := newHarness(t)
	d := h.start(nil)
	d.RequestConnect()
	waitState(t, d, Connected)
	d.RequestDisconnect()
	waitState(t, d, Idle)
	d.mu.Lock()
	waits := []float64{0.8}
	d.otpWait = func(float64) float64 {
		if len(waits) == 0 {
			return 0
		}
		w := waits[0]
		waits = waits[1:]
		return w
	}
	d.mu.Unlock()
	started := time.Now()
	d.RequestConnect()
	waitState(t, d, Connecting)
	if _, m := d.snapshot(); !strings.Contains(m.Text, "one-time code") {
		t.Fatal(m)
	}
	waitState(t, d, Connected)
	if time.Since(started) < 800*time.Millisecond || len(h.pwLines()) != 2 {
		t.Fatal(time.Since(started), h.pwLines())
	}
}

func TestNewPasswordWhileConnectingStartsOverWithIt(t *testing.T) {
	// Measured with the assistant: Back, corrected password, Next. The attempt still running
	// used the old password and its result was shown for the new one.
	h := newHarness(t)
	t.Setenv("FAKE_DELAY", "3")
	d := h.start(nil)
	d.RequestConnect()
	waitState(t, d, Connecting)
	waitFor(2*time.Second, func() bool { return len(h.pwLines()) > 0 })
	t.Setenv("FAKE_DELAY", "0.2")
	h.mu.Lock()
	h.password = []byte("second")
	h.mu.Unlock()
	if err := d.SetPassword("second"); err != nil {
		t.Fatal(err)
	}
	waitState(t, d, Connected)
	if lines := h.pwLines(); lines[len(lines)-1] != "second" {
		t.Fatal(lines)
	}
}

func TestNewPasswordDuringTheKeyringReadIsUsed(t *testing.T) {
	// The attempt already read the old password when the new one arrived; its result must not
	// be shown as the result for the new one.
	h := newHarness(t)
	t.Setenv("FAKE_MODE", "auth_fail")
	release := make(chan struct{})
	d := h.start(func(o *Options) {
		o.PasswordGetter = func(ctx context.Context) ([]byte, error) {
			h.mu.Lock()
			value := h.password
			h.mu.Unlock()
			select {
			case <-release:
			case <-ctx.Done():
			}
			return value, nil
		}
	})
	d.RequestConnect()
	time.Sleep(200 * time.Millisecond)
	h.mu.Lock()
	h.password = []byte("second")
	h.mu.Unlock()
	if err := d.SetPassword("second"); err != nil {
		t.Fatal(err)
	}
	close(release)
	// Both attempts are rejected by the fake; only the second one may decide the state.
	waitState(t, d, AuthFailed)
	if !slices.Equal(h.pwLines(), []string{"pw-s3cret", "second"}) {
		t.Fatal(h.pwLines())
	}
}

func pidOf(tun Tunnel) int {
	if t, ok := tun.(*tunnel.Tunnel); ok {
		return t.Pid()
	}
	return 0
}

func TestDisconnectAfterSecretsChangedWhileConnectingEndsIdle(t *testing.T) {
	// The attempt belongs to old secrets and was stopped by the user: not stuck in disconnecting.
	h := newHarness(t)
	t.Setenv("FAKE_DELAY", "3")
	d := h.start(nil)
	d.RequestConnect()
	waitState(t, d, Connecting)
	waitFor(2*time.Second, func() bool { tun := d.tunnelNow(); return tun != nil && pidOf(tun) > 0 })
	d.mu.Lock()
	d.secretsGen++
	d.mu.Unlock()
	d.RequestDisconnect()
	waitState(t, d, Idle)
}

func TestDisconnectSurvivesTheTunnelGoingAwayMeanwhile(t *testing.T) {
	h := newHarness(t)
	d := h.start(nil, func(d *Daemon) {
		closeAll := d.forwarder.CloseAll
		d.closeAll = func() {
			closeAll()
			d.mu.Lock()
			d.tun = nil
			d.mu.Unlock()
		}
	})
	d.RequestConnect()
	waitState(t, d, Connected)
	d.RequestDisconnect()
	waitState(t, d, Idle)
}

func TestDisconnectDuringASecretsRestartWins(t *testing.T) {
	h := newHarness(t)
	t.Setenv("FAKE_DELAY", "3")
	t.Setenv("FAKE_MODE", "ignore_sigterm") // the stop takes stop_grace
	d := h.start(nil)
	d.RequestConnect()
	waitState(t, d, Connecting)
	waitFor(2*time.Second, func() bool { tun := d.tunnelNow(); return tun != nil && pidOf(tun) > 0 })
	saving := make(chan struct{})
	go func() { d.SetPassword("second"); close(saving) }()
	time.Sleep(50 * time.Millisecond)
	d.RequestDisconnect()
	<-saving
	waitState(t, d, Idle)
	time.Sleep(500 * time.Millisecond)
	d.mu.Lock()
	state, count := d.state, d.connectCount
	d.mu.Unlock()
	if state != Idle || count != 1 {
		t.Fatal("no new attempt after the Disconnect", state, count)
	}
}

func TestStaleTokenFilesAreRemovedAtStart(t *testing.T) {
	h := newHarness(t)
	os.WriteFile(filepath.Join(h.tokenDir, "totp-old"), []byte("base32:OLD"), 0o600)
	h.start(nil)
	if entries, _ := os.ReadDir(h.tokenDir); len(entries) != 0 {
		t.Fatal(entries)
	}
}

func TestKeyringLocked(t *testing.T) {
	h := newHarness(t)
	h.passwordErr = &credentials.LockedError{Msg: "x"}
	d := h.start(nil)
	d.RequestConnect()
	waitState(t, d, Keyring)
	if _, m := d.snapshot(); m != messages.KeyringLocked {
		t.Fatal(m)
	}
}

func TestSetPasswordStoresAndConnects(t *testing.T) {
	h := newHarness(t)
	h.passwordErr = credentials.ErrPasswordMissing
	d := h.start(nil)
	d.RequestConnect()
	waitState(t, d, Keyring)
	h.mu.Lock()
	h.passwordErr, h.password = nil, []byte("new")
	h.mu.Unlock()
	d.SetPassword("new")
	if !slices.Equal(h.stored, []string{"new"}) {
		t.Fatal(h.stored)
	}
	waitState(t, d, Connected)
}

func TestCiscoConnectingLaterPausesRunningTunnel(t *testing.T) {
	// Measured 2026-09-08: Cisco was connected while the tunnel was up, and uni-vpn kept
	// running because the check only happened while connecting.
	h := newHarness(t)
	d := h.start(nil)
	d.RequestConnect()
	waitState(t, d, Connected)
	h.cisco.Store(true)
	waitState(t, d, Blocked)
	if _, m := d.snapshot(); m != messages.Blocked {
		t.Fatal(m)
	}
	if !waitFor(3*time.Second, func() bool { return d.tunnelNow() == nil }) {
		t.Fatal("tunnel left")
	}
	time.Sleep(500 * time.Millisecond)
	if s, _ := d.snapshot(); s != Blocked {
		t.Fatal("without demand, blocked stays visible", s)
	}
	if len(h.pwLines()) != 1 {
		t.Fatal("no reconnect while Cisco is connected")
	}
	h.cisco.Store(false)
	waitState(t, d, Idle)
	d.RequestConnect()
	waitState(t, d, Connected)
	if len(h.pwLines()) != 2 {
		t.Fatal(h.pwLines())
	}
}

func TestCiscoPauseClosesBrowserConnections(t *testing.T) {
	h := newHarness(t)
	d := h.start(nil)
	c := h.client()
	echo(t, c, "x")
	h.cisco.Store(true)
	waitState(t, d, Blocked)
	readEOF(t, c, 3*time.Second)
}

func TestWindowsWithoutElevationExplainsInsteadOfStarting(t *testing.T) {
	h := newHarness(t)
	no := false
	d := h.start(func(o *Options) { o.Elevated = func() *bool { return &no } })
	d.RequestConnect()
	waitState(t, d, Error)
	if _, m := d.snapshot(); m != messages.NotElevated || h.pwLines() != nil {
		t.Fatal(m, h.pwLines())
	}
}

func TestCiscoBlockedThenReleased(t *testing.T) {
	h := newHarness(t)
	h.cisco.Store(true)
	d := h.start(nil)
	d.RequestConnect()
	waitState(t, d, Blocked)
	if h.pwLines() != nil {
		t.Fatal(h.pwLines())
	}
	h.cisco.Store(false)
	waitState(t, d, Connected)
}

func TestOfflineThenOnline(t *testing.T) {
	h := newHarness(t)
	h.probeResult.Store(false)
	d := h.start(nil)
	d.RequestConnect()
	waitState(t, d, Offline)
	if h.pwLines() != nil {
		t.Fatal(h.pwLines())
	}
	h.probeResult.Store(true)
	waitState(t, d, Connected)
}

func TestMissingOpenconnectBinary(t *testing.T) {
	h := newHarness(t)
	h.cfg.OpenConnect = "/nonexistent/openconnect"
	d := h.start(nil)
	d.RequestConnect()
	waitState(t, d, Error)
	if _, m := d.snapshot(); m != messages.ProgramMissing {
		t.Fatal(m)
	}
	if statusValue(t, d, "action") != "repair" {
		t.Fatal(statusValue(t, d, "action"))
	}
}

func TestConfigErrorDaemon(t *testing.T) {
	h := newHarness(t)
	d := h.start(func(o *Options) { o.ConfigError = "config.toml line 2: broken" })
	if s, m := d.snapshot(); s != Error || m != messages.SettingsBroken {
		t.Fatal(s, m)
	}
	if statusValue(t, d, "setup_needed") != true {
		t.Fatal("setup_needed")
	}
	if _, ok := d.acquire(context.Background()); ok {
		t.Fatal("acquired")
	}
}

// --- Self repair ---------------------------------------------------------------------

func TestPortInUseIsRetriedUntilItIsFree(t *testing.T) {
	h := newHarness(t)
	blocker, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(h.cfg.SocksPort))
	if err != nil {
		t.Fatal(err)
	}
	d := h.start(nil)
	if s, m := d.snapshot(); s != Error || m != messages.PortInUse {
		blocker.Close()
		t.Fatal(s, m)
	}
	blocker.Close()
	waitState(t, d, Idle)
	if _, m := d.snapshot(); m != messages.NotConnected || d.forwarder.Addr() == nil {
		t.Fatal(m)
	}
	echo(t, h.client(), "hello")
}

func TestSetupAfterABrokenConfigKeepsTheOldFile(t *testing.T) {
	h := newHarness(t)
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(cfgPath, []byte("user = \"ab1\"\nthis is not toml\n"), 0o600)
	d := h.start(func(o *Options) { o.ConfigError, o.ConfigPath = "line 2: broken", cfgPath })
	if statusValue(t, d, "setup_needed") != true {
		t.Fatal("setup_needed")
	}
	if err := d.CompleteSetup("cd234", "pw", "base32:GEZDGNBVGY3TQOJQ", "heidelberg", nil); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(cfgPath + ".broken"); !strings.Contains(string(data), "this is not toml") {
		t.Fatalf("%q", data)
	}
	written, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	// The daemon keeps listening where it did, so the new file names the same ports.
	if written.User != "cd234" || written.SocksPort != h.cfg.SocksPort || written.HTTPPort != h.cfg.HTTPPort {
		t.Fatal(written.User, written.SocksPort, written.HTTPPort)
	}
	if statusValue(t, d, "setup_needed") != false {
		t.Fatal("setup_needed")
	}
	waitState(t, d, Connected)
}

// --- Repair -------------------------------------------------------------------------

type fakeProcess struct {
	done chan struct{}
	code int
}

func newFakeProcess() *fakeProcess { return &fakeProcess{done: make(chan struct{})} }

func (p *fakeProcess) Poll() (int, bool) {
	select {
	case <-p.done:
		return p.code, true
	default:
		return 0, false
	}
}

func (p *fakeProcess) Wait() int {
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
	}
	return p.code
}

func (p *fakeProcess) finish(code int) {
	p.code = code
	close(p.done)
}

func (d *Daemon) forceSet(state State, message messages.Message) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.set(state, message)
}

func TestRepairRunsTheInstallerAndReportsAFailure(t *testing.T) {
	h := newHarness(t)
	process := newFakeProcess()
	starts := 0
	d := h.start(func(o *Options) {
		o.RepairStart = func() (Process, error) { starts++; return process, nil }
	})
	d.forceSet(Error, messages.ProgramMissing)
	if statusValue(t, d, "action") != "repair" {
		t.Fatal()
	}
	d.StartRepair()
	d.StartRepair() // a second click while it runs starts nothing
	if starts != 1 {
		t.Fatal(starts)
	}
	if _, m := d.snapshot(); m != messages.Repairing || statusValue(t, d, "repairing") != true ||
		statusValue(t, d, "action") != nil {
		t.Fatal(m)
	}
	process.finish(1)
	waitFor(3*time.Second, func() bool { _, m := d.snapshot(); return m == messages.RepairFailed })
	if _, m := d.snapshot(); m != messages.RepairFailed || statusValue(t, d, "repairing") != false ||
		statusValue(t, d, "action") != "repair" {
		t.Fatal(m)
	}
}

func TestRepairThatCannotStartSaysSo(t *testing.T) {
	h := newHarness(t)
	d := h.start(func(o *Options) {
		o.RepairStart = func() (Process, error) { return nil, &notFoundError{"powershell.exe"} }
	})
	d.forceSet(Error, messages.ProgramMissing)
	d.StartRepair()
	if _, m := d.snapshot(); m != messages.RepairFailed || statusValue(t, d, "action") != "repair" {
		t.Fatal(m)
	}
}

func TestRepairThatSucceedsWithoutARestartClearsTheError(t *testing.T) {
	h := newHarness(t)
	process := newFakeProcess()
	d := h.start(func(o *Options) { o.RepairStart = func() (Process, error) { return process, nil } })
	d.forceSet(Error, messages.NotElevated)
	d.StartRepair()
	process.finish(0)
	waitState(t, d, Idle)
	if _, m := d.snapshot(); m != messages.NotConnected {
		t.Fatal(m)
	}
}

// --- Profiles ------------------------------------------------------------------------

func profileHarness(t *testing.T) (*harness, string) {
	h := newHarness(t)
	args := filepath.Join(t.TempDir(), "args")
	t.Setenv("FAKE_ARGS_FILE", args)
	return h, args
}

func lastArgs(t *testing.T, path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var args []string
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &args); err != nil {
		t.Fatal(err)
	}
	return args
}

func TestWithoutTOTPNoSecretIsRead(t *testing.T) {
	h, args := profileHarness(t)
	h.cfg.MFA = "none"
	h.totpErr = credentials.ErrTotpMissing
	d := h.start(nil)
	d.RequestConnect()
	waitState(t, d, Connected)
	if !slices.Equal(h.pwLines(), []string{"pw-s3cret"}) {
		t.Fatal(h.pwLines())
	}
	if _, err := os.Stat(h.tokenfile); err == nil {
		t.Fatal("token file")
	}
	for _, a := range lastArgs(t, args) {
		if strings.HasPrefix(a, "--token") {
			t.Fatal(a)
		}
	}
}

func TestSAMLIsRefusedBeforeAnythingStarts(t *testing.T) {
	h, args := profileHarness(t)
	h.cfg.MFA = "saml"
	d := h.start(nil)
	d.RequestConnect()
	waitState(t, d, AuthFailed)
	if _, m := d.snapshot(); m != messages.SAMLRequired || h.pwLines() != nil {
		t.Fatal(m)
	}
	if _, err := os.Stat(args); err == nil {
		t.Fatal("openconnect started")
	}
}

func TestTOTPAppendSendsPasswordAndCodeInOneLine(t *testing.T) {
	h, _ := profileHarness(t)
	h.cfg.MFA = "totp_append"
	var d *Daemon
	d = h.start(func(o *Options) {
		o.TunnelFactory = func() (Tunnel, error) {
			tun, err := d.makeTunnel()
			if err == nil {
				tun.(*tunnel.Tunnel).TOTPCode = func(string, time.Time) (string, error) { return "123456", nil }
			}
			return tun, err
		}
	})
	d.RequestConnect()
	waitState(t, d, Connected)
	if !slices.Equal(h.pwLines(), []string{"pw-s3cret123456"}) {
		t.Fatal(h.pwLines())
	}
	if _, err := os.Stat(h.tokenfile); err == nil {
		t.Fatal("token file")
	}
}

func TestTOTPAppendWithoutASecretIsReportedLikeAMissingOne(t *testing.T) {
	h, _ := profileHarness(t)
	h.cfg.MFA = "totp_append"
	h.totp = []byte("  ")
	d := h.start(nil)
	d.RequestConnect()
	waitState(t, d, Keyring)
	if _, m := d.snapshot(); m != messages.TOTPMissing || h.pwLines() != nil {
		t.Fatal(m, h.pwLines())
	}
}

func TestTOTPAppendWithAMalformedSecretIsAFinalAuthFailure(t *testing.T) {
	h, _ := profileHarness(t)
	h.cfg.MFA = "totp_append"
	h.totp = []byte("base32:!!!")
	d := h.start(nil)
	d.RequestConnect()
	waitState(t, d, AuthFailed)
	if _, m := d.snapshot(); m != messages.TOTPUnusable || d.tunnelNow() != nil || h.pwLines() != nil {
		t.Fatal(m)
	}
}

func TestDuoPushSendsPushAndNeedsNoSecret(t *testing.T) {
	h, args := profileHarness(t)
	h.cfg.MFA = "duo_push"
	h.totpErr = credentials.ErrTotpMissing
	d := h.start(nil)
	d.RequestConnect()
	waitState(t, d, Connected)
	if !slices.Equal(h.pwLines(), []string{"pw-s3cret", "push"}) {
		t.Fatal(h.pwLines())
	}
	if slices.Contains(lastArgs(t, args), "--non-inter") {
		t.Fatal("--non-inter")
	}
}

func TestProfileFlagsReachOpenconnect(t *testing.T) {
	h, args := profileHarness(t)
	h.cfg.Authgroup, h.cfg.UsernameSuffix = "staff-net", "@staff-net.ethz.ch"
	d := h.start(nil)
	d.RequestConnect()
	waitState(t, d, Connected)
	got := lastArgs(t, args)
	if !slices.Contains(got, "--authgroup=staff-net") || !slices.Contains(got, "--user=u@staff-net.ethz.ch") {
		t.Fatal(got)
	}
}

func TestOTPWaitOnlyWithTOTP(t *testing.T) {
	h, _ := profileHarness(t)
	h.cfg.MFA = "none"
	d := h.start(nil)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lastOTPStep, d.hasOTPStep = 1000/30, true
	if d.realOTPWait(1000) != 0 {
		t.Fatal()
	}
}

func TestStatusNamesUniversityAndSecondFactor(t *testing.T) {
	h, _ := profileHarness(t)
	h.cfg.DefaultDomains = []string{"intranet.example.edu"}
	d := h.start(nil)
	for key, want := range map[string]any{"university": "heidelberg", "university_name": "Heidelberg University",
		"mfa": "totp_field", "mfa_portal_url": "https://mfa.uni-heidelberg.de/"} {
		if got := statusValue(t, d, key); got != want {
			t.Fatal(key, got)
		}
	}
	if len(statusValue(t, d, "mfa_steps").([]string)) == 0 {
		t.Fatal("mfa_steps")
	}
	if got := statusValue(t, d, "domains").([]string); !slices.Equal(got, []string{"intranet.example.edu"}) {
		t.Fatal("no domains.txt: the profile's defaults", got)
	}
	if p, _ := d.PAC(); !strings.Contains(p, "intranet.example.edu") {
		t.Fatal(p)
	}
}

// --- Status ------------------------------------------------------------------------

func TestStatusKeys(t *testing.T) {
	h := newHarness(t)
	d := h.start(nil)
	s, _ := d.Status()
	for _, key := range []string{"protocol", "version", "state", "message", "since", "host", "user", "socks_port",
		"http_port", "active_connections", "bytes_in", "bytes_out", "last_error", "log_tail"} {
		if _, ok := s.Get(key); !ok {
			t.Fatal(key)
		}
	}
	if v, _ := s.Get("protocol"); v != 1 {
		t.Fatal(v)
	}
	if v, _ := s.Get("state"); v != "idle" {
		t.Fatal(v)
	}
}

// The keys and their order are those of daemon.py's status(); values Python would write the same.
func TestStatusKeyOrderLikePython(t *testing.T) {
	h := newHarness(t)
	d := h.start(nil)
	s, _ := d.Status()
	var keys []string
	for _, m := range s {
		keys = append(keys, m.Key)
	}
	want := []string{"protocol", "version", "commit", "auto_update", "language", "update_pending", "state", "message",
		"message_id", "message_t", "action", "since", "host", "user", "university", "university_name", "mfa", "mfa_portal_url",
		"mfa_steps", "socks_port", "http_port", "idle_minutes", "active_connections", "bytes_in", "bytes_out",
		"connects", "last_error", "domains", "pac_url", "pac_refresh", "log_tail", "setup_needed", "repairing",
		"busy", "error_kind", "platform", "elevated"}
	if !slices.Equal(keys, want) {
		t.Fatal(keys)
	}
	text := string(pyjson.Marshal(s))
	for _, part := range []string{`"commit": null`, `"update_pending": null`, `"message_id": "not_connected"`,
		`"action": null`, `"idle_minutes": 0.01`, `"last_error": null`, `"error_kind": null`, `"elevated": null`} {
		if !strings.Contains(text, part) {
			t.Fatal(part, text)
		}
	}
}

func TestResumeTriggersReconnect(t *testing.T) {
	h := newHarness(t)
	d := h.start(nil)
	c := h.client()
	echo(t, c, "x")
	if s, _ := d.snapshot(); s != Connected {
		t.Fatal(s)
	}
	h.wallOffset.Store(120)
	time.Sleep(400 * time.Millisecond)
	if !strings.Contains(h.logs.String(), "Resume detected, reconnecting the tunnel") {
		t.Fatal(h.logs.String())
	}
	// The old tunnel is gone, the browser connection was closed ...
	readEOF(t, c, 3*time.Second)
	// ... and since there was still demand, a new tunnel is up.
	waitState(t, d, Connected)
	if len(h.pwLines()) != 2 {
		t.Fatal(h.pwLines())
	}
}

// --- Constants shared with the Python core --------------------------------------------

func repoRoot(t *testing.T) string {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestVersionAndProtocolMatchThePythonPackage(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "uni_vpn", "__init__.py"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "__version__ = \""+Version+"\"\n") {
		t.Fatal("__version__ differs from", Version)
	}
	if !strings.Contains(text, "PROTOCOL = "+strconv.Itoa(Protocol)+"\n") {
		t.Fatal("PROTOCOL differs from", Protocol)
	}
}
