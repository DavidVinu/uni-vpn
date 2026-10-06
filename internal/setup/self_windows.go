package setup

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// moveAside moves the running program out of the folder that is about to be deleted: Windows
// cannot delete a running .exe, but it can rename it. The moved copy goes at the next restart.
func moveAside(exe string) {
	target := filepath.Join(os.TempDir(), fmt.Sprintf("uni-vpn-core-%d.old", os.Getpid()))
	if os.Rename(exe, target) != nil {
		return
	}
	from, err := windows.UTF16PtrFromString(target)
	if err == nil {
		windows.MoveFileEx(from, nil, windows.MOVEFILE_DELAY_UNTIL_REBOOT)
	}
}
