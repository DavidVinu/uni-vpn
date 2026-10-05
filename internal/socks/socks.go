// Package socks is a minimal SOCKS5 server that dials out from a fixed source address.
// It mirrors uni_vpn/socks.py.
//
// Windows stand-in for ocproxy: openconnect cannot hand the tunnel to a script there, so it
// brings up a Wintun adapter instead and this server connects from the adapter's address.
// Windows sends such sockets through the adapter (strong host model) while everything else
// keeps using the normal routes. Like ocproxy it supports CONNECT to IPv4 and host names only,
// and resolves names with the VPN's DNS servers.
package socks

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	Version      = 5
	NoAuth       = 0
	NoAcceptable = 0xFF
	CmdConnect   = 1

	AtypIPv4   = 1
	AtypDomain = 3
	AtypIPv6   = 4

	RepOK              = 0
	RepFailure         = 1
	RepNetUnreachable  = 3
	RepHostUnreachable = 4
	RepRefused         = 5
	RepCmdUnsupported  = 7
	RepAtypUnsupported = 8
)

const (
	handshakeTimeout = 30 * time.Second
	dnsTimeout       = 3 * time.Second
	stopWait         = 2 * time.Second
)

// Server is the SOCKS5 server. Set the exported fields before Start.
type Server struct {
	Port           int
	Host           string        // default "127.0.0.1"
	DNSPort        int           // default 53
	ConnectTimeout time.Duration // default 20 s
	Log            *slog.Logger

	mu     sync.Mutex
	source string
	dns    []string
	ln     net.Listener
	ctx    context.Context
	cancel context.CancelFunc
	conns  map[net.Conn]struct{}
	wg     sync.WaitGroup
}

// NewServer returns a server on port that connects from source and asks the dns servers.
func NewServer(port int, source string, dns []string, log *slog.Logger) *Server {
	return &Server{
		Port: port, Host: "127.0.0.1", DNSPort: 53, ConnectTimeout: 20 * time.Second, Log: log,
		source: source, dns: append([]string{}, dns...),
	}
}

// Route returns the current source address and DNS servers.
func (s *Server) Route() (source string, dns []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.source, append([]string{}, s.dns...)
}

// SetRoute changes source address and DNS servers, e.g. after openconnect reconnected on
// its own and the script reported a new address. New connections use them.
func (s *Server) SetRoute(source string, dns []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.source, s.dns = source, append([]string{}, dns...)
}

// Start listens on Host:Port. The server stops when ctx is done or Stop is called.
func (s *Server) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", net.JoinHostPort(s.Host, strconv.Itoa(s.Port)))
	if err != nil {
		return err
	}
	if s.Log == nil {
		s.Log = slog.New(slog.DiscardHandler)
	}
	s.mu.Lock()
	s.ln = ln
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.conns = map[net.Conn]struct{}{}
	s.mu.Unlock()
	context.AfterFunc(ctx, s.Stop)
	s.wg.Add(1)
	go s.serve(ln)
	return nil
}

// Addr is the listening address, nil when not started.
func (s *Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return nil
	}
	return s.ln.Addr()
}

// Stop closes the listener and all connections and waits up to 2 s for the handlers.
func (s *Server) Stop() {
	s.mu.Lock()
	ln := s.ln
	if ln == nil {
		s.mu.Unlock()
		return
	}
	s.ln = nil
	s.cancel()
	conns := s.conns
	s.conns = nil
	s.mu.Unlock()
	ln.Close()
	for c := range conns {
		c.Close()
	}
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(stopWait):
	}
}

// track registers c for Stop; false (and c closed) when the server is stopping.
func (s *Server) track(c net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conns == nil {
		c.Close()
		return false
	}
	s.conns[c] = struct{}{}
	return true
}

func (s *Server) untrack(c net.Conn) {
	s.mu.Lock()
	if s.conns != nil {
		delete(s.conns, c)
	}
	s.mu.Unlock()
	c.Close()
}

func (s *Server) serve(ln net.Listener) {
	defer s.wg.Done()
	for {
		c, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			time.Sleep(time.Second) // like asyncio when out of file descriptors
			continue
		}
		if !s.track(c) {
			continue
		}
		s.wg.Add(1)
		go s.handle(c)
	}
}

func (s *Server) handle(client net.Conn) {
	defer s.wg.Done()
	defer s.untrack(client)
	upstream, err := s.negotiate(client)
	if err != nil {
		// Read/write errors and timeouts end the client quietly; one bad client must not
		// stop the server.
		var ve *valueError
		if errors.As(err, &ve) {
			s.Log.Warn(fmt.Sprintf("SOCKS: %s", err))
		}
		return
	}
	if upstream == nil {
		return
	}
	if !s.track(upstream) {
		return
	}
	defer s.untrack(upstream)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); pipe(client, upstream) }()
	go func() { defer wg.Done(); pipe(upstream, client) }()
	wg.Wait()
}

