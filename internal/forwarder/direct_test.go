package forwarder

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// website is a server on 127.0.0.1 that greets and then echoes, like a campus page.
func website(t *testing.T) int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				c.Write([]byte("campus"))
				io.Copy(c, c)
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

// socksConnect sends a whole SOCKS5 greeting and a CONNECT to host:port, as browsers do, and
// returns the server's reply code.
func socksConnect(t *testing.T, e *env, host string, port int) (net.Conn, byte) {
	t.Helper()
	c := e.dialRaw(t)
	c.Write([]byte{socksVersion, 1, 0})
	if got := readWithin(t, c, 2, 2*time.Second); string(got) != string([]byte{socksVersion, 0}) {
		t.Fatalf("method reply %v", got)
	}
	req := append([]byte{socksVersion, 1, 0, 3, byte(len(host))}, host...)
	req = binary.BigEndian.AppendUint16(req, uint16(port))
	c.Write(req)
	reply := readWithin(t, c, 10, 20*time.Second)
	if len(reply) < 2 {
		t.Fatalf("reply %v", reply)
	}
	return c, reply[1]
}

// Uni VPN refuses (a code no longer accepted, Cisco connected): the security page that opens
// only on campus is reached directly by the core, also for browsers that ignore "; DIRECT".
func TestRefusedSecurityPageGoesDirectly(t *testing.T) {
	e, _ := setup(t)
	e.setTarget(func(context.Context) (int, bool) { return 0, false })
	e.setDirect("localhost")
	c, rep := socksConnect(t, e, "LocalHost.", website(t))
	if rep != 0 {
		t.Fatal(rep)
	}
	if got := readWithin(t, c, 6, 2*time.Second); string(got) != "campus" {
		t.Fatal(string(got))
	}
	c.Write([]byte("hello"))
	if got := readWithin(t, c, 5, 2*time.Second); string(got) != "hello" {
		t.Fatal(string(got))
	}
	// Not through the VPN: no demand for it, nothing counted.
	if e.fwd.Active() != 0 || e.fwd.BytesIn() != 0 || e.fwd.BytesOut() != 0 || e.activity.Load() != 0 || e.fwd.Rejected() != 0 {
		t.Fatal(e.fwd.Active(), e.fwd.BytesIn(), e.fwd.BytesOut(), e.activity.Load(), e.fwd.Rejected())
	}
	c.Close()
}

func TestRefusedOtherWebsiteIsTurnedDown(t *testing.T) {
	e, _ := setup(t)
	e.setTarget(func(context.Context) (int, bool) { return 0, false })
	e.setDirect("localhost")
	c, rep := socksConnect(t, e, "lsf.localhost.example", website(t))
	if rep != 2 { // not allowed
		t.Fatal(rep)
	}
	expectEOF(t, c, 2*time.Second)
	eventually(t, func() bool { return e.fwd.Rejected() == 1 })
}

func TestRefusedSecurityPageUnreachable(t *testing.T) {
	e, _ := setup(t)
	e.setTarget(func(context.Context) (int, bool) { return 0, false })
	e.setDirect("localhost")
	c, rep := socksConnect(t, e, "localhost", freePort(t))
	if rep != 4 { // host unreachable
		t.Fatal(rep)
	}
	expectEOF(t, c, 2*time.Second)
}

// A network whose DNS answers the security page's name with this computer's own address: the
// core never connects there for whoever uses the port (a program of another account would reach
// the app's own API), as internal/socks refuses too. "localhost" stands in for that answer.
func TestRefusedSecurityPageNeverReachesThisComputer(t *testing.T) {
	e, _ := setup(t)
	e.setTarget(func(context.Context) (int, bool) { return 0, false })
	e.setDirect("localhost")
	e.refuseThisComputer()
	c, rep := socksConnect(t, e, "localhost", website(t))
	if rep != 4 { // host unreachable
		t.Fatal(rep)
	}
	expectEOF(t, c, 2*time.Second)
}

func TestThisComputer(t *testing.T) {
	for address, want := range map[string]bool{
		"127.0.0.1": true, "127.1.2.3": true, "0.0.0.0": true, "0.1.2.3": true, "::1": true,
		"::": true, "::ffff:127.0.0.1": true, "129.206.100.1": false, "10.0.0.1": false,
		"2001:db8::1": false, "fe80::1%eth0": false, "": false, "localhost": false,
	} {
		if got := thisComputer(address); got != want {
			t.Error(address, got)
		}
	}
}

// With the tunnel up, the security page goes through it like every listed website.
func TestSecurityPageThroughTheTunnelWhenUp(t *testing.T) {
	e, _ := setup(t)
	e.setDirect("localhost")
	c := e.dial(t)
	c.Write([]byte("hello"))
	if got := readWithin(t, c, 5, 2*time.Second); string(got) != "hello" {
		t.Fatal(string(got))
	}
	if e.fwd.Active() != 1 {
		t.Fatal(e.fwd.Active())
	}
}

