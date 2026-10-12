//go:build unix

package netstack

import (
	"context"
	"io"
	"net"
	"net/netip"
	"os"
	"syscall"
	"testing"
	"time"
)

// pair returns both ends of a packet socketpair, like the one the in-process tunnel hands
// to openconnect.
func pair(t *testing.T) (*os.File, *os.File) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_DGRAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, fd := range fds {
		if err := syscall.SetNonblock(fd, true); err != nil {
			t.Fatal(err)
		}
	}
	return os.NewFile(uintptr(fds[0]), "a"), os.NewFile(uintptr(fds[1]), "b")
}

func newStack(t *testing.T, f *os.File, addr string) *Stack {
	s, err := New(f, netip.MustParseAddr(addr), []netip.Addr{netip.MustParseAddr("10.0.0.1")}, 1400)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestTCPBothWays(t *testing.T) {
	a, b := pair(t)
	client := newStack(t, a, "10.0.0.2")
	server := newStack(t, b, "10.0.0.1")
	ln, err := server.listenTCP(8080)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		buf := make([]byte, 5)
		if _, err := io.ReadFull(c, buf); err != nil {
			return
		}
		c.Write(append([]byte("re: "), buf...))
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := client.DialContext(ctx, "tcp", "10.0.0.1:8080")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(c)
	if err != nil || string(got) != "re: hello" {
		t.Fatalf("%q %v", got, err)
	}
	if addr := c.LocalAddr().(*net.TCPAddr); addr.IP.String() != "10.0.0.2" {
		t.Fatal(addr)
	}
}

// The SOCKS server asks the VPN's DNS servers over UDP through the stack.
func TestUDP(t *testing.T) {
	a, b := pair(t)
	client := newStack(t, a, "10.0.0.2")
	server := newStack(t, b, "10.0.0.1")
	pc, err := server.listenUDP(53)
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	go func() {
		buf := make([]byte, 512)
		n, from, err := pc.ReadFrom(buf)
		if err == nil {
			pc.WriteTo(append([]byte("re: "), buf[:n]...), from)
		}
	}()
	c, err := client.DialContext(context.Background(), "udp", "10.0.0.1:53")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	c.Write([]byte("query"))
	buf := make([]byte, 512)
	n, err := c.Read(buf)
	if err != nil || string(buf[:n]) != "re: query" {
		t.Fatalf("%q %v", buf[:n], err)
	}
}

func TestDialNobodyFails(t *testing.T) {
	a, b := pair(t)
	client := newStack(t, a, "10.0.0.2")
	newStack(t, b, "10.0.0.1")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	if c, err := client.DialContext(ctx, "tcp", "10.0.0.99:80"); err == nil {
		c.Close()
		t.Fatal("connected")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatal(d)
	}
}

func TestCloseEndsWhenPeerIsGone(t *testing.T) {
	a, b := pair(t)
	s := newStack(t, a, "10.0.0.2")
	b.Close()
	done := make(chan struct{})
	go func() { s.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Close hangs")
	}
}
