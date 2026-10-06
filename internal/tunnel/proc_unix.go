//go:build !windows

package tunnel

import (
	"os"
	"os/exec"
	"syscall"
)

func newSession(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func terminate(p *os.Process) error { return p.Signal(syscall.SIGTERM) }

func reconnectSignal(p *os.Process) error { return p.Signal(syscall.SIGUSR2) }
