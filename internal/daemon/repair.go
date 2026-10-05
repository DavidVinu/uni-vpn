package daemon

import (
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/DavidVinu/uni-vpn/internal/platform"
)

// The app's Repair button runs the installer again, without a terminal. It installs what is
// missing (openconnect, ocproxy, Wintun), registers the service again and restarts it. Where
// that needs administrator rights, the operating system asks in its own window: the UAC
// prompt on Windows, the password dialog of pkexec on Linux. On macOS Homebrew installs
// without asking. It mirrors uni_vpn/repair.py.
//
// The installer outlives this daemon, because the service restart at its end stops the
// daemon: Linux starts it as a unit of its own (systemd would stop everything in the
// service's cgroup), macOS in a session of its own, Windows outside the daemon's
// kill-on-close job.

// RepoRoot is the installation the installer scripts live in: the parent of BinDir.
func RepoRoot() string { return filepath.Dir(BinDir()) }

// RepairCommand is the installer's command line.
func RepairCommand() []string {
	root := RepoRoot()
	if platform.IsWindows {
		return []string{"powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File",
			filepath.Join(root, "install.ps1"), "-Repair"}
	}
	installer := []string{"/bin/bash", filepath.Join(root, "install.sh"), "--repair"}
	if !platform.IsMacOS {
		if systemdRun, err := exec.LookPath("systemd-run"); err == nil {
			return append([]string{systemdRun, "--user", "--collect", "--quiet", "--wait", "--"}, installer...)
		}
	}
	return installer
}

type cmdProcess struct {
	once sync.Once
	done chan struct{}
	mu   sync.Mutex
	code int
}

func (p *cmdProcess) Poll() (int, bool) {
	select {
	case <-p.done:
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.code, true
	default:
		return 0, false
	}
}

func (p *cmdProcess) Wait() int {
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.code
}

func watch(cmd *exec.Cmd) Process {
	p := &cmdProcess{done: make(chan struct{})}
	go func() {
		cmd.Wait()
		p.mu.Lock()
		p.code = cmd.ProcessState.ExitCode()
		p.mu.Unlock()
		close(p.done)
	}()
	return p
}

// StartRepairProcess starts the installer in repair mode.
func StartRepairProcess() (Process, error) {
	argv := RepairCommand()
	newCmd := func() *exec.Cmd {
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Dir = RepoRoot()
		return cmd
	}
	cmd, err := startRepair(newCmd)
	if err != nil {
		return nil, err
	}
	return watch(cmd), nil
}
