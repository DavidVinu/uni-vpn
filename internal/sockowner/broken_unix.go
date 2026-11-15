//go:build (linux && !android) || (darwin && !ios)

package sockowner

import (
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// broken reports whether c already failed: the client reset it. A reset takes both ends out of
// the system's table, so the lookup cannot tell, but the request it sent is still there to
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
	if err := raw.Control(func(fd uintptr) { code, gerr = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_ERROR) }); err != nil || gerr != nil {
		return false
	}
	return code != 0
}
