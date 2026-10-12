//go:build libopenconnect || ios

package daemon

import (
	"github.com/DavidVinu/uni-vpn/internal/libtunnel"
	"github.com/DavidVinu/uni-vpn/internal/totp"
	"github.com/DavidVinu/uni-vpn/internal/tunnel"
)

// inProcessTunnel runs libopenconnect inside the core (iOS, or the Linux tests). Only these
// builds link libtunnel and its network stack.
func (d *Daemon) inProcessTunnel(tc tunnel.Config) (Tunnel, error) {
	t := libtunnel.New(tc, d.log, libtunnel.Default())
	t.TOTPCode = totp.CodeAt
	return t, nil
}
