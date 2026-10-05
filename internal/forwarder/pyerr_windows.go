package forwarder

import (
	"fmt"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// The proactor event loop raises the plain Windows error.
func connectFailed(errno syscall.Errno, _ string, _ int) string {
	return fmt.Sprintf("[WinError %d] %s", int(errno), strerror(errno))
}

// strerror is the system message in the user's language, trimmed like Python does.
func strerror(errno syscall.Errno) string {
	buf := make([]uint16, 512)
	const neutral = 0x0400 // MAKELANGID(LANG_NEUTRAL, SUBLANG_DEFAULT)
	n, err := windows.FormatMessage(windows.FORMAT_MESSAGE_FROM_SYSTEM|windows.FORMAT_MESSAGE_IGNORE_INSERTS,
		0, uint32(errno), neutral, buf, nil)
	if err != nil {
		return fmt.Sprintf("Windows Error 0x%x", uint32(errno))
	}
	return strings.TrimRight(windows.UTF16ToString(buf[:n]), " \t\r\n.")
}
