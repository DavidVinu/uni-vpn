//go:build linux

package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/DavidVinu/uni-vpn/internal/platform"
)

// The Android app starts the core with its token and shows "another app is in the way" for
// this exit code (Core.kt), instead of waiting for a core that can never answer.
func TestPhoneCoreEndsWhenAnotherAppHoldsItsPort(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME"} {
		t.Setenv(name, filepath.Join(dir, name))
	}
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	socksPort := free.Addr().(*net.TCPAddr).Port
	free.Close()
	cfg := filepath.Join(dir, "XDG_CONFIG_HOME", "uni-vpn")
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	toml := fmt.Sprintf("university = \"heidelberg\"\nuser = \"ab123\"\nsocks_port = %d\nhttp_port = %d\n",
		socksPort, held.Addr().(*net.TCPAddr).Port)
	if err := os.WriteFile(filepath.Join(cfg, "config.toml"), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(apiTokenEnv, "t0ken")
	if rc := run([]string{"daemon"}); rc != exitPortsTaken {
		t.Fatal(rc)
	}
	if _, err := os.Stat(platform.ReadyFile()); err == nil {
		t.Fatal("ready file without the port")
	}
}
