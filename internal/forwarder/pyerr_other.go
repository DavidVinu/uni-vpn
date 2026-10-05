//go:build !windows

package forwarder

import (
	"fmt"
	"strings"
	"syscall"
)

// The selector event loop reports a failed non-blocking connect this way.
func connectFailed(errno syscall.Errno, ip string, port int) string {
	return fmt.Sprintf("[Errno %d] Connect call failed ('%s', %d)", int(errno), ip, port)
}

func strerror(errno syscall.Errno) string {
	s := errno.Error()
	if s != "" {
		s = strings.ToUpper(s[:1]) + s[1:]
	}
	return s
}
