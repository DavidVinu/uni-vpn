// Package netstack is a userspace TCP/IP stack (gVisor, set up as wireguard-go's netstack
// does) on a packet file descriptor. The in-process tunnel gives openconnect one end of a
// socketpair as its tun device and this stack the other, so the SOCKS server can reach the
// VPN without a tun device, routes or root, as ocproxy does for the openconnect program.
package netstack

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/icmp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
)

const (
	// maxPacket is the largest IP packet either side can hand over.
	maxPacket = 65535
	nic       = 1
	// An iOS packet tunnel may use a few dozen megabytes in all. gVisor's defaults (1 MiB of
	// buffer per TCP connection, up to 4 MiB, and 1024 queued packets) would use that up with
	// a handful of connections; these are about what a phone's own sockets start with.
	queuedPackets = 256
	bufferMin     = 4 << 10
	bufferDefault = 64 << 10
	bufferMax     = 256 << 10
	// fullWait: how many milliseconds a packet waits for room in a full queue before it is
	// dropped (TCP then sends it again).
	fullWait = 100
)

// Stack is one userspace network interface with a single address.
type Stack struct {
	file  *os.File
	ep    *channel.Endpoint
	stack *stack.Stack
	// prefix: 4 when each packet on the fd starts with its address family (Apple systems).
	prefix int
	// dropped counts packets the fd did not take.
	dropped atomic.Uint64
	// waits counts the pauses for a full queue.
	waits atomic.Uint64

	cancel context.CancelFunc
	once   sync.Once
	wg     sync.WaitGroup
}

// New starts a stack with addr on f, which carries raw IP packets, one per read or write (a
// SOCK_DGRAM socketpair, as openconnect's setup_tun_fd expects). f should be non-blocking
// (os.NewFile after syscall.SetNonblock) so Close can interrupt a pending read. The stack
// owns f from now on. dns is unused: the SOCKS server resolves names itself, through
// DialContext.
func New(f *os.File, addr netip.Addr, dns []netip.Addr, mtu int) (*Stack, error) {
	return start(f, addr, dns, mtu, afPrefix)
}

// start is New with or without the address family in front of each packet.
func start(f *os.File, addr netip.Addr, _ []netip.Addr, mtu int, prefix bool) (*Stack, error) {
	s, err := build(addr, mtu)
	if err != nil {
		f.Close()
		return nil, err
	}
	s.file = f
	if prefix {
		s.prefix = 4
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.wg.Add(2)
	go s.inbound()
	go s.outbound(ctx)
	return s, nil
}

// build is wireguard-go's CreateNetTUN for one address, with small TCP buffers.
func build(addr netip.Addr, mtu int) (*Stack, error) {
	s := &Stack{
		ep: channel.New(queuedPackets, uint32(mtu), ""),
		stack: stack.New(stack.Options{
			NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, ipv6.NewProtocol},
			TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol, icmp.NewProtocol6, icmp.NewProtocol4},
			HandleLocal:        true,
		}),
	}
	fail := func(what string, err tcpip.Error) (*Stack, error) {
		s.stack.Close()
		return nil, fmt.Errorf("netstack: %s: %v", what, err)
	}
	sack := tcpip.TCPSACKEnabled(true) // off by default
	if err := s.stack.SetTransportProtocolOption(tcp.ProtocolNumber, &sack); err != nil {
		return fail("SACK", err)
	}
	recv := tcpip.TCPReceiveBufferSizeRangeOption{Min: bufferMin, Default: bufferDefault, Max: bufferMax}
	if err := s.stack.SetTransportProtocolOption(tcp.ProtocolNumber, &recv); err != nil {
		return fail("receive buffer", err)
	}
	send := tcpip.TCPSendBufferSizeRangeOption{Min: bufferMin, Default: bufferDefault, Max: bufferMax}
	if err := s.stack.SetTransportProtocolOption(tcp.ProtocolNumber, &send); err != nil {
		return fail("send buffer", err)
	}
	if err := s.stack.CreateNIC(nic, s.ep); err != nil {
		return fail("NIC", err)
	}
	proto, route := tcpip.NetworkProtocolNumber(ipv4.ProtocolNumber), header.IPv4EmptySubnet
	if addr.Is6() {
		proto, route = ipv6.ProtocolNumber, header.IPv6EmptySubnet
	}
	pa := tcpip.ProtocolAddress{Protocol: proto, AddressWithPrefix: tcpip.AddrFromSlice(addr.AsSlice()).WithPrefix()}
	if err := s.stack.AddProtocolAddress(nic, pa, stack.AddressProperties{}); err != nil {
		return fail("address", err)
	}
	s.stack.AddRoute(tcpip.Route{Destination: route, NIC: nic})
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
		if n <= s.prefix {
			continue
		}
		pkt := buf[s.prefix:n]
		var proto tcpip.NetworkProtocolNumber
		switch pkt[0] >> 4 {
		case 4:
			proto = header.IPv4ProtocolNumber
		case 6:
			proto = header.IPv6ProtocolNumber
		default:
			continue
		}
		pb := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(pkt)})
		s.ep.InjectInbound(proto, pb)
		pb.DecRef()
	}
}

