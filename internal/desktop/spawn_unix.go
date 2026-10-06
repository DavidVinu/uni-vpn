//go:build !windows

package desktop

import (
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"time"
)

// Spawn starts a program in a session of its own, with no terminal attached.
func Spawn(args []string) bool {
	cmd := exec.Command(args[0], args[1:]...) // stdin, stdout and stderr stay nil: /dev/null
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return false
	}
	go cmd.Wait()
	return true
}

// OpenURL opens a page in the default browser. False when there is no desktop to show it on.
func OpenURL(link string) bool {
	if !HasDesktop(runtime.GOOS, os.Getenv) {
		return false
	}
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}
	// xdg-open can stay in the foreground when it starts a new browser: no pipes the browser
	// could inherit, and a launcher that is still running counts as success.
	cmd := exec.Command(opener, link)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return false
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err == nil
	case <-time.After(3 * time.Second):
		return true
	}
}
