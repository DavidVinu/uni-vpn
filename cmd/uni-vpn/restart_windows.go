package main

import (
	"errors"
	"os"
	"os/exec"

	"github.com/DavidVinu/uni-vpn/internal/platform"
)

// restart runs the updated program from exe, the path this one was started from (the old
// file is now <name>.old), with the same arguments.
func restart(exe string) int {
	return supervise(os.Getenv, os.Environ(), func(env []string) int {
		cmd := exec.Command(exe, os.Args[1:]...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		cmd.Env = env
		platform.HideWindow(cmd)
		err := cmd.Run()
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		if err != nil {
			return 1
		}
		return 0
	})
}
