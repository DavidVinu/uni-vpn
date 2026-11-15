//go:build windows

package service

// Checks against the real Task Scheduler (CI: windows-latest).

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DavidVinu/uni-vpn/internal/winsys"
)

// Whose the task of earlier versions is: registered as uni-vpn registers it (this account's
// name in the XML), Task Scheduler answers with a name or a SID; both must count as ours.
func TestTaskOwnerScriptAgainstTaskScheduler(t *testing.T) {
	if !winsys.IsAdmin() {
		t.Skip("creating a task with the highest run level needs administrator rights")
	}
	sid := winsys.CurrentSID()
	if !strings.HasPrefix(sid, "S-1-") {
		t.Fatalf("CurrentSID: %q", sid)
	}
	name := fmt.Sprintf("uni-vpn-test-%08x", time.Now().UnixNano()&0xffffffff)
	owner := func(sid string) string {
		cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", taskOwnerScript(name, sid))
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s %v: %s", out, err, stderr.String())
		}
		return strings.TrimSpace(string(out))
	}
	if got := owner(sid); got != "" {
		t.Fatalf("without the task: %q", got)
	}
	exe, _ := os.Executable()
	xmlFile := filepath.Join(t.TempDir(), "task.xml")
	text := strings.ReplaceAll(winsys.RenderTaskBinary(winsys.Conhost(), exe, winsys.CurrentUser(), filepath.Dir(exe)), "\n", "\r\n")
	data := []byte{0xFF, 0xFE}
	for _, r := range text {
		data = append(data, byte(r), byte(r>>8)) // ASCII and BMP paths only
	}
	if err := os.WriteFile(xmlFile, data, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("schtasks", "/Create", "/TN", name, "/XML", xmlFile, "/F").CombinedOutput()
	t.Cleanup(func() { exec.Command("schtasks", "/Delete", "/TN", name, "/F").Run() })
	if err != nil {
		t.Fatal(string(out), err)
	}
	if got := owner(sid); got != "ours" {
		t.Fatalf("this account's task: %q", got)
	}
	if got := owner("S-1-5-21-1-2-3-1001"); got != "other" {
		t.Fatalf("another account's task: %q", got)
	}
}
