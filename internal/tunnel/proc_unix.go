//go:build !windows

package tunnel

import (
	"os"
	"os/exec"
	"syscall"

	"github.com/DavidVinu/uni-vpn/internal/platform"
)

// newSession starts openconnect in a session of its own. Not on macOS: when the service dies (a
// crash, a SIGKILL), launchd ends what is left of it by its process group only, so openconnect
// and its ocproxy would keep the VPN session open beside the next start's. systemd ends the
// unit's whole cgroup, and Windows has a job object (winsys.KillChildrenWithUs).
func newSession(cmd *exec.Cmd) {
	if platform.IsMacOS {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func terminate(p *os.Process) error { return p.Signal(syscall.SIGTERM) }

func reconnectSignal(p *os.Process) error { return p.Signal(syscall.SIGUSR2) }
