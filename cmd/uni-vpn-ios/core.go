//go:build ios || iosapi

package main

import (
	"context"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"runtime/debug"
	"sync"
	"time"

	"github.com/DavidVinu/uni-vpn/internal/coremain"
	"github.com/DavidVinu/uni-vpn/internal/credentials"
	"github.com/DavidVinu/uni-vpn/internal/daemon"
	"github.com/DavidVinu/uni-vpn/internal/platform"
)

// What UniVPNStart returns.
const (
	startOK         = 0
	startLocked     = 1
	startError      = 2
	startPortsTaken = 3
)

// memoryLimit: a packet tunnel extension may use about 50 MB.
const memoryLimit = 30 << 20

// The core of this process; the app runs one at a time.
var (
	mu       sync.Mutex
	cancel   context.CancelFunc
	finished chan struct{} // closed once coremain.Run returned
	release  func()        // frees the lock and the ports without waiting for the daemon
)

// stopWait is how long stop waits for the daemon to end by itself (a tunnel saying goodbye
// to the gateway) before it frees the lock and the ports and leaves the rest in the background.
var stopWait = 5 * time.Second

// extend is for tests, which make the daemon slow to stop.
var extend func(*slog.Logger, *daemon.Options)

// start runs the core like "uni-vpn daemon" in the background. Paths are the shared App Group
// container's, the key is FileStore's (64 hex digits), lockWait how long to wait for a core of
// the other process (app or extension) to let go. A core that already runs here counts as
// started.
func start(configHome, stateHome, apiToken, keyHex, deviceName string, lockWait time.Duration) int {
	mu.Lock()
	defer mu.Unlock()
	if runningLocked() {
		return startOK
	}
	if key, err := hex.DecodeString(keyHex); err != nil || len(key) != 32 || configHome == "" || stateHome == "" {
		return startError
	}
	os.Setenv("XDG_CONFIG_HOME", configHome)
	os.Setenv("XDG_STATE_HOME", stateHome)
	if deviceName != "" {
		os.Setenv("UNI_VPN_DEVICE_NAME", deviceName)
	} else {
		os.Unsetenv("UNI_VPN_DEVICE_NAME")
	}
	os.Setenv(credentials.KeyEnv, keyHex)
	credentials.ReloadFileStore()
	os.Unsetenv(credentials.KeyEnv)
	platform.InProcessOpenConnect = true
	debug.SetMemoryLimit(memoryLimit)

	ctx, stop := context.WithCancel(context.Background())
	started := make(chan struct{})
	result := make(chan error, 1)
	done := make(chan struct{})
	var now func()
	go func() {
		result <- coremain.Run(ctx, coremain.Options{APIToken: apiToken, LockWait: lockWait,
			Extend: func(log *slog.Logger, o *daemon.Options) {
				// Any app can listen on 127.0.0.1 before the core does: the app and the tunnel
				// talk to the core only while the ready file exists, as on Android.
				o.ReadyFile = platform.ReadyFile()
				if extend != nil {
					extend(log, o)
				}
			},
			Release: func(f func()) { now = f }, Started: func() { close(started) }})
		close(done)
	}()
	select {
	case <-started:
		cancel, finished, release = stop, done, now
		return startOK
	case err := <-result:
		stop()
		if errors.Is(err, coremain.ErrLocked) {
			return startLocked
		}
		if errors.Is(err, coremain.ErrPortsTaken) {
			return startPortsTaken
		}
		return startError
	}
}

// stop ends the core. It returns once the core released its lock and ports, after stopWait
// at most: what is left of a slow daemon then ends in the background.
func stop() {
	mu.Lock()
	defer mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(stopWait):
		release()
	}
	cancel, finished, release = nil, nil, nil
}

func running() bool {
	mu.Lock()
	defer mu.Unlock()
	return runningLocked()
}

func runningLocked() bool {
	if finished == nil {
		return false
	}
	select {
	case <-finished:
		return false // ended by itself
	default:
		return true
	}
}
