//go:build !windows

package daemon

import (
	"os/exec"
	"syscall"
)

func startRepair(newCmd func() *exec.Cmd) (*exec.Cmd, error) {
	cmd := newCmd() // stdin, stdout and stderr stay nil: /dev/null
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd, cmd.Start()
}
