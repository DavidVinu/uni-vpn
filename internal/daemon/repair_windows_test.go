package daemon

import (
	"testing"

	"golang.org/x/sys/windows"
)

// install.ps1 shows its progress; this program's own setup runs without a console window.
func TestRepairWindow(t *testing.T) {
	if repairWindow(false) != windows.CREATE_NEW_CONSOLE || repairWindow(true) != windows.CREATE_NO_WINDOW {
		t.Fatal(repairWindow(false), repairWindow(true))
	}
}
