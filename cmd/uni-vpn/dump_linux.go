package main

import "golang.org/x/sys/unix"

func notDumpable() { _ = unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0) }
