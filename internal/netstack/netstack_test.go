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

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
)

// pair returns both ends of a packet socketpair, like the one the in-process tunnel hands
// to openconnect.
func pair(t *testing.T) (*os.File, *os.File) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_DGRAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, fd := range fds {
		// As internal/libtunnel sizes them: Darwin's default queue holds two packets and
		// drops the rest with ENOBUFS.
		_ = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_SNDBUF, 256<<10)
		_ = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_RCVBUF, 256<<10)
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

// On Apple systems openconnect puts the address family in front of each packet on its tun
// fd (TUN_HAS_AF_PREFIX), in network byte order, and expects it on what it reads.
func TestAddressFamilyPrefixOut(t *testing.T) {
	a, b := pair(t)
	defer b.Close()
	s, err := start(a, netip.MustParseAddr("10.0.0.2"), nil, 1400, true)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if c, err := s.DialContext(ctx, "tcp", "10.0.0.1:80"); err == nil {
			c.Close()
		}
	}()
	b.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 2000)
	n, err := b.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if n < 24 || string(buf[:4]) != "\x00\x00\x00\x02" || buf[4]>>4 != 4 || int(buf[6])<<8|int(buf[7]) != n-4 {
		t.Fatalf("% x", buf[:min(n, 24)])
	}
}

func TestAddressFamilyPrefixBothWays(t *testing.T) {
	a, b := pair(t)
	client, err := start(a, netip.MustParseAddr("10.0.0.2"), nil, 1400, true)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := start(b, netip.MustParseAddr("10.0.0.1"), nil, 1400, true)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ln, err := server.listenTCP(8080)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		if c, err := ln.Accept(); err == nil {
			c.Write([]byte("hi"))
			c.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := client.DialContext(ctx, "tcp", "10.0.0.1:8080")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if got, err := io.ReadAll(c); err != nil || string(got) != "hi" {
		t.Fatalf("%q %v", got, err)
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

// A phone's packet tunnel has a few dozen megabytes in all, so each connection's buffers stay
// small (gVisor's own defaults are 1 MiB per connection, up to 4 MiB).
func TestSmallTCPBuffers(t *testing.T) {
	a, _ := pair(t)
	s := newStack(t, a, "10.0.0.2")
	var recv tcpip.TCPReceiveBufferSizeRangeOption
	var send tcpip.TCPSendBufferSizeRangeOption
	if err := s.stack.TransportProtocolOption(tcp.ProtocolNumber, &recv); err != nil {
		t.Fatal(err)
	}
	if err := s.stack.TransportProtocolOption(tcp.ProtocolNumber, &send); err != nil {
		t.Fatal(err)
	}
	for _, r := range [][3]int{{recv.Min, recv.Default, recv.Max}, {send.Min, send.Default, send.Max}} {
		if r[1] != 64<<10 || r[2] != 256<<10 || r[0] > r[1] {
			t.Fatal(recv, send)
		}
	}
}

// Small buffers still carry a download much larger than themselves.
func TestBulkTransfer(t *testing.T) {
	a, b := pair(t)
	client := newStack(t, a, "10.0.0.2")
	server := newStack(t, b, "10.0.0.1")
	ln, err := server.listenTCP(8080)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	// The race detector slows gVisor on macOS to a few hundred KB/s (8 MiB take 50 ms without).
	size := 8 << 20
	if raceEnabled {
		size = 512 << 10
	}
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		c.Write(make([]byte, size))
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := client.DialContext(ctx, "tcp", "10.0.0.1:8080")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(20 * time.Second))
	start := time.Now()
	n, err := io.Copy(io.Discard, c)
	t.Logf("%d bytes in %v", n, time.Since(start))
	for name, st := range map[string]*Stack{"client": client, "server": server} {
		tcpStats := st.stack.Stats().TCP
		t.Logf("%s: retransmits %d, timeouts %d", name, tcpStats.Retransmits.Value(),
			tcpStats.Timeouts.Value())
	}
	if n != int64(size) || err != nil {
		t.Fatal(n, err)
	}
}
