package daemon

import (
	"errors"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

const createBreakawayFromJob = 0x01000000

func startRepair(newCmd func() *exec.Cmd, own bool) (*exec.Cmd, error) {
	window := repairWindow(own)
	cmd := newCmd()
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: window | createBreakawayFromJob}
	err := cmd.Start()
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		// An outer job (Task Scheduler) without breakaway: the elevated part that install.ps1
		// starts through UAC is not ours anyway.
		cmd = newCmd()
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: window}
		err = cmd.Start()
	}
	return cmd, err
}

// repairWindow: install.ps1 gets a visible window, so the progress and the administrator
// prompt are not hidden. This program's own setup (own) runs elevated already and has nothing
// to show but English log lines.
func repairWindow(own bool) uint32 {
	if own {
		return windows.CREATE_NO_WINDOW
	}
	return windows.CREATE_NEW_CONSOLE
}
