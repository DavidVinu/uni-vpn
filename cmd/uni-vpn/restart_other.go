//go:build !windows

package main

import (
	"os"
	"syscall"
)

var execve = syscall.Exec

// restart starts the updated program in place of this process: same process id, so systemd
// and launchd keep watching the same service. exe is the path the program was started from,
// where the update put the new one.
func restart(exe string) int {
	_ = execve(exe, os.Args, os.Environ())
	return 1 // only reached when exec failed
}
