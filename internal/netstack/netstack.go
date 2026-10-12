// Package netstack is a userspace TCP/IP stack (gVisor, through wireguard-go's netstack) on
// a packet file descriptor. The in-process tunnel gives openconnect one end of a socketpair
// as its tun device and this stack the other, so the SOCKS server can reach the VPN without
// a tun device, routes or root, as ocproxy does for the openconnect program.
package netstack

import (
	"context"
	"encoding/binary"
	"net"
	"net/netip"
	"os"
	"sync"

	"golang.zx2c4.com/wireguard/tun"
	wgnetstack "golang.zx2c4.com/wireguard/tun/netstack"
)

// maxPacket is the largest IP packet either side can hand over.
const maxPacket = 65535

// Stack is one userspace network interface with a single address.
type Stack struct {
	file *os.File
	dev  tun.Device
	net  *wgnetstack.Net
	// prefix: 4 when each packet on the fd starts with its address family (Apple systems).
	prefix int

	once sync.Once
	wg   sync.WaitGroup
}

// New starts a stack with addr on f, which carries raw IP packets, one per read or write (a
// SOCK_DGRAM socketpair, as openconnect's setup_tun_fd expects). f should be non-blocking
// (os.NewFile after syscall.SetNonblock) so Close can interrupt a pending read. The stack
// owns f from now on.
func New(f *os.File, addr netip.Addr, dns []netip.Addr, mtu int) (*Stack, error) {
	return start(f, addr, dns, mtu, afPrefix)
}

// start is New with or without the address family in front of each packet.
func start(f *os.File, addr netip.Addr, dns []netip.Addr, mtu int, prefix bool) (*Stack, error) {
	dev, n, err := wgnetstack.CreateNetTUN([]netip.Addr{addr}, dns, mtu)
	if err != nil {
		f.Close()
		return nil, err
	}
	s := &Stack{file: f, dev: dev, net: n}
	if prefix {
		s.prefix = 4
	}
	s.wg.Add(2)
	go s.inbound()
	go s.outbound()
	return s, nil
}

// inbound hands packets from the fd to the stack until the fd fails or closes.
func (s *Stack) inbound() {
	defer s.wg.Done()
	buf := make([]byte, maxPacket)
	for {
		n, err := s.file.Read(buf)
		if err != nil {
			return
		}
		if n > s.prefix {
			_, _ = s.dev.Write([][]byte{buf[:n]}, s.prefix)
		}
	}
}

// outbound hands packets from the stack to the fd. It keeps draining after a write error
// until the device closes, since the stack blocks while a packet waits to be read.
func (s *Stack) outbound() {
	defer s.wg.Done()
	bufs := [][]byte{make([]byte, maxPacket)}
	sizes := []int{0}
	for {
		n, err := s.dev.Read(bufs, sizes, s.prefix)
		if err != nil {
			return
		}
		if n > 0 && sizes[0] > 0 {
			pkt := bufs[0][:s.prefix+sizes[0]]
			if s.prefix > 0 {
				family := uint32(afInet)
				if pkt[s.prefix]>>4 == 6 {
					family = afInet6
				}
				binary.BigEndian.PutUint32(pkt, family)
			}
			_, _ = s.file.Write(pkt)
		}
	}
}

// DialContext connects through the stack, like net.Dialer.DialContext ("tcp" or "udp" and
// an IP address with port).
func (s *Stack) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return s.net.DialContext(ctx, network, address)
}

// listenTCP is for tests, which play the far side of the tunnel with a second stack.
func (s *Stack) listenTCP(port int) (net.Listener, error) {
	return s.net.ListenTCP(&net.TCPAddr{Port: port})
}

func (s *Stack) listenUDP(port int) (net.PacketConn, error) {
	return s.net.ListenUDP(&net.UDPAddr{Port: port})
}

// Close stops the stack, closes its connections and the fd, and waits for the copying.
func (s *Stack) Close() error {
	s.once.Do(func() {
		s.file.Close()
		s.dev.Close()
		s.wg.Wait()
	})
	return nil
}
