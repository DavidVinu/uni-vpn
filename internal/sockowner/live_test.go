//go:build (linux && !android) || (darwin && !ios) || windows

package sockowner

import (
	"errors"
	"net"
	"testing"
	"time"
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

// Setup looks at the status port right after it started this account's core, which may not
// have taken the connection yet: until then its end has no owner (Linux), which is not
// another account's.
func TestAListenerOfThisAccountIsOursAlsoBeforeItAccepts(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	if OtherAccountListens(port) {
		t.Fatal("a listener that does not accept counts as another account's")
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	for range 20 {
		if OtherAccountListens(port) {
			t.Fatal("this account's listener counts as another account's")
		}
	}
}
