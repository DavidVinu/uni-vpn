//go:build windows

package sockowner

import (
	"net"
	"syscall"

	"golang.org/x/sys/windows"
)

const soError = 0x1007 // SO_ERROR

// broken reports whether c already failed: the client reset it. A reset takes both ends out of
// the system's table, so the lookup cannot tell, but the request it sent may still be there to
// read. Nobody reads the answer, and another account must not get a request through this way.
func broken(c net.Conn) bool {
	sc, ok := c.(syscall.Conn)
	if !ok {
		return false
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return false
	}
	var code int
	var gerr error
	if err := raw.Control(func(fd uintptr) { code, gerr = windows.GetsockoptInt(windows.Handle(fd), windows.SOL_SOCKET, soError) }); err != nil || gerr != nil {
		return false
	}
	return code != 0
}
