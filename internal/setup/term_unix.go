//go:build linux || darwin

package setup

import (
	"os"

	"golang.org/x/sys/unix"
)

// isTerminal reports whether stdin is a terminal.
func isTerminal() bool {
	_, err := unix.IoctlGetTermios(int(os.Stdin.Fd()), ioctlGetTermios)
	return err == nil
}

// noEcho turns off the echo of typed characters on stdin and returns how to turn it back on.
func noEcho() (restore func(), ok bool) {
	fd := int(os.Stdin.Fd())
	old, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		return func() {}, false
	}
	quiet := *old
	quiet.Lflag &^= unix.ECHO
	quiet.Lflag |= unix.ICANON | unix.ISIG
	quiet.Iflag |= unix.ICRNL
	if err := unix.IoctlSetTermios(fd, ioctlSetTermios, &quiet); err != nil {
		return func() {}, false
	}
	return func() { unix.IoctlSetTermios(fd, ioctlSetTermios, old) }, true
}

// writable is os.access(path, W_OK).
func writable(path string) bool { return unix.Access(path, unix.W_OK) == nil }
