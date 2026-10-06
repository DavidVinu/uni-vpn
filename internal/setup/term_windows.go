package setup

import (
	"os"

	"golang.org/x/sys/windows"
)

// isTerminal reports whether stdin is a console.
func isTerminal() bool {
	var mode uint32
	return windows.GetConsoleMode(windows.Handle(os.Stdin.Fd()), &mode) == nil
}

// noEcho turns off the echo of typed characters on stdin and returns how to turn it back on.
func noEcho() (restore func(), ok bool) {
	h := windows.Handle(os.Stdin.Fd())
	var mode uint32
	if windows.GetConsoleMode(h, &mode) != nil {
		return func() {}, false
	}
	quiet := (mode &^ windows.ENABLE_ECHO_INPUT) | windows.ENABLE_LINE_INPUT | windows.ENABLE_PROCESSED_INPUT
	if windows.SetConsoleMode(h, quiet) != nil {
		return func() {}, false
	}
	return func() { windows.SetConsoleMode(h, mode) }, true
}

// writable: only used for the macOS command folder.
func writable(string) bool { return true }
