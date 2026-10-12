//go:build !windows

package daemon

import (
	"context"
	"net"
	"slices"
	"testing"
	"time"

	"github.com/DavidVinu/uni-vpn/internal/linotp"
	"github.com/DavidVinu/uni-vpn/internal/linotp/fakelinotp"
	"github.com/DavidVinu/uni-vpn/internal/pyjson"
)

// enrollHarness: a Heidelberg account and the fake MFA portal in place of the real one.
func enrollHarness(t *testing.T) (*harness, *fakelinotp.Portal, *Daemon) {
	h := newHarness(t)
	h.cfg.University, h.cfg.MFA, h.cfg.User = "heidelberg", "totp_field", "ab123"
	h.password = []byte("pw")
	p := fakelinotp.Start()
	t.Cleanup(p.Close)
	d := h.start(func(o *Options) {
		o.PortalClient = func(portal string, _ func(context.Context, string, string) (net.Conn, error)) (*linotp.Client, error) {
			if portal != "https://mfa.uni-heidelberg.de/" {
				t.Errorf("portal %q", portal)
			}
			c, err := linotp.New(p.URL, nil)
			if err == nil {
				c.SetHTTPClient(p.Client())
			}
			return c, err
		}
	})
	return h, p, d
}

func enrollStage(d *Daemon) (string, string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.enrollJob == nil {
		return "", ""
	}
	key := ""
	if d.enrollJob.err != nil {
		key = d.enrollJob.err.Key
	}
	return d.enrollJob.stage, key
}

func waitEnroll(t *testing.T, d *Daemon, stage string) string {
	t.Helper()
	if !waitFor(8*time.Second, func() bool { s, _ := enrollStage(d); return s == stage }) {
		s, key := enrollStage(d)
		t.Fatalf("enroll stage %q (%s), expected %q", s, key, stage)
	}
	_, key := enrollStage(d)
	return key
}

// Disconnected: the first code opens the VPN, the next one the portal; then uni-vpn has its
// own code generator and never needs the user's app again.
func TestEnrollWithTwoCodesFromTheApp(t *testing.T) {
	h, p, d := enrollHarness(t)
	if err := d.Enroll("123 456", false); err != nil {
		t.Fatal(err)
	}
	waitState(t, d, Connected)
	if got := h.pwLines(); !slices.Equal(got, []string{"pw", "123456"}) {
		t.Fatal(got)
	}
	if key := waitEnroll(t, d, EnrollCode); key != "enroll.next_code" {
		t.Fatal(key)
	}
	if err := d.Enroll("999999", false); err != nil {
		t.Fatal(err)
	}
	if key := waitEnroll(t, d, EnrollCode); key != "enroll.code_wrong" {
		t.Fatal(key)
	}
	d.Enroll("111111", false)
	waitEnroll(t, d, EnrollDone)
	h.mu.Lock()
	stored := slices.Clone(h.storedTOTP)
	h.mu.Unlock()
	p.Lock()
	defer p.Unlock()
	if len(p.Tokens) != 1 || len(stored) != 1 {
		t.Fatal(p.Tokens, stored)
	}
	for serial, secret := range p.Tokens {
		if stored[0] != secret || !p.Verified[serial] {
			t.Fatal(stored, secret, p.Verified)
		}
	}
	if p.Calls[len(p.Calls)-1] != "logout" {
		t.Fatal(p.Calls)
	}
	s, _ := d.Status()
	if v, _ := s.Get("enroll"); string(pyjson.Marshal(v)) != `{"stage": "done", "error": null, "error_t": null}` {
		t.Fatal(string(pyjson.Marshal(v)))
	}
}

// Connected already: the one code signs in to the portal.
func TestEnrollWhileConnectedNeedsOneCode(t *testing.T) {
	h, p, d := enrollHarness(t)
	d.RequestConnect()
	waitState(t, d, Connected)
	d.Enroll("111111", false)
	waitEnroll(t, d, EnrollDone)
	if got := h.pwLines(); !slices.Equal(got, []string{"pw"}) {
		t.Fatal("no new VPN sign-in", got)
	}
	p.Lock()
	defer p.Unlock()
	if len(p.Tokens) != 1 {
		t.Fatal(p.Tokens)
	}
}

