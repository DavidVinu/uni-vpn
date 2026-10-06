//go:build windows

package platform

import (
	"os/exec"
	"syscall"
)

const createNoWindow = 0x08000000

func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow, HideWindow: true}
}

// HideWindow keeps a child process from opening a console window.
func HideWindow(cmd *exec.Cmd) { hideWindow(cmd) }
