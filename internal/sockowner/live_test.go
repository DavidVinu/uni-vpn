//go:build (linux && !android) || (darwin && !ios) || windows

package sockowner

import (
	"errors"
	"net"
	"testing"
)

// The system's own table, on each desktop system: this test's connection is this account's,
// seen from both ends.
func TestOwnConnection(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	for _, c := range []net.Conn{server, client} {
		r, err := clientEnd(c)
		if err != nil || !r.known || r.owner != self {
			t.Fatal(r, err)
		}
		other, err := OtherAccount(c)
		// A Windows test runner may run as a service, outside any sign-in session.
		if other || (err != nil && !errors.Is(err, errNoSession)) {
			t.Fatal(other, err)
		}
	}
}

// A dual-stack client: an IPv6 socket connected to ::ffff:127.0.0.1 (Go makes one for an IPv6
// local address).
func TestOwnDualStackConnection(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	client, err := net.DialTCP("tcp", &net.TCPAddr{IP: net.IPv6unspecified}, ln.Addr().(*net.TCPAddr))
	if err != nil {
		t.Skip("no dual-stack sockets here:", err)
	}
	defer client.Close()
	server, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if r, err := clientEnd(server); err != nil || !r.known || r.owner != self {
		t.Fatal(r, err)
	}
}
