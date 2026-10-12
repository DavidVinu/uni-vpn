//go:build unix

package libtunnel

import (
	"os"
	"syscall"
)

// sockBuf: Darwin gives a datagram socketpair 2 KiB per datagram and 4 KiB of queue, which
// holds two packets; Linux has more but the same value does no harm there.
const sockBuf = 256 << 10

// socketPair returns the network stack's end (non-blocking, so Close interrupts its reads)
// and openconnect's end of a packet socketpair.
func socketPair() (*os.File, int, error) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_DGRAM, 0)
	if err != nil {
		return nil, -1, err
	}
	for _, fd := range fds {
		syscall.CloseOnExec(fd)
		_ = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_SNDBUF, sockBuf)
		_ = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_RCVBUF, sockBuf)
	}
	if err := syscall.SetNonblock(fds[0], true); err != nil {
		syscall.Close(fds[0])
		syscall.Close(fds[1])
		return nil, -1, err
	}
	return os.NewFile(uintptr(fds[0]), "netstack"), fds[1], nil
}

func closeFD(fd int) { _ = syscall.Close(fd) }
