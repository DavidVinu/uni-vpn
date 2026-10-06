//go:build !windows

package main

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// harden: no core dumps with the password in memory.
func harden() {
	_ = unix.Setrlimit(unix.RLIMIT_CORE, &unix.Rlimit{Cur: 0, Max: 0})
	notDumpable()
}

func umask() { syscall.Umask(0o077) }

// acquireLock is an exclusive lock for the lifetime of the process (flock, like the Python
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
