// Package sockowner tells whether a connection to the local API comes from another account
// of this computer. With fast user switching both accounts' apps and browsers reach the
// same port, held by whichever uni-vpn started first. Like Tailscale's local API on Windows,
// the system's table of TCP connections names the owner of the client's end.
//
// Every lookup fails open: when the system cannot tell, the request goes through.
package sockowner

import (
	"errors"
	"net"
	"net/netip"
)

// errNoSession: on Windows, uni-vpn itself runs outside a sign-in session (session 0, like a
// service), so there is no session to compare with.
var errNoSession = errors.New("no sign-in session to compare")

// errServiceClient: on Windows, the client is a service (session 0), say an antivirus that
// passes web traffic through itself. Another signed-in account's programs run in its own
// session, so this tells nothing about the account.
var errServiceClient = errors.New("the client is a service, outside any sign-in session")

// row is one end of a TCP connection in the system's table.
type row struct {
	local, remote netip.AddrPort
	owner         uint32 // the account (uid); the process id on Windows
	known         bool   // false: no owner any more, its program closed this end
}

// ends are the client's and the server's address of c.
func ends(c net.Conn) (client, server netip.AddrPort, err error) {
	remote, ok := c.RemoteAddr().(*net.TCPAddr)
	local, ok2 := c.LocalAddr().(*net.TCPAddr)
	if !ok || !ok2 {
		return client, server, errors.New("not a TCP connection")
	}
	return unmap(remote.AddrPort()), unmap(local.AddrPort()), nil
}

// unmap turns ::ffff:127.0.0.1 (a dual-stack socket) into 127.0.0.1.
func unmap(a netip.AddrPort) netip.AddrPort { return netip.AddrPortFrom(a.Addr().Unmap(), a.Port()) }

// find returns the client's end of the connection between client and server. Our own end
// must be in rows too, owned by self: otherwise the table is not what this code expects.
func find(rows []row, client, server netip.AddrPort, self uint32) (row, error) {
	var ours, theirs *row
	for i := range rows {
		r := &rows[i]
		switch {
		case r.local == server && r.remote == client && r.known:
			ours = r
		case r.local == client && r.remote == server && (theirs == nil || !theirs.known):
			theirs = r
		}
	}
	if ours == nil || ours.owner != self {
		return row{}, errors.New("this connection is not ours in the system's table")
	}
	if theirs == nil {
		return row{}, errors.New("the client's end is not in the system's table")
	}
	return *theirs, nil
}

// otherOwner: an end without owner counts as another account's. Nobody can read the answer,
// and another account must not get a request through by closing before the lookup.
func otherOwner(r row, self uint32) bool { return !r.known || r.owner != self }

// otherSession compares the sign-in sessions (by process id) of the client, owner, and of
// uni-vpn, self: Windows names processes in its table, not accounts.
func otherSession(sessions map[uint32]uint32, self, owner uint32) (bool, error) {
	ours, ok := sessions[self]
	if !ok || ours == 0 {
		return false, errNoSession
	}
	theirs, ok := sessions[owner]
	if !ok {
		return false, errors.New("the client's program has ended")
	}
	if theirs == 0 {
		return false, errServiceClient
	}
	return theirs != ours, nil
}
