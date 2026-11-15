//go:build linux

package desktop

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Another account's shell from the same packaged script is not this account's: Repair would
// skip starting this one's, and a restart would wait for one it cannot stop.
func TestRunningSkipsAnotherAccountsProcess(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("only root can start a process as another account")
	}
	dir := t.TempDir()
	os.Chmod(dir, 0o755)
	marker := filepath.Join(dir, "uni-vpn-app.py")
	cmd := exec.Command("sh", "-c", "sleep 30; exit 0", marker)
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65534, Gid: 65534}}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	for i := 0; i < 50; i++ {
		if cmdline, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", cmd.Process.Pid)); strings.Contains(string(cmdline), marker) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := Running(marker); len(got) != 0 {
		t.Fatal("found", len(got), "processes, the first", got[0].PID)
	}
}
