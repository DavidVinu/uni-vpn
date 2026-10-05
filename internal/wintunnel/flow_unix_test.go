//go:build !windows

package wintunnel

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/DavidVinu/uni-vpn/internal/tunnel"
	"github.com/DavidVinu/uni-vpn/internal/tunnel/fakeoc"
)

// fakeSocks stands in for the SOCKS server: it listens on the port and echoes.
type fakeSocks struct {
	l      net.Listener
	mu     sync.Mutex
	source string
	dns    []string
}

func (s *fakeSocks) Update(source string, dns []string) {
	s.mu.Lock()
	s.source, s.dns = source, dns
	s.mu.Unlock()
}

func (s *fakeSocks) Stop() { s.l.Close() }

func startFakeSocks(port int, source string, dns []string) (SocksServer, error) {
	l, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return nil, err
	}
	s := &fakeSocks{l: l, source: source, dns: dns}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	return s, nil
}

func newFlow(t *testing.T) (*Tunnel, string) {
	tmp := t.TempDir()
	tokenDir := filepath.Join(tmp, "state")
	t.Setenv(fakeoc.EnvVar, "openconnect")
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	t.Setenv("FAKE_MODE", "ok")
	t.Setenv("FAKE_DELAY", "0.1")
	t.Setenv("FAKE_PASSWORD_FILE", filepath.Join(tmp, "pw"))
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	tn := New(tunnel.Config{LoginName: "u", Host: "vpn.example", MFA: "totp_field", StopGrace: 15 * time.Second},
		exe, "script.js", nil, tokenDir)
	tn.NewSocks = startFakeSocks
	tn.CtrlC = func(pid int) bool { return syscall.Kill(pid, syscall.SIGINT) == nil }
	t.Cleanup(func() { tn.Kill() })
	return tn, tokenDir
}

func up(tn *Tunnel) bool {
	_, _, ok := tn.Source()
	return ok
}

