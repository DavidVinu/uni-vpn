//go:build !windows

// The daemon tests drive the POSIX tunnel with the fake openconnect, like tests/test_daemon.py (posix_only).

package daemon

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DavidVinu/uni-vpn/internal/config"
	"github.com/DavidVinu/uni-vpn/internal/credentials"
	"github.com/DavidVinu/uni-vpn/internal/i18n"
	"github.com/DavidVinu/uni-vpn/internal/messages"
	"github.com/DavidVinu/uni-vpn/internal/pac"
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

// stopped: Disconnect ended the loop for good, the page that waited for a tunnel is dropped.
func stopped(t *testing.T, d *Daemon, c net.Conn) {
	t.Helper()
	time.Sleep(500 * time.Millisecond)
	s, m := d.snapshot()
	d.mu.Lock()
	running := d.loopRunning
	d.mu.Unlock()
	if s != Idle || running {
		t.Fatal(s, m, running)
	}
	readEOF(t, c, time.Second)
}

func TestDisconnectWhileWaitingForTheNextCodeDoesNotRestart(t *testing.T) {
	// "Waiting for the next code" has no tunnel yet; the page that asked for it is still waiting.
	h := newHarness(t)
	h.cfg.ClientWait = 5
	d := h.start(nil, func(d *Daemon) { d.otpWait = func(float64) float64 { return 3 } })
	c := h.client()
	if !waitFor(3*time.Second, func() bool { _, m := d.snapshot(); return m == messages.WaitingForCode }) {
		t.Fatal(d.snapshot())
	}
	d.RequestDisconnect()
	stopped(t, d, c)
	if h.pwLines() != nil {
		t.Fatal(h.pwLines())
	}
}

func TestDisconnectWhileOfflineDoesNotRestart(t *testing.T) {
	// A page load looks for the network again (slow while Wi-Fi comes back), then Disconnect.
	h := newHarness(t)
	h.cfg.RetryInterval, h.cfg.ClientWait = 30, 5
	var probes atomic.Int64
	d := h.start(func(o *Options) {
		o.Probe = func(ctx context.Context) bool {
			if probes.Add(1) > 1 {
				select {
				case <-time.After(2 * time.Second):
				case <-ctx.Done():
				}
			}
			return false
		}
	}, func(d *Daemon) { d.offlineRecheck = 0.2 })
	d.RequestConnect()
	waitState(t, d, Offline)
	time.Sleep(300 * time.Millisecond)
	c := h.client()
	if !waitFor(2*time.Second, func() bool { return probes.Load() == 2 }) {
		t.Fatal(probes.Load())
	}
	d.RequestDisconnect()
	stopped(t, d, c)
	if probes.Load() != 2 {
		t.Fatal(probes.Load())
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
	waitState(t, d, Idle, 10*time.Second) // the harness's 3 s idle timer, on a slow runner too
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
	waitState(t, d, Idle, 10*time.Second)
	if d.tunnelNow() != nil {
		t.Fatal("tunnel left")
	}
}

// Logout, an update's restart or Repair stop the service while openconnect takes a moment to
// sign off. A Connect (or a new password) then must not sign in again with an openconnect that
// outlives the service.
func TestConnectDuringShutdownStartsNoTunnel(t *testing.T) {
	h := newHarness(t)
	t.Setenv("FAKE_MODE", "ignore_sigterm") // killed after stop_grace (1 s)
	d := h.start(nil)
	c := h.client()
	echo(t, c, "x")
	waitState(t, d, Connected)
	before := len(h.pwLines())
	go d.Stop()
	time.Sleep(300 * time.Millisecond) // Run is stopping the tunnel
	d.RequestConnect()
	select {
	case <-h.done:
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not end")
	}
	time.Sleep(1500 * time.Millisecond)
	if after := len(h.pwLines()); after != before {
		t.Fatalf("sign-ins %d -> %d: an openconnect started while the service stopped", before, after)
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

// openconnect 9.01 at Stanford or UIUC (pass 24): the web sign-in of the default group ends the
// attempt at once with a final message, without Duo's 75 s wait and without another try.
func TestWebSignInIsFinal(t *testing.T) {
	for _, mode := range []string{"sso_browser", "sso_embedded"} {
		t.Run(mode, func(t *testing.T) {
			h, args := profileHarness(t)
			h.cfg.MFA = "duo_push"
			t.Setenv("FAKE_MODE", mode)
			d := h.start(nil)
			d.RequestConnect()
			waitState(t, d, AuthFailed, 5*time.Second)
			if statusValue(t, d, "message_id") != "saml_required" || statusValue(t, d, "action") != nil {
				t.Fatal(statusValue(t, d, "message_id"), statusValue(t, d, "action"))
			}
			time.Sleep(600 * time.Millisecond)
			if data, _ := os.ReadFile(args); strings.Count(string(data), "\n") != 1 {
				t.Fatalf("%d starts", strings.Count(string(data), "\n"))
			}
		})
	}
}

func TestGatewayTroubleIsRetriedNotTakenForAWrongPassword(t *testing.T) {
	// The gateway answered 503 (busy, maintenance), before or after the login: the password was
	// never checked, so no "check your password" that stops all further attempts.
	for _, mode := range []string{"gateway_503", "cstp_503"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t)
			t.Setenv("FAKE_MODE", mode)
			d := h.start(nil)
			d.RequestConnect()
			if !waitFor(4*time.Second, func() bool { return len(h.pwLines()) >= 2 }) {
				s, m := d.snapshot()
				t.Fatal(s, m, h.pwLines())
			}
			if s, _ := d.snapshot(); s == AuthFailed || d.lastErrorMessage() != messages.ConnectFailed.Text {
				t.Fatal(s, d.lastErrorMessage())
			}
		})
	}
}

