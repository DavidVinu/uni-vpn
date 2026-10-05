package forwarder

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type env struct {
	fwd      *Forwarder
	echoPort int
	activity atomic.Int64
	mu       sync.Mutex
	target   func(ctx context.Context) (int, bool)
}

func freePort(t *testing.T) int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func setup(t *testing.T) (*env, *syncBuffer) {
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()
	t.Cleanup(func() { echo.Close() })
	e := &env{echoPort: echo.Addr().(*net.TCPAddr).Port}
	e.setTarget(func(context.Context) (int, bool) { return e.echoPort, true })
	logs := &syncBuffer{}
	log := slog.New(slog.NewTextHandler(logs, nil))
	e.fwd = New("127.0.0.1", freePort(t), e.getTarget, func() { e.activity.Add(1) }, 500*time.Millisecond, log)
	if err := e.fwd.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.fwd.Stop)
	return e, logs
}

func (e *env) setTarget(fn func(ctx context.Context) (int, bool)) {
	e.mu.Lock()
	e.target = fn
	e.mu.Unlock()
}

func (e *env) getTarget(ctx context.Context) (int, bool) {
	e.mu.Lock()
	fn := e.target
	e.mu.Unlock()
	return fn(ctx)
}

func (e *env) dial(t *testing.T) net.Conn {
	c, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(e.fwd.Port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func readWithin(t *testing.T, c net.Conn, n int, d time.Duration) []byte {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(d))
	buf := make([]byte, n)
	k, err := io.ReadFull(c, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		t.Fatal(err)
	}
	return buf[:k]
}

// expectEOF is reader.read() returning b"" within d.
func expectEOF(t *testing.T, c net.Conn, d time.Duration) {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(d))
	n, err := c.Read(make([]byte, 10))
	if n != 0 || err == nil || isTimeout(err) {
		t.Fatalf("read %d, %v; want EOF", n, err)
	}
}

func isTimeout(err error) bool {
	ne, ok := err.(net.Error)
	return ok && ne.Timeout()
}

func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	for range 100 {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not met")
}

func TestPipesBothDirectionsAndCounts(t *testing.T) {
	e, _ := setup(t)
	c := e.dial(t)
	c.Write([]byte("hello"))
	if got := readWithin(t, c, 5, 2*time.Second); string(got) != "hello" {
		t.Fatal(got)
	}
	if e.fwd.Active() != 1 || e.fwd.BytesOut() != 5 || e.fwd.BytesIn() != 5 {
		t.Fatal(e.fwd.Active(), e.fwd.BytesOut(), e.fwd.BytesIn())
	}
	if e.activity.Load() < 2 {
		t.Fatal(e.activity.Load())
	}
	c.Close()
	time.Sleep(200 * time.Millisecond)
	if e.fwd.Active() != 0 {
		t.Fatal(e.fwd.Active())
	}
}

func TestNoTargetClosesImmediately(t *testing.T) {
	e, _ := setup(t)
	e.setTarget(func(context.Context) (int, bool) { return 0, false })
	c := e.dial(t)
	expectEOF(t, c, 2*time.Second)
	eventually(t, func() bool { return e.fwd.Rejected() == 1 })
}

func TestUnreachableTargetCloses(t *testing.T) {
	e, logs := setup(t)
	port := freePort(t)
	e.setTarget(func(context.Context) (int, bool) { return port, true })
	c := e.dial(t)
	// Windows retries a refused connection to localhost for about 2 s.
	expectEOF(t, c, 5*time.Second)
	eventually(t, func() bool { return e.fwd.Rejected() == 1 })
	want := "ocproxy port " + strconv.Itoa(port) + " not reachable: "
	if runtime.GOOS == "linux" {
		want += "[Errno 111] Connect call failed ('127.0.0.1', " + strconv.Itoa(port) + ")"
	}
	if !strings.Contains(logs.String(), want) {
		t.Fatal(logs.String())
	}
}

func TestCloseAll(t *testing.T) {
	e, _ := setup(t)
	c := e.dial(t)
	c.Write([]byte("x"))
	readWithin(t, c, 1, 2*time.Second)
	e.fwd.CloseAll()
	expectEOF(t, c, 2*time.Second)
	time.Sleep(200 * time.Millisecond)
	if e.fwd.Active() != 0 {
		t.Fatal(e.fwd.Active())
	}
}

func TestCloseAllCancelsWaitingHandlers(t *testing.T) {
	e, _ := setup(t)
	gate := make(chan struct{})
	defer close(gate)
	// Ignores ctx on purpose: the handler must still be dropped.
	e.setTarget(func(context.Context) (int, bool) { <-gate; return e.echoPort, true })
	c := e.dial(t)
	time.Sleep(100 * time.Millisecond)
	if e.fwd.Active() != 1 {
		t.Fatal(e.fwd.Active())
	}
	e.fwd.CloseAll()
	if e.fwd.Active() != 0 {
		t.Fatal(e.fwd.Active())
	}
	expectEOF(t, c, 2*time.Second)
	if e.fwd.Rejected() != 0 {
		t.Fatal(e.fwd.Rejected())
	}
}

func TestHalfcloseGrace(t *testing.T) {
	e, _ := setup(t)
	c := e.dial(t)
	c.Write([]byte("abc"))
	c.(*net.TCPConn).CloseWrite()
	if got := readWithin(t, c, 3, 2*time.Second); string(got) != "abc" {
		t.Fatal(got)
	}
	expectEOF(t, c, 2*time.Second)
}

func TestHalfcloseGraceEndsSilentUpstream(t *testing.T) {
	e, _ := setup(t)
	// An upstream that never answers nor closes: after the client's EOF the connection
	// lives on for the grace period only.
	silent, _ := net.Listen("tcp", "127.0.0.1:0")
	defer silent.Close()
	go func() {
		for {
			c, err := silent.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	port := silent.Addr().(*net.TCPAddr).Port
	e.setTarget(func(context.Context) (int, bool) { return port, true })
	c := e.dial(t)
	c.Write([]byte("abc"))
	time.Sleep(100 * time.Millisecond)
	start := time.Now()
	c.(*net.TCPConn).CloseWrite()
	expectEOF(t, c, 3*time.Second)
	if d := time.Since(start); d < 400*time.Millisecond {
		t.Fatal("closed before the grace period", d)
	}
	eventually(t, func() bool { return e.fwd.Active() == 0 })
}

func TestStopEndsWaitingAndCancelsContext(t *testing.T) {
	e, _ := setup(t)
	cancelled := make(chan struct{})
	e.setTarget(func(ctx context.Context) (int, bool) { <-ctx.Done(); close(cancelled); return 0, false })
	c := e.dial(t)
	eventually(t, func() bool { return e.fwd.Active() == 1 })
	e.fwd.Stop()
	<-cancelled
	expectEOF(t, c, 2*time.Second)
	if e.fwd.Addr() != nil || e.fwd.Active() != 0 {
		t.Fatal("still running")
	}
}
