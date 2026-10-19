// Package forwarder is the SOCKS passthrough: it accepts browser connections and passes the
// bytes on to ocproxy. It mirrors uni_vpn/forwarder.py, apart from Direct.
package forwarder

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/DavidVinu/uni-vpn/internal/pac"
)

// TargetFunc is called for each browser connection and returns the ocproxy port, or
// ok=false to reject the connection. ctx is cancelled by CloseAll while it waits; the
// connection is then dropped even if the function does not return.
type TargetFunc func(ctx context.Context) (port int, ok bool)

const (
	cancelWait = time.Second
	stopWait   = 5 * time.Second
	// greetWait: browsers send their SOCKS greeting as soon as they are connected.
	greetWait = 10 * time.Second
	// socksVersion starts every SOCKS5 greeting, the only SOCKS that ocproxy and
	// internal/socks speak (the PAC file says SOCKS5).
	socksVersion = 5
	// directWait: connecting to a host that Direct names.
	directWait = 15 * time.Second
	// methodsWait: the rest of the greeting, which browsers send in one piece with the
	// version. Short: other refused connections close at once, as before.
	methodsWait = 50 * time.Millisecond
)

// Forwarder accepts on Host:Port and forwards to 127.0.0.1:<target>.
// TargetFunc and the activity callback are called from many goroutines at once.
type Forwarder struct {
	Host string
	Port int

	// Direct names the hosts a refused connection still reaches directly (nil: none). Set
	// it before Start.
	Direct func() []string

	getTarget      TargetFunc
	onActivity     func()
	halfcloseGrace time.Duration
	log            *slog.Logger

	active, bytesIn, bytesOut, rejected atomic.Int64

	mu      sync.Mutex
	ln      net.Listener
	ctx     context.Context
	cancel  context.CancelFunc
	conns   map[net.Conn]struct{}
	waiting map[*waiter]struct{}
	wg      sync.WaitGroup
}

type waiter struct {
	cancel context.CancelFunc
	done   chan struct{} // closed when the handler has finished
}

// New returns a forwarder; halfcloseGrace is how long the other direction may go on after
// one direction has ended.
func New(host string, port int, getTarget TargetFunc, onActivity func(), halfcloseGrace time.Duration,
	log *slog.Logger) *Forwarder {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Forwarder{Host: host, Port: port, getTarget: getTarget, onActivity: onActivity,
		halfcloseGrace: halfcloseGrace, log: log}
}

// Active is the number of open browser connections, including those waiting for a target.
func (f *Forwarder) Active() int { return int(f.active.Load()) }

// BytesIn counts bytes from ocproxy to the browsers.
func (f *Forwarder) BytesIn() int64 { return f.bytesIn.Load() }

// BytesOut counts bytes from the browsers to ocproxy.
func (f *Forwarder) BytesOut() int64 { return f.bytesOut.Load() }

// Rejected counts connections without a target or with an unreachable one.
func (f *Forwarder) Rejected() int64 { return f.rejected.Load() }

// Start listens on Host:Port. The forwarder stops when ctx is done or Stop is called.
func (f *Forwarder) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", net.JoinHostPort(f.Host, strconv.Itoa(f.Port)))
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.ln = ln
	f.ctx, f.cancel = context.WithCancel(context.Background())
	f.conns = map[net.Conn]struct{}{}
	f.waiting = map[*waiter]struct{}{}
	f.mu.Unlock()
	context.AfterFunc(ctx, f.Stop)
	f.wg.Add(1)
	go f.serve(ln)
	return nil
}

// Addr is the listening address, nil when not started.
func (f *Forwarder) Addr() net.Addr {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ln == nil {
		return nil
	}
	return f.ln.Addr()
}

// Unbind frees the port at once; Stop still ends the connections.
func (f *Forwarder) Unbind() {
	f.mu.Lock()
	ln := f.ln
	f.mu.Unlock()
	if ln != nil {
		ln.Close()
	}
}