func TestAClientCertificateRequestIsNoCertificateProblem(t *testing.T) {
	// Gateways that also take device certificates ask for one first; a wrong password after
	// that is still a wrong password, not an untrusted server.
	h := newHarness(t)
	t.Setenv("FAKE_MODE", "clientcert_auth_fail")
	d := h.start(nil)
	d.RequestConnect()
	waitState(t, d, AuthFailed)
	if _, m := d.snapshot(); m != messages.PasswordRejected {
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
	// Only the certificates file and the last used code window stay (tunnel.CAFileName).
	for _, e := range func() []os.DirEntry { entries, _ := os.ReadDir(h.tokenDir); return entries }() {
		if !slices.Contains([]string{tunnel.CAFileName, otpStepName, signedInName, addedDefaultsName, pacSumName}, e.Name()) {
			t.Fatal(e.Name())
		}
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
	if len(h.pwLines()) != 2 {
		t.Fatal("one more try with the next code, then no automatic attempt", h.pwLines())
	}
}

func TestTOTPUsedByAnotherDeviceIsTriedOnceMoreWithTheNextCode(t *testing.T) {
	// Synced devices share the secret: another one signed in with this window's code a moment
	// before, and LinOTP takes each code only once. The next window's code gets through.
	h := newHarness(t)
	t.Setenv("FAKE_MODE", "totp_replayed")
	t.Setenv("FAKE_ARGS_FILE", filepath.Join(t.TempDir(), "args"))
	d := h.start(nil, func(d *Daemon) {
		waits := []float64{0, 0.8} // the first code is unused here, the second waits for its window
		d.otpWait = func(float64) float64 {
			if len(waits) == 0 {
				return 0
			}
			w := waits[0]
			waits = waits[1:]
			return w
		}
	})
	started := time.Now()
	d.RequestConnect()
	if !waitFor(4*time.Second, func() bool { _, m := d.snapshot(); return m == messages.WaitingForCode }) {
		t.Fatal(d.snapshot())
	}
	waitState(t, d, Connected)
	if time.Since(started) < 800*time.Millisecond || len(h.pwLines()) != 2 {
		t.Fatal(time.Since(started), h.pwLines())
	}
}

// The same as TestTOTPUsedByAnotherDeviceIsTriedOnceMoreWithTheNextCode, for a gateway whose
// code field is in the password's form (secondary_password): ethz, bremen, muenster,
// harvard-fasrc, ugr, w-hs; and marburg's totp_append. This password signed in before; another
// synced device signed in with this window's code a moment ago.
func TestCodeInPasswordFormUsedByAnotherDeviceIsTriedOnceMore(t *testing.T) {
	h := newHarness(t)
	args := filepath.Join(t.TempDir(), "args")
	t.Setenv("FAKE_ARGS_FILE", args)
	d := h.start(nil, func(d *Daemon) {
		waits := []float64{0, 0, 0.8}
		d.otpWait = func(float64) float64 {
			if len(waits) == 0 {
				return 0
			}
			w := waits[0]
			waits = waits[1:]
			return w
		}
	})
	d.RequestConnect()
	waitState(t, d, Connected)
	d.RequestDisconnect()
	waitState(t, d, Idle)
	os.Remove(args)
	d.mu.Lock()
	d.hasOTPStep = false // the last sign-in was in an earlier window (minutes ago)
	d.mu.Unlock()
	t.Setenv("FAKE_MODE", "append_replayed")
	d.RequestConnect()
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		st, m := d.snapshot()
		if st == Connected {
			return
		}
		if st == AuthFailed {
			time.Sleep(500 * time.Millisecond)
			t.Fatalf("stranded in %s (%s); the next window's code was never tried", st, m.ID)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(d.snapshot())
}

// A password never accepted here: no second attempt (the account may lock).
func TestCodeInPasswordFormWithoutEarlierSignInIsNotTriedAgain(t *testing.T) {
	h := newHarness(t)
	t.Setenv("FAKE_MODE", "append_rejected")
	d := h.start(nil)
	d.RequestConnect()
	waitState(t, d, AuthFailed)
	time.Sleep(500 * time.Millisecond)
	if len(h.pwLines()) != 1 {
		t.Fatal(h.pwLines())
	}
}

// The iPhone's tunnel extension runs a core of its own on the app's folders, and a service
// restarts after an update: a password that signed in with the core before is tried once more
// with the next code there too.
func TestCodeInPasswordFormRetryAcrossCores(t *testing.T) {
	h := newHarness(t)
	args := filepath.Join(t.TempDir(), "args")
	t.Setenv("FAKE_ARGS_FILE", args)
	app := h.start(nil)
	app.RequestConnect()
	waitState(t, app, Connected)
	app.RequestDisconnect()
	waitState(t, app, Idle)
	h.teardown()
	h.d = nil
	os.Remove(args)
	t.Setenv("FAKE_MODE", "append_replayed")
	ext := h.start(nil, func(d *Daemon) { d.hasOTPStep = false }) // minutes later
	ext.RequestConnect()
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		st, m := ext.snapshot()
		if st == Connected {
			return
		}
		if st == AuthFailed {
			time.Sleep(500 * time.Millisecond)
			t.Fatalf("stranded in %s (%s); the next window's code was never tried", st, m.ID)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(ext.snapshot())
}

// A password changed after the last sign-in never signed in: the next core does not try it twice.
func TestChangedPasswordIsNotTriedTwiceByTheNextCore(t *testing.T) {
	h := newHarness(t)
	app := h.start(nil)
	app.RequestConnect()
	waitState(t, app, Connected)
	app.RequestDisconnect()
	waitState(t, app, Idle)
	if err := app.SetPassword("new-pw"); err != nil {
		t.Fatal(err)
	}
	// The new password starts a connection: let it end before the core stops, or an
	// openconnect still being started can hold the ports while the next core binds them.
	app.RequestDisconnect()
	waitState(t, app, Idle)
	h.teardown()
	h.d = nil
	os.Remove(h.pwfile)
	t.Setenv("FAKE_MODE", "append_rejected")
	ext := h.start(nil, func(d *Daemon) { d.hasOTPStep = false })
	ext.RequestConnect()
	waitState(t, ext, AuthFailed)
	time.Sleep(500 * time.Millisecond)
	if len(h.pwLines()) != 1 {
		t.Fatal(h.pwLines())
	}
}

// The password changed at the university after the last sign-in: refused with the next code
// too, it gets no more second attempts, also not from the next core (the account may lock).
func TestPasswordRefusedTwiceGetsNoMoreRetries(t *testing.T) {
	h := newHarness(t)
	d := h.start(nil)
	d.RequestConnect()
	waitState(t, d, Connected)
	d.RequestDisconnect()
	waitState(t, d, Idle)
	os.Remove(h.pwfile)
	t.Setenv("FAKE_MODE", "append_rejected")
	d.mu.Lock()
	d.hasOTPStep = false
	d.mu.Unlock()
	d.RequestConnect()
	waitState(t, d, AuthFailed)
	time.Sleep(500 * time.Millisecond)
	if len(h.pwLines()) != 2 {
		t.Fatal("first time", h.pwLines())
	}
	attempt := func(d *Daemon) {
		t.Helper()
		os.Remove(h.pwfile)
		d.mu.Lock()
		d.hasOTPStep = false // minutes later
		d.mu.Unlock()
		d.RequestConnect()
		deadline := time.Now().Add(3 * time.Second)
		for len(h.pwLines()) == 0 && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		time.Sleep(time.Second)
		if st, _ := d.snapshot(); st != AuthFailed || len(h.pwLines()) != 1 {
			t.Fatal(st, h.pwLines())
		}
	}
	attempt(d)
	h.teardown()
	h.d = nil
	attempt(h.start(nil))
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

// The iPhone's app and its tunnel extension each run a core on the same folders, and a service
// restarts after an update: the next core knows the window whose code was just used (a test code
// typed into the MFA portal, or the last sign-in), since a code is accepted only once.
func TestUsedCodeWindowSurvivesARestart(t *testing.T) {
	h := newHarness(t)
	first := h.start(nil)
	first.NoteCodeShown() // /api/totp/check
	first.mu.Lock()
	step := first.lastOTPStep
	first.mu.Unlock()
	h.teardown()
	h.d = nil
	next := h.start(nil)
	now := float64(time.Now().UnixNano()) / 1e9
	if int64(math.Floor(now/OTPStep)) != step {
		t.Skip("crossed a window boundary")
	}
	next.mu.Lock()
	wait := next.realOTPWait(now)
	next.mu.Unlock()
	if wait == 0 {
		t.Fatal("the next core would sign in with the code just used")
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
	// Not just a pid: before the fake ignores SIGTERM the stop ends at once, the restart is
	// over before the Disconnect and rightly starts a second attempt.
	waitFor(5*time.Second, func() bool { return len(h.pwLines()) > 0 })
	d.mu.Lock()
	gen := d.secretsGen
	d.mu.Unlock()
	saving := make(chan struct{})
	go func() { d.SetPassword("second"); close(saving) }()
	// The Disconnect must land while the restart stops the tunnel, not before it starts.
	waitFor(2*time.Second, func() bool { d.mu.Lock(); defer d.mu.Unlock(); return d.secretsGen != gen })
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

func TestDisconnectWhileThePortOpensDoesNotReconnect(t *testing.T) {
	// openconnect may open its port while the Disconnect stops it (here SIGTERM waits for the
	// kill): that is no connection, whose demand would sign in again once the tunnel is gone.
	h := newHarness(t)
	t.Setenv("FAKE_MODE", "ignore_sigterm")
	t.Setenv("FAKE_DELAY", "1") // the port opens well after the Disconnect...
	h.cfg.StopGrace = 4         // ...and well before the kill
	h.cfg.ReadyTimeout = 10
	h.cfg.DemandWindow = 60 // a demand from "connected" would outlast the stop
	h.cfg.IdleMinutes = 15  // the idle timer must not end it first
	h.cfg.MFA = "none"      // the fake would read the token file the stop removed
	d := h.start(nil)
	d.RequestConnect()
	waitFor(10*time.Second, func() bool { return len(h.pwLines()) > 0 }) // SIGTERM is handled
	d.RequestDisconnect()
	waitState(t, d, Idle, 15*time.Second) // after the kill, on a slow runner too
	time.Sleep(500 * time.Millisecond)
	d.mu.Lock()
	state, count := d.state, d.connectCount
	d.mu.Unlock()
	if state != Idle || count != 1 || d.tunnelNow() != nil {
		t.Fatal("a new attempt after the Disconnect", state, count)
	}
}

func TestBytesDuringADisconnectAreNoDemand(t *testing.T) {
	// A download passes its last bytes while the Disconnect closes the connections: no new
	// use, which would sign in again (a new Duo push) right after the Disconnect.
	h := newHarness(t)
	h.cfg.DemandWindow = 60
	h.cfg.IdleMinutes = 15
	d := h.start(nil, func(d *Daemon) {
		closeAll := d.closeAll
		d.closeAll = func() {
			d.noteActivity() // what the forwarder does for a chunk in flight
			closeAll()
		}
	})
	echo(t, h.client(), "x")
	d.RequestDisconnect()
	waitState(t, d, Idle)
	time.Sleep(time.Second)
	d.mu.Lock()
	state, count := d.state, d.connectCount
	d.mu.Unlock()
	if state != Idle || count != 1 {
		t.Fatal("signed in again after the Disconnect", state, count)
	}
}

func TestDisconnectWhileThePortOpensBeforeTheStopDoesNotReconnect(t *testing.T) {
	// The port opens after the Disconnect said "disconnecting" but before it stops the tunnel
	// (it closes the connections first): still no connection.
	h := newHarness(t)
	t.Setenv("FAKE_DELAY", "0.5")
	h.cfg.StopGrace = 4
	h.cfg.ReadyTimeout = 10
	h.cfg.DemandWindow = 60
	h.cfg.IdleMinutes = 15
	h.cfg.MFA = "none"
	d := h.start(nil, func(d *Daemon) {
		closeAll := d.closeAll
		d.closeAll = func() {
			time.Sleep(1200 * time.Millisecond) // the port opens meanwhile
			closeAll()
		}
	})
	d.RequestConnect()
	waitFor(10*time.Second, func() bool { return len(h.pwLines()) > 0 })
	d.RequestDisconnect()
	waitState(t, d, Idle, 15*time.Second)
	time.Sleep(1500 * time.Millisecond)
	d.mu.Lock()
	state, count := d.state, d.connectCount
	d.mu.Unlock()
	if state != Idle || count != 1 {
		t.Fatal("a new attempt after the Disconnect", state, count)
	}
}

func TestStaleTokenFilesAreRemovedAtStart(t *testing.T) {
	h := newHarness(t)
	os.WriteFile(filepath.Join(h.tokenDir, "totp-old"), []byte("base32:OLD"), 0o600)
	h.start(nil)
	if _, err := os.Stat(filepath.Join(h.tokenDir, "totp-old")); err == nil {
		t.Fatal("totp-old left")
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

func TestOfflineAPageLoadLooksForTheNetworkAgain(t *testing.T) {
	// Wi-Fi comes back a few seconds after waking the laptop: the next page load gets through
	// instead of failing until the next try (retry_interval, 30 s).
	h := newHarness(t)
	h.cfg.RetryInterval = 30
	h.probeResult.Store(false)
	var probes atomic.Int64
	d := h.start(func(o *Options) {
		o.Probe = func(context.Context) bool { probes.Add(1); return h.probeResult.Load() }
	}, func(d *Daemon) { d.offlineRecheck = 1 })
	readEOF(t, h.client(), 3*time.Second)
	// A page with many requests probes once, not once for each.
	for range 5 {
		readEOF(t, h.client(), time.Second)
	}
	if s, _ := d.snapshot(); s != Offline || probes.Load() != 1 {
		t.Fatal(s, probes.Load())
	}
	time.Sleep(1200 * time.Millisecond)
	h.probeResult.Store(true)
	echo(t, h.client(), "x")
	if probes.Load() != 2 {
		t.Fatal(probes.Load())
	}
}

// Some universities' gateways cannot be reached from their own network (Halle, HS Flensburg):
// there the listed websites open directly instead of failing while the gateway is away.
func TestListedWebsitesGoDirectlyWhileTheGatewayCannotBeReached(t *testing.T) {
	h := newHarness(t)
	h.cfg.RetryInterval = 30
	h.probeResult.Store(false)
	if err := pac.WriteDomains(h.domainsPath, []string{"uni-halle.de"}); err != nil {
		t.Fatal(err)
	}
	d := h.start(nil)
	if hosts := d.forwarder.Direct(); slices.Contains(hosts, "uni-halle.de") {
		t.Fatal(hosts)
	}
	d.RequestConnect()
	waitState(t, d, Offline)
	if hosts := d.forwarder.Direct(); !slices.Contains(hosts, "uni-halle.de") {
		t.Fatal(hosts)
	}
	h.probeResult.Store(true)
	h.wallOffset.Store(120)
	waitState(t, d, Connected, 3*time.Second)
	if hosts := d.forwarder.Direct(); slices.Contains(hosts, "uni-halle.de") {
		t.Fatal(hosts)
	}
}

// A dock or an iPhone plugged in later brings a new network service on macOS: the core looks
// for one at the start and again every few minutes.
func TestNewNetworkServicesGetTheRule(t *testing.T) {
	h := newHarness(t)
	h.start(nil)
	deadline := time.Now().Add(5 * time.Second)
	for h.covered.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if n := h.covered.Load(); n < 2 {
		t.Fatal(n)
	}
}

func TestOfflineResumeLooksForTheNetworkAgain(t *testing.T) {
	h := newHarness(t)
	h.cfg.RetryInterval = 30
	h.probeResult.Store(false)
	d := h.start(nil)
	d.RequestConnect()
	waitState(t, d, Offline)
	h.probeResult.Store(true)
	h.wallOffset.Store(120) // the laptop slept for two minutes
	waitState(t, d, Connected, 3*time.Second)
}

func TestNonSocksConnectionDoesNotSignIn(t *testing.T) {
	// A web page's <img src="http://127.0.0.1:1080/pixel.gif">.
	h := newHarness(t)
	d := h.start(nil)
	c, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(h.cfg.SocksPort)))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte("GET /pixel.gif HTTP/1.1\r\nHost: 127.0.0.1:1080\r\n\r\n"))
	readEOF(t, c, 3*time.Second)
	time.Sleep(300 * time.Millisecond)
	if s, _ := d.snapshot(); s != Idle || h.pwLines() != nil {
		t.Fatal(s, h.pwLines())
	}
	echo(t, h.client(), "x")
}

func TestProbeAsksTheGatewayOfTheCurrentAccount(t *testing.T) {
	// The service started with Heidelberg's defaults, then the assistant set up another one.
	var mu sync.Mutex
	var asked []string
	t.Cleanup(func() { probe = DefaultProbe }) // after the daemon stopped (cleanups run last first)
	probe = func(_ context.Context, host string, _ time.Duration) bool {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, host)
		return true
	}
	h := newHarness(t)
	h.cfg.User = ""
	d := h.start(func(o *Options) {
		o.ConfigError, o.ConfigPath, o.NeedsSetup = "missing", filepath.Join(t.TempDir(), "config.toml"), true
		o.Probe = nil
	})
	if err := d.CompleteSetup("ab123", "pw", "", "other",
		[]config.Setting{{Key: "host", Value: "vpn.example.edu"}, {Key: "mfa", Value: "none"}}); err != nil {
		t.Fatal(err)
	}
	waitState(t, d, Connected)
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(asked, []string{"vpn.example.edu"}) {
		t.Fatal(asked)
	}
}

func TestDefaultProbe(t *testing.T) {
	// A "Not listed" gateway on its own port.
	gateway := httptest.NewTLSServer(http.NotFoundHandler())
	defer gateway.Close()
	if DefaultProbe(context.Background(), gateway.Listener.Addr().String(), 3*time.Second) {
		t.Fatal("a certificate nobody trusts (a captive portal's) counts as online")
	}
	probeRoots = x509.NewCertPool()
	probeRoots.AddCert(gateway.Certificate())
	defer func() { probeRoots = nil }()
	if !DefaultProbe(context.Background(), gateway.Listener.Addr().String(), 3*time.Second) {
		t.Fatal(gateway.Listener.Addr())
	}
	// No TLS at all (a captive portal), or nothing there: offline.
	plain, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := plain.Accept()
			if err != nil {
				return
			}
			c.Write([]byte("HTTP/1.1 302 Found\r\nLocation: http://portal.example/\r\n\r\n"))
			c.Close()
		}
	}()
	host := plain.Addr().String()
	if DefaultProbe(context.Background(), host, 3*time.Second) {
		t.Fatal(host)
	}
	plain.Close()
	if DefaultProbe(context.Background(), host, 3*time.Second) {
		t.Fatal(host)
	}
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

func TestStatusPortTakenIsRetriedUntilItIsFree(t *testing.T) {
	// Fast user switching: the other account's uni-vpn holds the port until it signs out.
	h := newHarness(t)
	blocker, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(h.cfg.HTTPPort))
	if err != nil {
		t.Fatal(err)
	}
	d := h.start(nil)
	if d.HTTP() != nil {
		blocker.Close()
		t.Fatal("bound a taken port")
	}
	blocker.Close()
	if !waitFor(3*time.Second, func() bool { return d.HTTP() != nil }) {
		t.Fatal("status port not bound again")
	}
	if r := h.http("GET", "/status.json", nil, ""); r.status != 200 {
		t.Fatal(r.status)
	}
}

func TestConnectWhileThePortIsTakenStartsNoTunnel(t *testing.T) {
	// Another proxy holds the browsers' port and the user presses Connect anyway: a tunnel
	// nobody can reach, and port_in_use would turn into "not connected" while it runs.
	h := newHarness(t)
	blocker, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(h.cfg.SocksPort))
	if err != nil {
		t.Fatal(err)
	}
	d := h.start(nil)
	d.RequestConnect()
	time.Sleep(500 * time.Millisecond)
	if s, m := d.snapshot(); s != Error || m != messages.PortInUse || h.pwLines() != nil || d.tunnelNow() != nil {
		blocker.Close()
		t.Fatal(s, m, h.pwLines())
	}
	blocker.Close()
	waitState(t, d, Idle)
	d.RequestConnect()
	waitState(t, d, Connected)
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

func TestRepairWithAMissingProgramSaysToDownloadAgain(t *testing.T) {
	// Setup cannot bring back openconnect: no Repair button that would only fail again.
	h := newHarness(t)
	process := newFakeProcess()
	d := h.start(func(o *Options) { o.RepairStart = func() (Process, error) { return process, nil } })
	d.forceSet(Error, messages.ProgramMissing)
	d.StartRepair()
	process.finish(messages.ReinstallNeededExit)
	waitFor(3*time.Second, func() bool { _, m := d.snapshot(); return m != messages.Repairing })
	if state, m := d.snapshot(); state != Error || m != messages.ReinstallNeeded ||
		statusValue(t, d, "message_id") != "reinstall_needed" || statusValue(t, d, "action") != nil {
		t.Fatal(state, m)
	}
}

func TestRepairWithAMissingProgramKeepsSayingSo(t *testing.T) {
	// Until the service restarts, each attempt finds the program missing again (one while
	// Repair runs, Connect after it): still no Repair button.
	h := newHarness(t)
	h.cfg.OpenConnect = "/nonexistent/openconnect"
	process := newFakeProcess()
	d := h.start(func(o *Options) { o.RepairStart = func() (Process, error) { return process, nil } })
	attempt := func() {
		d.RequestConnect()
		waitFor(3*time.Second, func() bool { d.mu.Lock(); defer d.mu.Unlock(); return !d.loopRunning })
	}
	attempt()
	d.StartRepair()
	attempt()
	process.finish(messages.ReinstallNeededExit)
	if !waitFor(3*time.Second, func() bool { _, m := d.snapshot(); return m == messages.ReinstallNeeded }) {
		t.Fatal(d.snapshot())
	}
	attempt()
	if state, m := d.snapshot(); state != Error || m != messages.ReinstallNeeded || statusValue(t, d, "action") != nil {
		t.Fatal(state, m)
	}
}

func TestConnectDuringARepairKeepsRepairingAndItsFailure(t *testing.T) {
	// Connect (or a browser) while Repair runs finds the program missing again: the app still
	// says "Repairing", and a failed Repair is still told.
	h := newHarness(t)
	h.cfg.OpenConnect = "/nonexistent/openconnect"
	process := newFakeProcess()
	d := h.start(func(o *Options) { o.RepairStart = func() (Process, error) { return process, nil } })
	d.forceSet(Error, messages.ProgramMissing)
	d.StartRepair()
	d.RequestConnect()
	waitFor(3*time.Second, func() bool { d.mu.Lock(); defer d.mu.Unlock(); return !d.loopRunning })
	if _, m := d.snapshot(); m != messages.ProgramMissing || statusValue(t, d, "message_id") != "repairing" ||
		statusValue(t, d, "action") != nil {
		t.Fatal(m, statusValue(t, d, "message_id"))
	}
	process.finish(1)
	if !waitFor(3*time.Second, func() bool { _, m := d.snapshot(); return m == messages.RepairFailed }) {
		t.Fatal(d.snapshot())
	}
	if statusValue(t, d, "action") != "repair" {
		t.Fatal(statusValue(t, d, "action"))
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

// Settings, Username of a "Not listed" account: the assistant sends back what status.json says
// about the gateway, so the account keeps its second factor and groups.
func TestStatusCarriesTheGatewayOfANotListedAccount(t *testing.T) {
	h := newHarness(t)
	h.cfg.User = ""
	path := filepath.Join(t.TempDir(), "config.toml")
	d := h.start(func(o *Options) { o.ConfigError, o.ConfigPath, o.NeedsSetup = "missing", path, true })
	gateway := []config.Setting{{Key: "host", Value: "vpn.example.edu"}, {Key: "mfa", Value: "totp_field"},
		{Key: "usergroup", Value: "staff"}, {Key: "authgroup", Value: "Staff-2FA"}}
	if err := d.CompleteSetup("ab123", "pw", "base32:GEZDGNBVGY3TQOJQ", "other", gateway); err != nil {
		t.Fatal(err)
	}
	if name := statusValue(t, d, "university_name"); name != "vpn.example.edu" {
		t.Fatal("the gateway, not \"Other university\" in English:", name)
	}
	var again []config.Setting
	for _, s := range gateway {
		again = append(again, config.Setting{Key: s.Key, Value: statusValue(t, d, s.Key)})
	}
	if err := d.CompleteSetup("cd456", "pw", "base32:GEZDGNBVGY3TQOJQ", "other", again); err != nil {
		t.Fatal(err)
	}
	if got, err := config.Overrides(path); err != nil || !slices.Equal(got, gateway) {
		t.Fatal(got, err)
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

// The keys and their order are those of daemon.py's status(), then the Go core's own (sync, enroll,
// usergroup, authgroup); values Python would write the same.
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
		"busy", "error_kind", "platform", "elevated", "sync", "enroll", "usergroup", "authgroup"}
	if !slices.Equal(keys, want) {
		t.Fatal(keys)
	}
	text := string(pyjson.Marshal(s))
	for _, part := range []string{`"commit": null`, `"update_pending": null`, `"message_id": "not_connected"`,
		`"action": null`, `"idle_minutes": 0.05`, `"last_error": null`, `"error_kind": null`, `"elevated": null`} {
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

// A keyring that does not save shows the keyring message in the user's language, not the
// keyring tool's English.
func TestSavingIntoABrokenKeyringIsTranslated(t *testing.T) {
	h := newHarness(t)
	broken := func(string) error { return &credentials.KeyringError{Msg: "Uni VPN could not be saved: 5"} }
	d := h.start(func(o *Options) { o.PasswordSetter, o.TOTPSetter = broken, broken })
	for name, err := range map[string]error{"password": d.SetPassword("pw"), "totp": d.SetTOTP("JBSWY3DPEHPK3PXP")} {
		if key, _ := i18n.Of(err).(pyjson.Object); key == nil || fmt.Sprint(key) != fmt.Sprint(i18n.T("msg.keyring_locked").JSON()) {
			t.Errorf("%s: %v %v", name, err, i18n.Of(err))
		}
	}
	if !strings.Contains(h.logs.String(), "could not be saved: 5") {
		t.Error("keyring's own words not logged")
	}
}

// phoneClock is the phone's clock (monoClock) in the harness: it moves on with the wall clock
// while the phone sleeps.
func phoneClock(h *harness) func(*Daemon) {
	return func(d *Daemon) {
		awake := d.mono
		d.mono = func() float64 { return awake() + float64(h.wallOffset.Load()) }
	}
}

// A phone sleeps whenever its screen is off: that is no resume, and the tunnel is still there for
// the next page, without a new sign-in (a Duo push for some universities).
func TestPhoneScreenOffKeepsTheTunnel(t *testing.T) {
	h := newHarness(t)
	h.cfg.IdleMinutes = 5
	d := h.start(nil, phoneClock(h))
	c := h.client()
	echo(t, c, "x")
	h.wallOffset.Store(120) // two minutes with the screen off
	time.Sleep(400 * time.Millisecond)
	echo(t, c, "y")
	if s, _ := d.snapshot(); s != Connected || len(h.pwLines()) != 1 || strings.Contains(h.logs.String(), "Resume detected") {
		t.Fatal(s, h.pwLines(), h.logs.String())
	}
}

// ... but the time asleep counts towards the idle limit.
func TestPhoneScreenOffCountsTowardsIdle(t *testing.T) {
	h := newHarness(t)
	h.cfg.IdleMinutes = 1
	d := h.start(nil, phoneClock(h))
	c := h.client()
	echo(t, c, "x")
	c.Close()
	h.wallOffset.Store(120)
	waitState(t, d, Idle, 3*time.Second)
	time.Sleep(300 * time.Millisecond)
	if s, _ := d.snapshot(); s != Idle || len(h.pwLines()) != 1 || !strings.Contains(h.logs.String(), "Idle for") {
		t.Fatal(s, h.pwLines(), h.logs.String())
	}
}
