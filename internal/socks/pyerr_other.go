//go:build !windows

package socks

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"
)

func strerror(errno syscall.Errno) string {
	s := errno.Error()
	if s != "" {
		s = strings.ToUpper(s[:1]) + s[1:]
	}
	return s
}

func lowerStrerror(errno syscall.Errno) string { return strings.ToLower(strerror(errno)) }

func errnoString(errno syscall.Errno) string {
	return fmt.Sprintf("[Errno %d] %s", int(errno), strerror(errno))
}

// The selector event loop reports a failed non-blocking connect this way.
func connectFailed(errno syscall.Errno, ip string, port int) string {
	return fmt.Sprintf("[Errno %d] Connect call failed ('%s', %d)", int(errno), ip, port)
}

func isRefused(err error) bool { return errors.Is(err, syscall.ECONNREFUSED) }

func reportICMPErrors(*net.UDPConn) {}
