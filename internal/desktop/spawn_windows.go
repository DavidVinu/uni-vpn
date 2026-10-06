package desktop

import (
	"os/exec"

	"golang.org/x/sys/windows"
)

// Spawn starts a program and leaves it running.
func Spawn(args []string) bool {
	cmd := exec.Command(args[0], args[1:]...)
	if err := cmd.Start(); err != nil {
		return false
	}
	go cmd.Wait()
	return true
}

// OpenURL opens a page in the default browser, like os.startfile.
func OpenURL(link string) bool {
	verb, _ := windows.UTF16PtrFromString("open")
	file, err := windows.UTF16PtrFromString(link)
	if err != nil {
		return false
	}
	return windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL) == nil
}