// A portal that allows no new token: nothing stored, the guide is the way.
func TestEnrollRefusedLeavesNothingBehind(t *testing.T) {
	h, p, d := enrollHarness(t)
	p.Lock()
	p.RefuseEnroll, p.Code = "token limit reached", ""
	p.Unlock()
	d.RequestConnect()
	waitState(t, d, Connected)
	d.Enroll("111111", false)
	if key := waitEnroll(t, d, EnrollFailed); key != "enroll.refused" {
		t.Fatal(key)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.storedTOTP) != 0 {
		t.Fatal(h.storedTOTP)
	}
}

// A failed test of the new token removes it from the account again.
func TestEnrollRemovesATokenThatFailsItsTest(t *testing.T) {
	_, p, d := enrollHarness(t)
	p.Lock()
	p.Code = ""
	p.Unlock()
	d.RequestConnect()
	waitState(t, d, Connected)
	d.mu.Lock()
	d.opts.TOTPSetter = func(string) error { return context.DeadlineExceeded }
	d.mu.Unlock()
	d.Enroll("111111", false)
	if key := waitEnroll(t, d, EnrollFailed); key != "enroll.failed" {
		t.Fatal(key)
	}
	p.Lock()
	defer p.Unlock()
	if len(p.Tokens) != 0 || len(p.Deleted) != 1 {
		t.Fatal(p.Tokens, p.Deleted)
	}
}

func TestEnrollWithAWrongVPNCode(t *testing.T) {
	h, _, d := enrollHarness(t)
	t.Setenv("FAKE_MODE", "totp_rejected")
	d.Enroll("123456", false)
	if key := waitEnroll(t, d, EnrollFailed); key != "enroll.code_wrong" {
		t.Fatal(key)
	}
	_ = h
}

func TestEnrollOnlyWhereThePortalAllowsIt(t *testing.T) {
	h := newHarness(t)
	h.cfg.University = "other"
	d := h.start(nil)
	if err := d.Enroll("123456", false); err == nil {
		t.Fatal("no MFA portal for this account")
	}
	_, _, d = enrollHarness(t)
	if err := d.Enroll("12a456", false); err == nil {
		t.Fatal("not a code")
	}
	d.Enroll("123456", false)
	d.Enroll("", true)
	if s, _ := enrollStage(d); s != "" {
		t.Fatal(s)
	}
}

// The setup assistant with a code from the app instead of the portal's QR code: the account is
// set up, the VPN opens with the code, and the portal step follows.
func TestSetupWithACodeFromTheApp(t *testing.T) {
	h := newHarness(t)
	p := fakelinotp.Start()
	t.Cleanup(p.Close)
	p.Code = ""
	cfgPath := t.TempDir() + "/config.toml"
	h.cfg.User = ""
	d := h.start(func(o *Options) {
		o.ConfigError, o.ConfigPath, o.NeedsSetup = "missing", cfgPath, true
		o.PortalClient = func(string, func(context.Context, string, string) (net.Conn, error)) (*linotp.Client, error) {
			c, err := linotp.New(p.URL, nil)
			c.SetHTTPClient(p.Client())
			return c, err
		}
	})
	h.password = []byte("pw")
	p.User = "ab123"
	if r := h.post("/api/setup", `{"user": "ab123", "password": "pw", "code": "12345", "university": "heidelberg"}`); r.status != 400 {
		t.Fatal(r.status, string(r.body))
	}
	r := h.post("/api/setup", `{"user": "ab123", "password": "pw", "code": "123456", "university": "heidelberg"}`)
	if r.status != 200 || r.json(t)["code"] != nil {
		t.Fatal(r.status, string(r.body))
	}
	waitEnroll(t, d, EnrollDone)
	if got := h.pwLines(); !slices.Equal(got, []string{"pw", "123456"}) {
		t.Fatal(got)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.storedTOTP) != 1 {
		t.Fatal(h.storedTOTP)
	}
	// Over the API: the progress, and a cancel.
	if v := h.http("GET", "/status.json", nil, "").json(t)["enroll"].(map[string]any); v["stage"] != "done" {
		t.Fatal(v)
	}
	if r := h.post("/api/totp/auto", `{"cancel": true}`); r.status != 200 || h.http("GET", "/status.json", nil, "").json(t)["enroll"] != nil {
		t.Fatal(r.status)
	}
	if r := h.post("/api/totp/auto", `{"code": "abc"}`); r.status != 400 {
		t.Fatal(r.status)
	}
}