func readTimeout(c net.Conn, buf []byte) error {
	c.SetReadDeadline(time.Now().Add(handshakeTimeout))
	_, err := io.ReadFull(c, buf)
	c.SetReadDeadline(time.Time{})
	return err
}

// negotiate returns the upstream connection, or nil after a refusal reply.
func (s *Server) negotiate(c net.Conn) (net.Conn, error) {
	head := make([]byte, 2)
	if err := readTimeout(c, head); err != nil {
		return nil, err
	}
	methods := make([]byte, head[1])
	if _, err := io.ReadFull(c, methods); err != nil {
		return nil, err
	}
	if head[0] != Version {
		return nil, nil
	}
	noAuth := false
	for _, m := range methods {
		noAuth = noAuth || m == NoAuth
	}
	if !noAuth {
		_, err := c.Write([]byte{Version, NoAcceptable})
		return nil, err
	}
	if _, err := c.Write([]byte{Version, NoAuth}); err != nil {
		return nil, err
	}
	req := make([]byte, 4)
	if err := readTimeout(c, req); err != nil {
		return nil, err
	}
	cmd, atyp := req[1], req[3]
	var host string
	known := true
	switch atyp {
	case AtypIPv4:
		b := make([]byte, 4)
		if _, err := io.ReadFull(c, b); err != nil {
			return nil, err
		}
		host = netip.AddrFrom4([4]byte(b)).String()
	case AtypDomain:
		n := make([]byte, 1)
		if _, err := io.ReadFull(c, n); err != nil {
			return nil, err
		}
		b := make([]byte, n[0])
		if _, err := io.ReadFull(c, b); err != nil {
			return nil, err
		}
		host = decodeASCIIReplace(b)
	case AtypIPv6:
		if _, err := io.ReadFull(c, make([]byte, 16)); err != nil {
			return nil, err
		}
		known = false
	default:
		known = false
	}
	pb := make([]byte, 2)
	if _, err := io.ReadFull(c, pb); err != nil {
		return nil, err
	}
	port := int(pb[0])<<8 | int(pb[1])
	if cmd != CmdConnect {
		return nil, reply(c, RepCmdUnsupported, nil, 0)
	}
	if !known {
		return nil, reply(c, RepAtypUnsupported, nil, 0)
	}
	source, dns := s.Route()
	address, err := Resolve(s.ctx, host, dns, source, dnsTimeout, s.DNSPort)
	if err != nil {
		var ve *valueError
		if s.ctx.Err() != nil || errors.As(err, &ve) {
			return nil, err
		}
		s.Log.Info(fmt.Sprintf("SOCKS: %s", err))
		return nil, reply(c, RepHostUnreachable, nil, 0)
	}
	upstream, err := s.dial(source, address, port)
	if err != nil {
		if s.ctx.Err() != nil {
			return nil, err
		}
		if isRefused(err) {
			return nil, reply(c, RepRefused, nil, 0)
		}
		text := ""
		if !isTimeout(err) {
			text = connectErrorString(err, source, address, port)
		}
		// Python prints `exc or "timeout"`, and an exception is always true: a timeout shows
		// as an empty text.
		s.Log.Info(fmt.Sprintf("SOCKS: %s:%d not reachable: %s", host, port, text))
		return nil, reply(c, RepHostUnreachable, nil, 0)
	}
	bound, _ := upstream.LocalAddr().(*net.TCPAddr)
	var ip net.IP
	boundPort := 0
	if bound != nil {
		ip, boundPort = bound.IP, bound.Port
	}
	if err := reply(c, RepOK, ip, boundPort); err != nil {
		upstream.Close()
		return nil, err
	}
	return upstream, nil
}

func (s *Server) dial(source, address string, port int) (net.Conn, error) {
	laddr, err := net.ResolveTCPAddr("tcp", net.JoinHostPort(source, "0"))
	if err != nil {
		return nil, err
	}
	d := net.Dialer{LocalAddr: laddr, Timeout: s.ConnectTimeout}
	return d.DialContext(s.ctx, "tcp", net.JoinHostPort(address, strconv.Itoa(port)))
}

func reply(c net.Conn, code byte, ip net.IP, port int) error {
	msg := []byte{Version, code, 0, AtypIPv4, 0, 0, 0, 0, byte(port >> 8), byte(port)}
	if v4 := ip.To4(); v4 != nil {
		copy(msg[4:8], v4)
	}
	_, err := c.Write(msg)
	return err
}

// decodeASCIIReplace is bytes.decode("ascii", errors="replace").
func decodeASCIIReplace(b []byte) string {
	r := make([]rune, len(b))
	for i, c := range b {
		if c < 0x80 {
			r[i] = rune(c)
		} else {
			r[i] = utf8.RuneError
		}
	}
	return string(r)
}

// pipe copies until EOF or an error, then half-closes dst.
func pipe(src, dst net.Conn) {
	buf := make([]byte, 65536)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				break
			}
		}
		if err != nil {
			break
		}
	}
	if cw, ok := dst.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite()
	}
}
