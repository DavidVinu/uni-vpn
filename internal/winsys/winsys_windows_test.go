//go:build windows

package winsys

// Checks against the real Windows APIs (CI: windows-latest).

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The test binary doubles as the processes the Ctrl+C tests need.
func TestMain(m *testing.M) {
	if len(os.Args) >= 3 && os.Args[1] == "__ctrl-c" {
		pid, _ := strconv.Atoi(os.Args[2])
		os.Exit(CtrlCHelper(pid))
	}
	if len(os.Args) >= 2 && os.Args[1] == "__sleeper" {
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt)
		select {
		case <-c:
			os.Exit(7)
		case <-time.After(30 * time.Second):
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}

func TestProxyRoundtrip(t *testing.T) {
	before, err := ProxyGet()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ProxySet(before) })
	if err := ProxySet("http://127.0.0.1:1081/proxy.pac"); err != nil {
		t.Fatal(err)
	}
	if got, _ := ProxyGet(); got != "http://127.0.0.1:1081/proxy.pac" {
		t.Fatal(got)
	}
	if err := ProxySet(""); err != nil {
		t.Fatal(err)
	}
	if got, _ := ProxyGet(); got != "" {
		t.Fatal(got)
	}
	if err := ProxySet(""); err != nil { // removing a missing value is fine
		t.Fatal(err)
	}
}

func TestUserPathRoundtrip(t *testing.T) {
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("uni-vpn-test-%d", time.Now().UnixNano()))
	t.Cleanup(func() { RemoveUserPath(dir) })
	if added, err := AddUserPath(dir); !added || err != nil {
		t.Fatal(added, err)
	}
	if added, _ := AddUserPath(dir + `\`); added {
		t.Fatal("added twice")
	}
	if err := RemoveUserPath(dir); err != nil {
		t.Fatal(err)
	}
	if added, _ := AddUserPath(dir); !added {
		t.Fatal("not added after removal")
	}
}

func TestSystemDirsComeFromTheAPI(t *testing.T) {
	system, windowsDir, err := SystemDirs()
	if err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(filepath.Join(system, "netsh.exe")); err != nil || !st.Mode().IsRegular() {
		t.Fatal(system, err)
	}
	if st, err := os.Stat(windowsDir); err != nil || !st.IsDir() {
		t.Fatal(windowsDir, err)
	}
}

func ctrlCReachesAProcessWithItsOwnConsole(t *testing.T) {
	cmd := exec.Command(os.Args[0], "__sleeper")
	HiddenNewConsole(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill() })
	time.Sleep(time.Second) // until the child has installed its Ctrl+C handler
	if !SendCtrlC(cmd.Process.Pid, 5*time.Second, os.Args[0], "__ctrl-c") {
		t.Fatal("SendCtrlC failed")
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
		if code := cmd.ProcessState.ExitCode(); code != 7 {
			t.Fatal("exit code", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("child did not exit")
	}
}

func TestCtrlCReachesAProcessWithItsOwnConsole(t *testing.T) {
	ctrlCReachesAProcessWithItsOwnConsole(t)
}

func TestCtrlCStillReachesChildrenAfterAllowingIt(t *testing.T) {
	AllowCtrlCForChildren()
	ctrlCReachesAProcessWithItsOwnConsole(t)
}

func TestAwakeClockAdvances(t *testing.T) {
	first := AwakeSeconds()
	time.Sleep(200 * time.Millisecond)
	if AwakeSeconds()-first <= 0.1 {
		t.Fatal("awake clock did not advance")
	}
}

func TestTaskXMLIsAcceptedByTaskScheduler(t *testing.T) {
	if !IsAdmin() {
		t.Skip("creating a task with the highest run level needs administrator rights")
	}
	name := fmt.Sprintf("uni-vpn-test-%08x", time.Now().UnixNano()&0xffffffff)
	exe, _ := os.Executable()
	xmlFile := filepath.Join(t.TempDir(), "task.xml")
	text := strings.ReplaceAll(RenderTaskBinary(Conhost(), exe, CurrentUser(), filepath.Dir(exe)), "\n", "\r\n")
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
	query, _ := exec.Command("schtasks", "/Query", "/TN", name, "/XML").Output()
	if !strings.Contains(string(query), "HighestAvailable") {
		t.Fatal(string(query))
	}
}

func TestKillChildrenWithUs(t *testing.T) {
	// In a fresh test process the job is new; a second call nests another job (Windows 8+).
	if !KillChildrenWithUs() {
		t.Fatal("job object not set up")
	}
}
