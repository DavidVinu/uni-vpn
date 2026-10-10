package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// harden: no core files on Windows; Windows Error Reporting is per process.
func harden() {}

func umask() {}

// acquireLock locks the first byte (msvcrt.locking LK_NBLCK in the Python core, so either
// core sees the other); nil if another daemon holds it.
func acquireLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o666)
	if err != nil {
		return nil, err
	}
	ol := new(windows.Overlapped)
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, ol); err != nil {
		f.Close()
		return nil, nil
	}
	return f, nil
}
