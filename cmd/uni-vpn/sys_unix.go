//go:build !windows

package main

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// harden: no core dumps with the password in memory.
func harden() {
	_ = unix.Setrlimit(unix.RLIMIT_CORE, &unix.Rlimit{Cur: 0, Max: 0})
	notDumpable()
}

func umask() { syscall.Umask(0o077) }
