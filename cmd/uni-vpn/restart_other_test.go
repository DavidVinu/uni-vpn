//go:build !windows

package main

import (
	"errors"
	"os"
	"slices"
	"testing"
)

func TestPosixReplacesTheProcess(t *testing.T) {
	old, oldArgs := execve, os.Args
	defer func() { execve, os.Args = old, oldArgs }()
	os.Args = []string{"/app/bin/uni-vpn-core", "daemon"}
	var exe string
	var argv []string
	execve = func(path string, args []string, env []string) error {
		exe, argv = path, args
		return errors.New("exec failed")
	}
	if rc := restart("/app/bin/uni-vpn-core"); rc != 1 {
		t.Fatal(rc)
	}
	if exe != "/app/bin/uni-vpn-core" || !slices.Equal(argv, []string{"/app/bin/uni-vpn-core", "daemon"}) {
		t.Fatal(exe, argv)
	}
}
