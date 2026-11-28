//go:build windows

package platform

import (
	"syscall"
	"unsafe"
)

// packaged reports whether this process runs from an MSIX package (the Microsoft Store
// version): it then has a package identity.
func packaged() bool {
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GetCurrentPackageFullName")
	if proc.Find() != nil {
		return false
	}
	var n uint32
	r, _, _ := proc.Call(uintptr(unsafe.Pointer(&n)), 0)
	const errorInsufficientBuffer = 122 // APPMODEL_ERROR_NO_PACKAGE (15700) without one
	return r == errorInsufficientBuffer
}
