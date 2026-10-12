//go:build iosapi

package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/DavidVinu/uni-vpn/internal/credentials"
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
		if got := status(t, port, "wrong"); got != 403 {
			t.Fatal(got)
		}
		if got := start(configHome, stateHome, "t0ken", key, "", 0); got != startOK {
			t.Fatal("a running core counts as started", got)
		}
		stop()
		if running() {
			t.Fatal("still running")
		}
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
