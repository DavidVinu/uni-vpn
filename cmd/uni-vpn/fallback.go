package main

import (
	"os"
	"os/exec"
	"path/filepath"

	"github.com/DavidVinu/uni-vpn/internal/platform"
)

// The commands only people at a terminal use (status, connect, doctor, ...) are not ported
// yet. While the Python core is still installed next to this one (an installation that moved
// over through the hand-over, docs/go-switch.md), they run there; UNI_VPN_PYTHON keeps
// bin/uni-vpn from handing them back.
func pythonCommand(root string, args []string) []string {
	script := filepath.Join(root, "bin", "uni-vpn")
	if _, err := os.Stat(filepath.Join(root, "uni_vpn", "cli.py")); err != nil {
		return nil
	}
	var python string
	if platform.IsWindows {
		python = filepath.Join(root, "python", "python.exe")
		if _, err := os.Stat(python); err != nil {
			return nil
		}
	} else if found, err := exec.LookPath("python3"); err == nil {
		python = found
	} else {
		return nil
	}
	return append([]string{python, "-I", script}, args...)
}

func runPython(args []string) (int, bool) {
	command := pythonCommand(filepath.Dir(filepath.Dir(executable())), args)
	if command == nil {
		return 0, false
	}
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), "UNI_VPN_PYTHON=1")
	if err := cmd.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return exit.ExitCode(), true
		}
		return 1, true
	}
	return 0, true
}
