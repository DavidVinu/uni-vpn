//go:build !darwin

package netstack

// sendPacket writes one packet; a full socketpair makes the write wait in Go's poller.
func (s *Stack) sendPacket(pkt []byte) (full bool, err error) {
	_, err = s.file.Write(pkt)
	return false, err
}
