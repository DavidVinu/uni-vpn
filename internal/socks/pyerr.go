package socks

import (
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
)

// Python's asyncio and OSError word errors their own way; the log lines keep that wording.

// osErrorString is str(OSError) for an error that carries an errno, else err.Error().
func osErrorString(err error) string {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return err.Error()
	}
	return errnoString(errno)
}

// connectErrorString is str(exc) for a failed asyncio.open_connection to ip:port.
func connectErrorString(err error, source, ip string, port int) string {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return err.Error()
	}
	var se *os.SyscallError
	if errors.As(err, &se) && se.Syscall == "bind" {
		return fmt.Sprintf("[Errno %d] error while attempting to bind on address ('%s', 0): %s",
			int(errno), source, lowerStrerror(errno))
	}
	return connectFailed(errno, ip, port)
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.Is(err, os.ErrDeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout())
}