// outbound hands packets from the stack to the fd until Close. It keeps draining after a
// write error, since the stack drops what does not fit in the queue.
func (s *Stack) outbound(ctx context.Context) {
	defer s.wg.Done()
	buf := make([]byte, maxPacket+4)
	for {
		pb := s.ep.ReadContext(ctx)
		if pb == nil {
			return
		}
		view := pb.ToView()
		pb.DecRef()
		n, _ := view.Read(buf[s.prefix:])
		view.Release()
		if n == 0 {
			continue
		}
		if s.prefix > 0 {
			family := uint32(afInet)
			if buf[s.prefix]>>4 == 6 {
				family = afInet6
			}
			binary.BigEndian.PutUint32(buf, family)
		}
		s.write(ctx, buf[:s.prefix+n])
	}
}

// write sends one packet, waiting a little while openconnect's side of the queue is full.
func (s *Stack) write(ctx context.Context, pkt []byte) {
	for wait := 0; wait < fullWait; wait++ {
		full, err := s.sendPacket(pkt)
		if !full || ctx.Err() != nil {
			if err != nil {
				s.dropped.Add(1)
			}
			return
		}
		s.waits.Add(1)
		time.Sleep(time.Millisecond)
	}
	s.dropped.Add(1)
}

func fullAddr(ap netip.AddrPort) (tcpip.FullAddress, tcpip.NetworkProtocolNumber) {
	proto := tcpip.NetworkProtocolNumber(ipv4.ProtocolNumber)
	if ap.Addr().Is6() {
		proto = ipv6.ProtocolNumber
	}
	var a tcpip.Address
	if ap.Addr().IsValid() {
		a = tcpip.AddrFromSlice(ap.Addr().AsSlice())
	}
	return tcpip.FullAddress{NIC: nic, Addr: a, Port: ap.Port()}, proto
}

// DialContext connects through the stack, like net.Dialer.DialContext ("tcp" or "udp" and
// an IP address with port; names are not resolved here).
func (s *Stack) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, &net.OpError{Op: "dial", Net: network, Err: err}
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return nil, &net.OpError{Op: "dial", Net: network, Err: err}
	}
	p, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return nil, &net.OpError{Op: "dial", Net: network, Err: err}
	}
	fa, proto := fullAddr(netip.AddrPortFrom(ip.Unmap(), uint16(p)))
	switch network {
	case "tcp", "tcp4", "tcp6":
		return gonet.DialContextTCP(ctx, s.stack, fa, proto)
	case "udp", "udp4", "udp6":
		return gonet.DialUDP(s.stack, nil, &fa, proto)
	}
	return nil, &net.OpError{Op: "dial", Net: network, Err: net.UnknownNetworkError(network)}
}

// listenTCP is for tests, which play the far side of the tunnel with a second stack.
func (s *Stack) listenTCP(port int) (net.Listener, error) {
	fa, proto := fullAddr(netip.AddrPortFrom(netip.Addr{}, uint16(port)))
	return gonet.ListenTCP(s.stack, fa, proto)
}

func (s *Stack) listenUDP(port int) (net.PacketConn, error) {
	fa, proto := fullAddr(netip.AddrPortFrom(netip.Addr{}, uint16(port)))
	return gonet.DialUDP(s.stack, &fa, nil, proto)
}

// Close stops the stack, closes its connections and the fd, and waits for the copying.
func (s *Stack) Close() error {
	s.once.Do(func() {
		s.file.Close()
		s.cancel()
		s.stack.RemoveNIC(nic)
		s.stack.Close()
		s.ep.Close()
		s.wg.Wait()
	})
	return nil
}
