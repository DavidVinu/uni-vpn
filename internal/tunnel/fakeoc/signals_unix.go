//go:build !windows

package fakeoc

import (
	"os"
	"syscall"
)

var extraSignals = []os.Signal{syscall.SIGUSR2}

func isReconnectSignal(s os.Signal) bool { return s == syscall.SIGUSR2 }
