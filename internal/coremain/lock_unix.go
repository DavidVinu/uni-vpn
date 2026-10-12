//go:build !windows

package coremain

import (
	"os"

	"golang.org/x/sys/unix"
)

// acquireLock is an exclusive lock for the lifetime of the daemon (flock, like the Python
// core, so either core sees the other); nil if another daemon holds it.
func acquireLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o666)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, nil // Python treats every OSError here as "held by another daemon"
	}
	return f, nil
}
