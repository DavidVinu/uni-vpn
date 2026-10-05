//go:build windows

package fakeoc

import "os"

var extraSignals []os.Signal

func isReconnectSignal(os.Signal) bool { return false }