// Stop closes the listener and all connections and waits up to 5 s for the handlers.
func (f *Forwarder) Stop() {
	f.mu.Lock()
	ln := f.ln
	f.ln = nil
	f.mu.Unlock()
	if ln == nil {
		return
	}
	ln.Close()
	f.CloseAll()
	done := make(chan struct{})
	go func() { f.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(stopWait):
		f.log.Warn("Forwarder: connections not closed in time")
	}
	f.cancel()
}

// CloseAll drops every connection; the listener keeps running.
func (f *Forwarder) CloseAll() {
	// Cancel handlers still waiting for a target first: otherwise they keep up the
	// demand (active > 0) and trigger a reconnect right after disconnecting.
	f.mu.Lock()
	waiting := make([]*waiter, 0, len(f.waiting))
	for w := range f.waiting {
		waiting = append(waiting, w)
	}
	f.mu.Unlock()
	for _, w := range waiting {
		w.cancel()
	}
	deadline := time.After(cancelWait)
wait:
	for _, w := range waiting {
		select {
		case <-w.done:
		case <-deadline:
			break wait
		}
	}
	f.mu.Lock()
	conns := make([]net.Conn, 0, len(f.conns))
	for c := range f.conns {
		conns = append(conns, c)
	}
	f.mu.Unlock()
	for _, c := range conns {
		c.Close()
	}
}

func (f *Forwarder) track(c net.Conn) {
	f.mu.Lock()
	f.conns[c] = struct{}{}
	f.mu.Unlock()
}

func (f *Forwarder) untrack(c net.Conn) {
	f.mu.Lock()
	delete(f.conns, c)
	f.mu.Unlock()
	c.Close()
}

func (f *Forwarder) serve(ln net.Listener) {
	defer f.wg.Done()
	for {
		c, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			time.Sleep(time.Second) // like asyncio when out of file descriptors
			continue
		}
		f.wg.Add(1)
		go f.handle(c)
	}
}

func (f *Forwarder) handle(client net.Conn) {
	defer f.wg.Done()
	f.track(client)
	defer f.untrack(client)
	// Only a SOCKS client starts the tunnel. Anything else on this port, like a web page's
	// <img src="http://127.0.0.1:1080/"> or a port scan, would sign in to the VPN: a Duo push
	// nobody asked for.
	if !greets(client) {
		return
	}
	f.active.Add(1)
	w := &waiter{done: make(chan struct{})}
	port, ok, cancelled := f.target(w)
	if !ok {
		f.active.Add(-1) // a direct connection is no demand for the VPN
		close(w.done)
		if !cancelled && !f.direct(client) {
			f.rejected.Add(1)
		}
		return
	}
	defer close(w.done)
	defer f.active.Add(-1)
	var d net.Dialer
	upstream, err := d.DialContext(f.ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		f.log.Warn(fmt.Sprintf("ocproxy port %d not reachable: %s", port, connectErrorString(err, "127.0.0.1", port)))
		f.rejected.Add(1)
		return
	}
	f.track(upstream)
	defer f.untrack(upstream)
	f.splice(client, upstream, &f.bytesOut, &f.bytesIn, []byte{socksVersion})
}

// direct serves a refused connection to a host that Direct names: the security page that
// opens only on campus, which the rule gives as "SOCKS5 ...; DIRECT". The core answers the
// SOCKS request itself and connects from this device, so the page also opens in browsers
// that ignore "; DIRECT" (WebKitGTK with libproxy 0.5), and no browser takes Uni VPN for
// broken. False: refused, the connection is closed.
func (f *Forwarder) direct(client net.Conn) bool {
	if f.Direct == nil {
		return false
	}
	hosts := f.Direct()
	if len(hosts) == 0 {
		return false
	}
	host, port, ok := connectRequest(client)
	if !ok {
		return false
	}
	if !pac.Matches(host, hosts) {
		client.Write(socksReply(2)) // not allowed
		return false
	}
	d := net.Dialer{Timeout: directWait}
	host = pac.NormalizeHost(host)
	upstream, err := d.DialContext(f.ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		f.log.Info(fmt.Sprintf("%s not reachable directly: %s", host, err))
		client.Write(socksReply(4)) // host unreachable
		return true
	}
	client.SetReadDeadline(time.Time{})
	f.track(upstream)
	defer f.untrack(upstream)
	if _, err := client.Write(socksReply(0)); err == nil {
		f.splice(client, upstream, nil, nil, nil)
	}
	return true
}

