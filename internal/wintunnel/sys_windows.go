//go:build windows

package wintunnel

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/DavidVinu/uni-vpn/internal/tunnel"
)

const createNewConsole = 0x00000010

// prepare gives openconnect a console of its own (hidden) so Ctrl+C can reach it for a clean
// logout.
func prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewConsole, HideWindow: true}
}

// systemDirs returns System32 and the Windows folder from the API, not from variables the
// user can set.
func systemDirs() (string, string) {
	system, _ := windows.GetSystemDirectory()
	win, _ := windows.GetSystemWindowsDirectory()
	return system, win
}

var (
	kernel32                = windows.NewLazySystemDLL("kernel32.dll")
	procWideCharToMultiByte = kernel32.NewProc("WideCharToMultiByte")
	procFreeConsole         = kernel32.NewProc("FreeConsole")
	procAttachConsole       = kernel32.NewProc("AttachConsole")
	procSetConsoleCtrlHndlr = kernel32.NewProc("SetConsoleCtrlHandler")
)

const (
	wcNoBestFitChars = 0x00000400
	cpUTF8           = 65001
)

// encodePassword: openconnect reads stdin as text in the ANSI code page (setlocale(LC_ALL, "")
// and fgetws), so UTF-8 bytes would turn "ä" into "Ã¤" and fail the login.
func encodePassword(password []byte) ([]byte, error) {
	return ansiEncode(windows.GetACP(), password)
}

func ansiEncode(cp uint32, password []byte) ([]byte, error) {
	fail := &tunnel.PasswordEncodingError{Msg: passwordError(fmt.Sprintf("cp%d", cp))}
	if !utf8.Valid(password) {
		return nil, fail
	}
	if len(password) == 0 || cp == cpUTF8 {
		return append([]byte{}, password...), nil
	}
	wide := utf16.Encode([]rune(string(password)))
	var used int32
	call := func(out *byte, n int32) int32 {
		r, _, _ := procWideCharToMultiByte.Call(uintptr(cp), wcNoBestFitChars,
			uintptr(unsafe.Pointer(&wide[0])), uintptr(len(wide)),
			uintptr(unsafe.Pointer(out)), uintptr(n), 0, uintptr(unsafe.Pointer(&used)))
		return int32(r)
	}
	n := call(nil, 0)
	if n <= 0 || used != 0 {
		return nil, fail
	}
	out := make([]byte, n)
	if call(&out[0], n) != n || used != 0 {
		return nil, fail
	}
	return out, nil
}

// defaultCtrlC runs this program as the helper (see CtrlCHelperArg), like the Python core runs
// a Python helper.
func defaultCtrlC(pid int) bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	return SendCtrlCVia([]string{exe, CtrlCHelperArg}, pid, 5*time.Second)
}

// CtrlCHelperMain attaches to the console of pid and sends Ctrl+C to it. It returns the exit
// code: 0 sent, 1 attach failed, 2 sending failed. It must run in a process of its own.
func CtrlCHelperMain(pid int) int {
	procFreeConsole.Call()
	if r, _, _ := procAttachConsole.Call(uintptr(pid)); r == 0 {
		return 1
	}
	procSetConsoleCtrlHndlr.Call(0, 1)
	if err := windows.GenerateConsoleCtrlEvent(windows.CTRL_C_EVENT, 0); err != nil {
		return 2
	}
	return 0
}
