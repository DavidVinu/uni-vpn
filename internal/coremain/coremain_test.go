package coremain

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
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
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	httpPort, socksPort := freePort(t), freePort(t)
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
	return httpPort
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
