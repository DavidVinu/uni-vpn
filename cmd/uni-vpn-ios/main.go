//go:build ios

// Command uni-vpn-ios is the Go core as a C library for the iPhone app and its packet tunnel
// extension (go build -buildmode=c-archive -tags libopenconnect). iOS runs no child
// processes, so the core runs in the calling process and openconnect is a library too.
package main

import "C"

import (
	"strconv"
	"time"
)

// UniVPNStart starts the core in the background: 0 started, 1 another core holds the lock
// for longer than lockWaitMs, 2 error.
//
//export UniVPNStart
func UniVPNStart(configHome, stateHome, apiToken, secretsKeyHex, deviceName, lockWaitMs *C.char) C.int {
	wait, _ := strconv.Atoi(C.GoString(lockWaitMs))
	return C.int(start(C.GoString(configHome), C.GoString(stateHome), C.GoString(apiToken),
		C.GoString(secretsKeyHex), C.GoString(deviceName), time.Duration(wait)*time.Millisecond))
}

// UniVPNStop returns when the core stopped and released its lock and ports.
//
//export UniVPNStop
func UniVPNStop() { stop() }

// UniVPNRunning is 1 while the core runs, else 0.
//
//export UniVPNRunning
func UniVPNRunning() C.int {
	if running() {
		return 1
	}
	return 0
}

func main() {}
