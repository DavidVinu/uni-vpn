package netstack

import (
	"errors"
	"syscall"
)

// queueFull: Darwin answers a write to a full datagram socketpair with ENOBUFS at once instead
// of making the writer wait, so the packet would be lost and TCP would stall on retransmits.
func queueFull(err error) bool { return errors.Is(err, syscall.ENOBUFS) }
