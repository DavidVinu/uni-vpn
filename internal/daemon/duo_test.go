//go:build !windows

package daemon

import (
	"sync"
	"testing"
	"time"

	"github.com/DavidVinu/uni-vpn/internal/config"
	"github.com/DavidVinu/uni-vpn/internal/tunnel"
)

// pushTunnel stands for openconnect waiting for a Duo push nobody approves: it never becomes
// ready and records how long the daemon waits for it.
type pushTunnel struct {
	waits  chan time.Duration
	once   sync.Once
	exited chan struct{}
}

func (p *pushTunnel) Start([]byte, string) error { return nil }
func (p *pushTunnel) WaitReady(timeout time.Duration) bool {
	select {
	case p.waits <- timeout:
	default:
	}
	return false
}
func (p *pushTunnel) Stop(time.Duration)      { p.once.Do(func() { close(p.exited) }) }
func (p *pushTunnel) Exited() <-chan struct{} { return p.exited }
func (p *pushTunnel) HasExited() bool {
	select {
	case <-p.exited:
		return true
	default:
		return false
	}
}
func (p *pushTunnel) StoppedByUs() bool                      { return false }
func (p *pushTunnel) Classification() (tunnel.Verdict, bool) { return tunnel.Verdict{}, false }
func (p *pushTunnel) ReturnCode() (int, bool)                { return 0, false }
func (p *pushTunnel) OTPGeneratedAt() time.Time              { return time.Time{} }
func (p *pushTunnel) Port() int                              { return 0 }
func (p *pushTunnel) StartedAt() time.Time                   { return time.Time{} }
func (p *pushTunnel) ReadyAt() time.Time                     { return time.Time{} }

// A Duo push stays open for 60 s and the gateway answers "Login failed." when it runs out
// (DuoRejected: "confirm the Duo request on your phone in time"). With the default ready_timeout
// of 45 s the daemon gave up first, said the university did not answer in time and started
// over, which sent the next push to the phone.
func TestDuoGetsThePushWindow(t *testing.T) {
	h := newHarness(t)
	h.cfg.ReadyTimeout = config.Default().ReadyTimeout
	h.cfg.MFA = "duo_push"
	waits := make(chan time.Duration, 1)
	h.start(func(o *Options) {
		o.TunnelFactory = func() (Tunnel, error) {
			return &pushTunnel{waits: waits, exited: make(chan struct{})}, nil
		}
	})
	h.d.RequestConnect()
	select {
	case w := <-waits:
		if w < 60*time.Second {
			t.Errorf("waits %v for the sign-in; a Duo push is open for 60 s", w)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no sign-in")
	}
}
