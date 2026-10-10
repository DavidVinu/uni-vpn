//go:build windows

package tunnel

import (
	"os"
	"os/exec"
)

// The Windows tunnel (package wintunnel) sets its own process attributes.
func newSession(cmd *exec.Cmd) {}

// Python's terminate() is TerminateProcess on Windows.
func terminate(p *os.Process) error { return p.Kill() }

// No SIGUSR2 on Windows.
func reconnectSignal(p *os.Process) error { return nil }
