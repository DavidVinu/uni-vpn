// Package forwarder is the SOCKS passthrough: it accepts browser connections and passes the
// bytes on to ocproxy. It mirrors uni_vpn/forwarder.py.
package forwarder

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// TargetFunc is called for each browser connection and returns the ocproxy port, or
// ok=false to reject the connection. ctx is cancelled by CloseAll while it waits; the
// connection is then dropped even if the function does not return.
type TargetFunc func(ctx context.Context) (port int, ok bool)

const (
	cancelWait = time.Second
	stopWait   = 5 * time.Second
)

// Forwarder accepts on Host:Port and forwards to 127.0.0.1:<target>.
// TargetFunc and the activity callback are called from many goroutines at once.
type Forwarder struct {
	Host string
	Port int

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
	f.active.Add(1)
	w := &waiter{done: make(chan struct{})}
	defer close(w.done)
	defer f.active.Add(-1)
	f.track(client)
	defer f.untrack(client)

	port, ok, cancelled := f.target(w)
	if cancelled {
		return
	}
	if !ok {
		f.rejected.Add(1)
		return
	}
	var d net.Dialer
	upstream, err := d.DialContext(f.ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		f.log.Warn(fmt.Sprintf("ocproxy port %d not reachable: %s", port, connectErrorString(err, "127.0.0.1", port)))
		f.rejected.Add(1)
		return
	}
	f.track(upstream)
	defer f.untrack(upstream)

	done := make(chan struct{}, 2)
	go func() { f.pipe(client, upstream, &f.bytesOut); done <- struct{}{} }()
	go func() { f.pipe(upstream, client, &f.bytesIn); done <- struct{}{} }()
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
func (f *Forwarder) pipe(src, dst net.Conn, counter *atomic.Int64) {
	buf := make([]byte, 65536)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			counter.Add(int64(n))
			f.onActivity()
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
