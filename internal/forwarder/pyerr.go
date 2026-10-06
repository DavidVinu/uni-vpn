package forwarder

import (
	"errors"
	"syscall"
)

// connectErrorString is str(exc) for a failed asyncio.open_connection to ip:port, so the
// log line reads like the Python one.
func connectErrorString(err error, ip string, port int) string {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return err.Error()
	}
	return connectFailed(errno, ip, port)
}
