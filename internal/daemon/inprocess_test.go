//go:build libopenconnect

package daemon

import (
	"testing"

	"github.com/DavidVinu/uni-vpn/internal/libtunnel"
	"github.com/DavidVinu/uni-vpn/internal/platform"
)

func TestInProcessTunnelNeedsNoPrograms(t *testing.T) {
	old := platform.InProcessOpenConnect
	platform.InProcessOpenConnect = true
	t.Cleanup(func() { platform.InProcessOpenConnect = old })
	h := newHarness(t)
	h.cfg.OpenConnect, h.cfg.OCProxy = "/nonexistent/openconnect", "/nonexistent/ocproxy"
	d := h.start(nil)
	d.mu.Lock()
	tun, err := d.makeTunnel()
	d.mu.Unlock()
	if _, ok := tun.(*libtunnel.Tunnel); err != nil || !ok {
		t.Fatalf("%T %v", tun, err)
	}
}
