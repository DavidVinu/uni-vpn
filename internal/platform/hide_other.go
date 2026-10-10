//go:build !windows

package platform

import "os/exec"

// HideWindow keeps a child process from opening a console window on Windows.
func hideWindow(cmd *exec.Cmd) {}

func HideWindow(cmd *exec.Cmd) { hideWindow(cmd) }
