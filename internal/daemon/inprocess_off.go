//go:build !libopenconnect && !ios

package daemon

import "github.com/DavidVinu/uni-vpn/internal/tunnel"

// Desktop and Android builds run the openconnect program; InProcessOpenConnect is never set.
func (d *Daemon) inProcessTunnel(tunnel.Config) (Tunnel, error) {
	return nil, &notFoundError{"this build has no libopenconnect"}
}