// connectRequest reads the rest of a SOCKS5 greeting, whose version greets read, accepts
// "no authentication" and reads a CONNECT to a host name.
func connectRequest(c net.Conn) (host string, port int, ok bool) {
	c.SetReadDeadline(time.Now().Add(methodsWait))
	var n [1]byte
	if _, err := io.ReadFull(c, n[:]); err != nil {
		return "", 0, false
	}
	methods := make([]byte, n[0])
	if _, err := io.ReadFull(c, methods); err != nil {
		return "", 0, false
	}
	if bytes.IndexByte(methods, 0) < 0 {
		c.Write([]byte{socksVersion, 0xFF})
		return "", 0, false
	}
	if _, err := c.Write([]byte{socksVersion, 0}); err != nil {
		return "", 0, false
	}
	c.SetReadDeadline(time.Now().Add(greetWait))
	var head [5]byte // version, command, reserved, address type, name length
	if _, err := io.ReadFull(c, head[:]); err != nil || head[0] != socksVersion {
		return "", 0, false
	}
	if head[1] != 1 || head[3] != 3 { // CONNECT to a name: the rule names hosts
		c.Write(socksReply(2))
		return "", 0, false
	}
	rest := make([]byte, int(head[4])+2)
	if _, err := io.ReadFull(c, rest); err != nil {
		return "", 0, false
	}
	return string(rest[:head[4]]), int(binary.BigEndian.Uint16(rest[head[4]:])), true
}

// socksReply is a SOCKS5 reply with code rep and no bound address.
func socksReply(rep byte) []byte { return []byte{socksVersion, rep, 0, 1, 0, 0, 0, 0, 0, 0} }

// splice copies both ways until both end, or halfcloseGrace after the first has. out and in
// count the bytes to and from upstream (nil: not counted, and no activity).
func (f *Forwarder) splice(client, upstream net.Conn, out, in *atomic.Int64, head []byte) {
	done := make(chan struct{}, 2)
	go func() { f.pipe(client, upstream, out, head); done <- struct{}{} }()
	go func() { f.pipe(upstream, client, in, nil); done <- struct{}{} }()
	<-done
	timer := time.NewTimer(f.halfcloseGrace)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		client.Close()
		upstream.Close()
		<-done
	}
}

// greets reads the first byte of a new connection: true when it is the SOCKS version.
func greets(c net.Conn) bool {
	c.SetReadDeadline(time.Now().Add(greetWait))
	var first [1]byte
	_, err := io.ReadFull(c, first[:])
	c.SetReadDeadline(time.Time{})
	return err == nil && first[0] == socksVersion
}

// target asks getTarget while the handler counts as waiting for CloseAll.
func (f *Forwarder) target(w *waiter) (port int, ok, cancelled bool) {
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	w.cancel = cancel
	f.mu.Lock()
	f.waiting[w] = struct{}{}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		delete(f.waiting, w)
		f.mu.Unlock()
	}()
	type result struct {
		port int
		ok   bool
	}
	res := make(chan result, 1)
	go func() {
		p, ok := f.getTarget(ctx)
		res <- result{p, ok}
	}()
	select {
	case r := <-res:
		return r.port, r.ok, false
	case <-ctx.Done():
		return 0, false, true
	}
}

// pipe copies until EOF or an error, counting and noting activity, then half-closes dst.
// head (the byte greets read) goes out together with the first bytes read, so the greeting
// stays in one piece.
func (f *Forwarder) pipe(src, dst net.Conn, counter *atomic.Int64, head []byte) {
	buf := make([]byte, 65536)
	held := copy(buf, head)
	for {
		n, err := src.Read(buf[held:])
		n, held = n+held, 0
		if n > 0 && counter != nil {
			counter.Add(int64(n))
			f.onActivity()
		}
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
