package daemon

import (
	"errors"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"

	"github.com/DavidVinu/uni-vpn/internal/winsys"
)

const createBreakawayFromJob = 0x01000000

// startRepair: done, when not nil, runs once the installer has ended.
func startRepair(newCmd func() *exec.Cmd, own bool) (*exec.Cmd, func(), error) {
	window := repairWindow(own)
	cmd := newCmd()
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: window | createBreakawayFromJob}
	err := cmd.Start()
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return cmd, nil, err
	}
	// An outer job (Task Scheduler) without breakaway: the installer stays in this daemon's
	// kill-on-close job, and this program's setup ends this daemon. Until the installer is
	// done the job ends nothing with us (Repair disconnects first, so openconnect is not
	// running). The elevated part that install.ps1 starts through UAC is not ours anyway.
	kept := winsys.ChildrenEndWithUs(false)
	done := func() {
		if kept {
			winsys.ChildrenEndWithUs(true)
		}
	}
	cmd = newCmd()
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: window}
	if err := cmd.Start(); err != nil {
		done()
		return cmd, nil, err
	}
	return cmd, done, nil
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
