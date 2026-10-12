package netstack

import (
	"errors"
	"syscall"
)

// sendPacket writes one packet without Go's poller. On Darwin a datagram socketpair never
// reports itself writable again once full (kqueue looks at the sender's own, always empty,
// buffer), so a poller write could wait forever; a full queue also answers ENOBUFS instead of
// EAGAIN. Both mean: try again in a moment.
func (s *Stack) sendPacket(pkt []byte) (full bool, err error) {
	raw, err := s.file.SyscallConn()
	if err != nil {
		return false, err
	}
	var werr error
	if err := raw.Write(func(fd uintptr) bool {
		_, werr = syscall.Write(int(fd), pkt)
		return true
	}); err != nil {
		return false, err
	}
	if errors.Is(werr, syscall.EAGAIN) || errors.Is(werr, syscall.ENOBUFS) {
		return true, werr
	}
	return false, werr
}
