package coremain

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DavidVinu/uni-vpn/internal/daemon"
)

func freePort(t *testing.T) int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// home is a fresh config and state folder with a config.toml on free ports.
func home(t *testing.T) (httpPort int) {
	httpPort, _ = homePorts(t)
	return httpPort
}

// homePorts is home, with the SOCKS port too.
func homePorts(t *testing.T) (httpPort, socksPort int) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	httpPort, socksPort = freePort(t), freePort(t)
	for socksPort == httpPort {
		socksPort = freePort(t)
	}
	cfg := filepath.Join(dir, "config", "uni-vpn")
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	toml := fmt.Sprintf("university = \"heidelberg\"\nuser = \"ab123\"\nsocks_port = %d\nhttp_port = %d\n", socksPort, httpPort)
	if err := os.WriteFile(filepath.Join(cfg, "config.toml"), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	return httpPort, socksPort
}

type core struct {
	cancel context.CancelFunc
	done   chan error
}

func start(t *testing.T, o Options) *core {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	o.Started = func() { close(started) }
	c := &core{cancel: cancel, done: make(chan error, 1)}
	go func() { c.done <- Run(ctx, o) }()
	select {
	case <-started:
	case err := <-c.done:
		t.Fatal(err)
	case <-time.After(10 * time.Second):
		t.Fatal("not started")
	}
	return c
}

func (c *core) stop(t *testing.T) {
	t.Helper()
	c.cancel()
	select {
	case err := <-c.done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("not stopped")
	}
}

func status(t *testing.T, port int, token string) int {
	t.Helper()
	req, _ := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/status.json", port), nil)
	if token != "" {
		req.Header.Set("X-Uni-VPN-Token", token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestStartStopStartInOneProcess(t *testing.T) {
	port := home(t)
	for range 2 {
		c := start(t, Options{APIToken: "t0ken"})
		if got := status(t, port, "t0ken"); got != 200 {
			t.Fatal(got)
		}
		if got := status(t, port, ""); got != 403 {
			t.Fatal(got)
		}
		c.stop(t)
		if conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
			conn.Close()
			t.Fatal("status port still open")
		}
	}
}

func TestSecondCoreFindsTheLockHeld(t *testing.T) {
	home(t)
	c := start(t, Options{})
	defer c.stop(t)
	begin := time.Now()
	err := Run(context.Background(), Options{LockWait: 300 * time.Millisecond})
	if !errors.Is(err, ErrLocked) {
		t.Fatal(err)
	}
	if waited := time.Since(begin); waited < 300*time.Millisecond {
		t.Fatal(waited)
	}
}

// A core waiting for the lock gets it once the other one stopped (the iPhone app hands over
// to its VPN extension so).
func TestLockWaitTakesOver(t *testing.T) {
	home(t)
	c := start(t, Options{})
	go func() {
		time.Sleep(300 * time.Millisecond)
		c.cancel()
	}()
	second := start(t, Options{LockWait: 10 * time.Second})
	second.stop(t)
	if err := <-c.done; err != nil {
		t.Fatal(err)
	}
}

// The Android app's core: the ready file says both ports are its own (another app could
// listen on 127.0.0.1 first), and it goes when the core ends.
func TestReadyFileWhileThePortsAreOurs(t *testing.T) {
	port := home(t)
	ready := filepath.Join(t.TempDir(), "api-ready")
	c := start(t, Options{APIToken: "t0ken", Extend: func(_ *slog.Logger, o *daemon.Options) { o.ReadyFile = ready }})
	if _, err := os.Stat(ready); err != nil {
		t.Fatal(err)
	}
	if got := status(t, port, "t0ken"); got != 200 {
		t.Fatal(got)
	}
	c.stop(t)
	if _, err := os.Stat(ready); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("ready file left behind", err)
	}
}

// Without its status or SOCKS port the phone's core ends instead of running without it:
// whoever holds the port would get the app's token and password, or the browsers' traffic.
func TestPortHeldByAnotherProgramEndsThePhoneCore(t *testing.T) {
	for _, which := range []string{"status", "socks"} {
		t.Run(which, func(t *testing.T) {
			httpPort, socksPort := homePorts(t)
			held := map[string]int{"status": httpPort, "socks": socksPort}[which]
			ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", held))
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			ready := filepath.Join(t.TempDir(), "api-ready")
			// Left by a core that was killed: it must not vouch for whoever holds the port now.
			if err := os.WriteFile(ready, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				done <- Run(context.Background(), Options{APIToken: "t0ken",
					Extend: func(_ *slog.Logger, o *daemon.Options) { o.ReadyFile = ready }})
			}()
			select {
			case err := <-done:
				if !errors.Is(err, ErrPortsTaken) {
					t.Fatal(err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("the core kept running without its port")
			}
			if _, err := os.Stat(ready); !errors.Is(err, fs.ErrNotExist) {
				t.Fatal("ready file without the port", err)
			}
			// The other port is free again for the next try.
			other := map[string]int{"status": socksPort, "socks": httpPort}[which]
			if conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", other)); err == nil {
				conn.Close()
				t.Fatal("port still open", other)
			}
		})
	}
}

// blockingStore keeps the daemon's sync from ending, so the daemon is slow to stop.
type blockingStore struct{ release chan struct{} }

func (s blockingStore) Load(context.Context) (string, error) { <-s.release; return "", nil }
func (blockingStore) Save(string) error                      { return nil }
func (blockingStore) Delete() error                          { return nil }

// The iPhone's app and tunnel hand the lock over while the old daemon may still be ending
// (Options.Release): the ready file then belongs to the next core, and the old daemon ending
// later must not remove it.
func TestReadyFileStaysWithTheNextCore(t *testing.T) {
	home(t)
	ready := filepath.Join(t.TempDir(), "api-ready")
	store := blockingStore{make(chan struct{})}
	// Like the iPhone's: a program started at that moment (here the browsers' proxy
	// refresh, or the look at the system's proxy setting) would hold a copy of the lock and
	// the ports until it runs.
	phone := func(o *daemon.Options) {
		o.ProxyRefresh = func(int) {}
		o.ProxyUnversioned = func(int) bool { return false }
		o.ProxyCover = func(int) {}
	}
	var release func()
	first := start(t, Options{APIToken: "t0ken", Release: func(f func()) { release = f },
		Extend: func(_ *slog.Logger, o *daemon.Options) {
			o.ReadyFile, o.SyncStore = ready, store
			phone(o)
		}})
	release()
	if _, err := os.Stat(ready); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("ready file kept after the lock was let go", err)
	}
	second := start(t, Options{APIToken: "t0ken", Extend: func(_ *slog.Logger, o *daemon.Options) { o.ReadyFile = ready; phone(o) }})
	close(store.release)
	select {
	case err := <-first.done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the first daemon did not end")
	}
	if _, err := os.Stat(ready); err != nil {
		t.Fatal("the old daemon removed the next core's ready file:", err)
	}
	second.stop(t)
	if _, err := os.Stat(ready); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("ready file left behind", err)
	}
}
