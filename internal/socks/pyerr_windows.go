package socks

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

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

func lowerStrerror(errno syscall.Errno) string { return strings.ToLower(strerror(errno)) }

func errnoString(errno syscall.Errno) string {
	return fmt.Sprintf("[WinError %d] %s", int(errno), strerror(errno))
}

// The proactor event loop raises the plain Windows error.
func connectFailed(errno syscall.Errno, _ string, _ int) string { return errnoString(errno) }

func isRefused(err error) bool {
	return errors.Is(err, windows.WSAECONNREFUSED) || errors.Is(err, windows.ERROR_CONNECTION_REFUSED)
}

// Go turns off reporting of ICMP port unreachable on UDP sockets; asyncio's proactor gets
// it (WSAECONNRESET), so a dead DNS server fails at once instead of after the timeout.
func reportICMPErrors(conn *net.UDPConn) {
	rc, err := conn.SyscallConn()
	if err != nil {
		return
	}
	rc.Control(func(fd uintptr) {
		flag := uint32(1)
		var ret uint32
		windows.WSAIoctl(windows.Handle(fd), windows.SIO_UDP_CONNRESET, (*byte)(unsafe.Pointer(&flag)),
			uint32(unsafe.Sizeof(flag)), nil, 0, &ret, nil, 0)
	})
}
