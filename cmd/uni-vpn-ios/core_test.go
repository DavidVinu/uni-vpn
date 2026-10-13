//go:build iosapi

package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/DavidVinu/uni-vpn/internal/credentials"
	"github.com/DavidVinu/uni-vpn/internal/daemon"
	"github.com/DavidVinu/uni-vpn/internal/platform"
)

var key = strings.Repeat("ab", 32)

func freePort(t *testing.T) int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// container is the App Group folder with a config.toml on free ports.
func container(t *testing.T) (configHome, stateHome string, httpPort int) {
	dir := t.TempDir()
	configHome, stateHome = filepath.Join(dir, "config"), filepath.Join(dir, "state")
	// start sets these for the whole process; the test puts them back.
	for _, name := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "HOME", "UNI_VPN_DEVICE_NAME"} {
		t.Setenv(name, os.Getenv(name))
	}
	old := platform.InProcessOpenConnect
	t.Cleanup(func() { platform.InProcessOpenConnect = old })
	httpPort, socksPort := freePort(t), freePort(t)
	for socksPort == httpPort {
		socksPort = freePort(t)
	}
	if err := os.MkdirAll(filepath.Join(configHome, "uni-vpn"), 0o700); err != nil {
		t.Fatal(err)
	}
	toml := fmt.Sprintf("university = \"heidelberg\"\nuser = \"ab123\"\nsocks_port = %d\nhttp_port = %d\n", socksPort, httpPort)
	if err := os.WriteFile(filepath.Join(configHome, "uni-vpn", "config.toml"), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	return configHome, stateHome, httpPort
}

// readyExists: the core vouches that both ports are its own (daemon.Options.ReadyFile).
func readyExists(stateHome string) bool {
	_, err := os.Stat(filepath.Join(stateHome, "uni-vpn", "api-ready"))
	return err == nil
}

func status(t *testing.T, port int, token string) int {
	t.Helper()
	req, _ := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/status.json", port), nil)
	req.Header.Set("X-Uni-VPN-Token", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestStartStopStart(t *testing.T) {
	configHome, stateHome, port := container(t)
	for range 2 {
		if got := start(configHome, stateHome, "t0ken", key, "Davids iPhone", 0); got != startOK {
			t.Fatal(got)
		}
		if !running() || !platform.InProcessOpenConnect || os.Getenv("UNI_VPN_DEVICE_NAME") != "Davids iPhone" {
			t.Fatal("not set up")
		}
		if os.Getenv(credentials.KeyEnv) != "" || credentials.AndroidStore().Path != filepath.Join(configHome, "uni-vpn", "secrets.json") {
			t.Fatal("file store")
		}
		if got := status(t, port, "t0ken"); got != 200 {
			t.Fatal(got)
		}
		if !readyExists(stateHome) {
			t.Fatal("no ready file")
		}
		if got := status(t, port, "wrong"); got != 403 {
			t.Fatal(got)
		}
		if got := start(configHome, stateHome, "t0ken", key, "", 0); got != startOK {
			t.Fatal("a running core counts as started", got)
		}
		stop()
		if running() || readyExists(stateHome) {
			t.Fatal("still running")
		}
	}
}

// Another app listens on the core's port first: the core ends with its own code, so the app
// says so, and no ready file (not even one left by a killed core) sends the app's token there.
func TestAnotherAppHoldsThePort(t *testing.T) {
	configHome, stateHome, httpPort := container(t)
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", httpPort))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if err := os.MkdirAll(filepath.Join(stateHome, "uni-vpn"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateHome, "uni-vpn", "api-ready"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := start(configHome, stateHome, "t0ken", key, "", 0); got != startPortsTaken {
		t.Fatal(got)
	}
	if running() || readyExists(stateHome) {
		t.Fatal("ready without the port")
	}
}

// The other process (app or extension) holds the lock.
func TestLockHeldElsewhere(t *testing.T) {
	configHome, stateHome, _ := container(t)
	f, err := os.OpenFile(filepath.Join(configHome, "uni-vpn", "daemon.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	begin := time.Now()
	if got := start(configHome, stateHome, "", key, "", 200*time.Millisecond); got != startLocked {
		t.Fatal(got)
	}
	if time.Since(begin) < 200*time.Millisecond || running() {
		t.Fatal("did not wait")
	}
	unix.Flock(int(f.Fd()), unix.LOCK_UN)
	if got := start(configHome, stateHome, "", key, "", 0); got != startOK {
		t.Fatal(got)
	}
}

func TestBadKey(t *testing.T) {
	configHome, stateHome, _ := container(t)
	for _, k := range []string{"", "xyz", strings.Repeat("ab", 16)} {
		if got := start(configHome, stateHome, "", k, "", 0); got != startError {
			t.Fatal(k, got)
		}
	}
}

// blockingStore keeps the daemon's sync from ending, so the daemon is slow to stop.
type blockingStore struct{ release chan struct{} }

func (s blockingStore) Load(context.Context) (string, error) { <-s.release; return "", nil }
func (blockingStore) Save(string) error                      { return nil }
func (blockingStore) Delete() error                          { return nil }

// However slow the daemon is to end, UniVPNStop returns in time with the lock and the ports
// free, so the other process (app or extension) can start its core right away.
func TestStopIsBounded(t *testing.T) {
	configHome, stateHome, httpPort := container(t)
	store := blockingStore{make(chan struct{})}
	extend = func(_ *slog.Logger, o *daemon.Options) { o.SyncStore = store }
	oldWait := stopWait
	stopWait = 300 * time.Millisecond
	t.Cleanup(func() { extend, stopWait = nil, oldWait })
	if got := start(configHome, stateHome, "", key, "", 0); got != startOK {
		t.Fatal(got)
	}
	core := finished
	begin := time.Now()
	stop()
	if took := time.Since(begin); took > 2*time.Second {
		t.Fatal("stop took", took)
	}
	select {
	case <-core:
		t.Fatal("the daemon was not slow")
	default:
	}
	if running() {
		t.Fatal("still running")
	}
	f, err := os.OpenFile(filepath.Join(configHome, "uni-vpn", "daemon.lock"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal("lock still held:", err)
	}
	f.Close()
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", httpPort))
	if err != nil {
		t.Fatal("status port still held:", err)
	}
	ln.Close()
	if readyExists(stateHome) {
		t.Fatal("ready file kept after the lock was let go")
	}
	// A new core starts while the old one is still ending; the old one ending later leaves the
	// new one's ready file alone.
	extend = nil
	if got := start(configHome, stateHome, "", key, "", 0); got != startOK {
		t.Fatal(got)
	}
	close(store.release)
	<-core
	if !readyExists(stateHome) {
		t.Fatal("the old core removed the new one's ready file")
	}
	stop()
}