// An address instead of a name, or no "no authentication": refused as before.
func TestRefusedAddressRequestIsTurnedDown(t *testing.T) {
	e, _ := setup(t)
	e.setTarget(func(context.Context) (int, bool) { return 0, false })
	e.setDirect("localhost")
	c := e.dialRaw(t)
	c.Write([]byte{socksVersion, 1, 0})
	readWithin(t, c, 2, 2*time.Second)
	port := website(t)
	req := []byte{socksVersion, 1, 0, 1, 127, 0, 0, 1}
	c.Write(binary.BigEndian.AppendUint16(req, uint16(port)))
	if got := readWithin(t, c, 10, 2*time.Second); len(got) < 2 || got[1] != 2 {
		t.Fatal(got)
	}
	c = e.dialRaw(t)
	c.Write([]byte{socksVersion, 1, 2})
	if got := readWithin(t, c, 2, 2*time.Second); string(got) != string([]byte{socksVersion, 0xFF}) {
		t.Fatal(got)
	}
	expectEOF(t, c, 2*time.Second)
}

// Browsers send the whole greeting at once: a lone version byte is refused right away, as
// without a security page.
func TestRefusedLoneVersionByteClosesSoon(t *testing.T) {
	e, _ := setup(t)
	e.setTarget(func(context.Context) (int, bool) { return 0, false })
	e.setDirect("localhost")
	c := e.dial(t)
	started := time.Now()
	expectEOF(t, c, 2*time.Second)
	if time.Since(started) > time.Second {
		t.Fatal(time.Since(started))
	}
	eventually(t, func() bool { return e.fwd.Rejected() == 1 })
}

// A security page that does not answer (no network): stopping the service does not wait for
// the dial.
func TestStopEndsADirectDial(t *testing.T) {
	e, logs := setup(t)
	e.setTarget(func(context.Context) (int, bool) { return 0, false })
	e.setDirect("localhost")
	dialing := make(chan struct{})
	e.mu.Lock()
	e.dialer = func(ctx context.Context, _, _ string) (net.Conn, error) {
		close(dialing)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	e.mu.Unlock()
	c := e.dialRaw(t)
	c.Write([]byte{socksVersion, 1, 0})
	readWithin(t, c, 2, 2*time.Second)
	c.Write(append(append([]byte{socksVersion, 1, 0, 3, 9}, "localhost"...), 0, 80))
	select {
	case <-dialing:
	case <-time.After(2 * time.Second):
		t.Fatal("no dial")
	}
	started := time.Now()
	e.fwd.Stop()
	if time.Since(started) > 2*time.Second || strings.Contains(logs.String(), "not closed in time") {
		t.Fatal(time.Since(started), logs.String())
	}
}

// The VPN is up again: the websites the core opened directly meanwhile close, so the browser
// opens them again through the VPN (the campus version), and a connection through the tunnel
// stays.
func TestCloseDirectEndsOnlyTheDirectConnections(t *testing.T) {
	e, _ := setup(t)
	e.setTarget(func(context.Context) (int, bool) { return 0, false })
	e.setDirect("localhost")
	direct, rep := socksConnect(t, e, "localhost", website(t))
	if rep != 0 {
		t.Fatal(rep)
	}
	if got := readWithin(t, direct, 6, 2*time.Second); string(got) != "campus" {
		t.Fatal(string(got))
	}
	e.setTarget(func(context.Context) (int, bool) { return e.echoPort, true })
	tunnel := e.dial(t)
	tunnel.Write([]byte("a"))
	if got := readWithin(t, tunnel, 1, 2*time.Second); string(got) != "a" {
		t.Fatal(string(got))
	}
	e.fwd.CloseDirect()
	expectEOF(t, direct, 2*time.Second)
	tunnel.Write([]byte("b"))
	if got := readWithin(t, tunnel, 1, 2*time.Second); string(got) != "b" {
		t.Fatal(string(got))
	}
}

// One still connecting directly when the VPN comes up does not go on directly either.
func TestCloseDirectEndsADirectDial(t *testing.T) {
	e, _ := setup(t)
	e.setTarget(func(context.Context) (int, bool) { return 0, false })
	e.setDirect("localhost")
	port := website(t)
	dialing, release := make(chan struct{}), make(chan struct{})
	e.mu.Lock()
	e.dialer = func(ctx context.Context, network, address string) (net.Conn, error) {
		close(dialing)
		<-release
		return (&net.Dialer{}).DialContext(context.Background(), network, address)
	}
	e.mu.Unlock()
	c := e.dialRaw(t)
	c.Write([]byte{socksVersion, 1, 0})
	readWithin(t, c, 2, 2*time.Second)
	c.Write(binary.BigEndian.AppendUint16(append([]byte{socksVersion, 1, 0, 3, 9}, "localhost"...), uint16(port)))
	select {
	case <-dialing:
	case <-time.After(2 * time.Second):
		t.Fatal("no dial")
	}
	e.fwd.CloseDirect()
	close(release)
	expectEOF(t, c, 2*time.Second)
}

// A website open directly is in use (a download, a lecture video): an update waits for it.
func TestDirectOpenWhileAWebsiteIsOpenDirectly(t *testing.T) {
	e, _ := setup(t)
	e.setTarget(func(context.Context) (int, bool) { return 0, false })
	e.setDirect("localhost")
	if e.fwd.DirectOpen() {
		t.Fatal("open before any website")
	}
	direct, rep := socksConnect(t, e, "localhost", website(t))
	if rep != 0 {
		t.Fatal(rep)
	}
	if !e.fwd.DirectOpen() {
		t.Fatal("not open with a website open directly")
	}
	direct.Close()
	for deadline := time.Now().Add(5 * time.Second); e.fwd.DirectOpen(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("still open after the browser closed it")
		}
	}
}
