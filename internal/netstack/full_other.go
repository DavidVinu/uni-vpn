//go:build !darwin

package netstack

// queueFull: elsewhere a full socketpair makes the write wait (EAGAIN goes to the poller).
func queueFull(error) bool { return false }