func TestSocksServerComesUpFromScriptStateAndGoesAwayOnStop(t *testing.T) {
	tn, tokenDir := newFlow(t)
	if err := tn.Start([]byte("pw"), "base32:GEZDGNBVGY3TQOJQ"); err != nil {
		t.Fatal(err)
	}
	if !tn.WaitReady(5 * time.Second) {
		t.Fatal("not ready")
	}
	if _, err := os.Stat(tn.StateFile()); err != nil {
		t.Fatal(err)
	}
	if src, dns, _ := tn.Source(); src != "127.0.0.1" || !slices.Equal(dns, []string{"127.0.0.1"}) {
		t.Fatal(src, dns)
	}
	c, err := net.Dial("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(tn.Port())))
	if err != nil {
		t.Fatal(err)
	}
	c.Write([]byte("ping"))
	buf := make([]byte, 4)
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "ping" {
		t.Fatal(err, buf)
	}
	c.Close()
	tn.Stop(3 * time.Second)
	if !tn.HasExited() || up(tn) {
		t.Fatal("still up")
	}
	if _, err := os.Stat(tn.StateFile()); err == nil {
		t.Fatal("state file left")
	}
	if m, _ := filepath.Glob(filepath.Join(tokenDir, "totp-*")); len(m) != 0 {
		t.Fatal(m)
	}
	if code, _ := tn.ReturnCode(); code != 0 {
		t.Fatal("no clean logout", code)
	}
}

func TestScriptErrorEndsTheAttemptWithItsMessage(t *testing.T) {
	tn, _ := newFlow(t)
	t.Setenv("FAKE_SCRIPT_ERROR", "netsh add route failed")
	tn.Cfg.StopGrace = 2 * time.Second
	if err := tn.Start([]byte("pw"), ""); err != nil {
		t.Fatal(err)
	}
	if tn.WaitReady(5 * time.Second) {
		t.Fatal("ready")
	}
	if !tn.HasExited() || tn.StoppedByUs() || up(tn) {
		t.Fatal("state")
	}
	if v, _ := tn.Classification(); v != (tunnel.Verdict{State: "error", Message: "Tunnel setup failed: netsh add route failed"}) {
		t.Fatal(v)
	}
}

func TestStopBeforeTheProcessExistsEndsTheAttempt(t *testing.T) {
	tn, _ := newFlow(t)
	tn.Stop(2 * time.Second)
	if !tn.StoppedByUs() {
		t.Fatal("not marked")
	}
	if err := tn.Start([]byte("pw"), ""); err != nil {
		t.Fatal(err)
	}
	if tn.WaitReady(3 * time.Second) {
		t.Fatal("ready")
	}
	if !tn.HasExited() {
		t.Fatal("running")
	}
	tn.Stop(2 * time.Second)
}

func TestNewAddressAfterAReconnectIsFollowed(t *testing.T) {
	tn, _ := newFlow(t)
	if err := tn.Start([]byte("pw"), ""); err != nil {
		t.Fatal(err)
	}
	if !tn.WaitReady(5 * time.Second) {
		t.Fatal("not ready")
	}
	if err := os.WriteFile(tn.StateFile(), []byte("INTERNAL_IP4_ADDRESS=127.0.0.2\nINTERNAL_IP4_DNS=127.0.0.3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 30 {
		if src, _, _ := tn.Source(); src == "127.0.0.2" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	src, dns, _ := tn.Source()
	if src != "127.0.0.2" || !slices.Equal(dns, []string{"127.0.0.3"}) {
		t.Fatal(src, dns)
	}
	tn.mu.Lock()
	server := tn.socks.(*fakeSocks)
	tn.mu.Unlock()
	server.mu.Lock()
	got := server.source
	server.mu.Unlock()
	if got != "127.0.0.2" {
		t.Fatal("server not updated", got)
	}
	tn.Stop(2 * time.Second)
}

func TestScriptErrorAfterAReconnectKeepsTheTunnel(t *testing.T) {
	tn, _ := newFlow(t)
	if err := tn.Start([]byte("pw"), ""); err != nil {
		t.Fatal(err)
	}
	if !tn.WaitReady(5 * time.Second) {
		t.Fatal("not ready")
	}
	if err := os.WriteFile(tn.StateFile(), []byte("ERROR=netsh set address failed\nINTERNAL_IP4_ADDRESS=127.0.0.1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	if tn.HasExited() || !up(tn) {
		t.Fatal("tunnel dropped")
	}
	tn.Stop(2 * time.Second)
}

func TestServerStopsWhenOpenconnectDies(t *testing.T) {
	tn, _ := newFlow(t)
	if err := tn.Start([]byte("pw"), ""); err != nil {
		t.Fatal(err)
	}
	if !tn.WaitReady(5 * time.Second) {
		t.Fatal("not ready")
	}
	tn.Kill()
	select {
	case <-tn.Exited():
	case <-time.After(3 * time.Second):
		t.Fatal("did not exit")
	}
	for range 20 {
		if !up(tn) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if up(tn) {
		t.Fatal("server still up")
	}
}

func TestSocksPortTakenEndsTheAttempt(t *testing.T) {
	tn, _ := newFlow(t)
	tn.Cfg.StopGrace = 2 * time.Second
	tn.NewSocks = func(port int, source string, dns []string) (SocksServer, error) {
		return nil, &net.OpError{Op: "listen", Err: syscall.EADDRINUSE}
	}
	if err := tn.Start([]byte("pw"), ""); err != nil {
		t.Fatal(err)
	}
	if tn.WaitReady(5 * time.Second) {
		t.Fatal("ready")
	}
	v, _ := tn.Classification()
	want := "Local proxy port " + strconv.Itoa(tn.Port()) + " not available: "
	if v.State != "error" || len(v.Message) <= len(want) || v.Message[:len(want)] != want {
		t.Fatal(v)
	}
}
