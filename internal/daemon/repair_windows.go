package daemon

import (
	"errors"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

const createBreakawayFromJob = 0x01000000

func startRepair(newCmd func() *exec.Cmd) (*exec.Cmd, error) {
	// A visible window, so the progress and the administrator prompt are not hidden.
	cmd := newCmd()
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_CONSOLE | createBreakawayFromJob}
	err := cmd.Start()
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		// An outer job (Task Scheduler) without breakaway: the elevated part that install.ps1
		// starts through UAC is not ours anyway.
		cmd = newCmd()
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_CONSOLE}
		err = cmd.Start()
	}
	return cmd, err
}
