//go:build windows

package winsys

import (
	"errors"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/DavidVinu/uni-vpn/internal/platform"
)

var (
	kernel32                       = windows.NewLazySystemDLL("kernel32.dll")
	procSetConsoleCtrlHandler      = kernel32.NewProc("SetConsoleCtrlHandler")
	procAttachConsole              = kernel32.NewProc("AttachConsole")
	procFreeConsole                = kernel32.NewProc("FreeConsole")
	procQueryUnbiasedInterruptTime = kernel32.NewProc("QueryUnbiasedInterruptTime")
	procInternetSetOptionW         = windows.NewLazySystemDLL("wininet.dll").NewProc("InternetSetOptionW")
	procIsUserAnAdmin              = windows.NewLazySystemDLL("shell32.dll").NewProc("IsUserAnAdmin")
	procSendMessageTimeoutW        = windows.NewLazySystemDLL("user32.dll").NewProc("SendMessageTimeoutW")
)

const (
	internetOptionSettingsChanged = 39
	internetOptionRefresh         = 37
)

// --- proxy rule -----------------------------------------------------------

// ProxyGet returns the AutoConfigURL of the current user, "" when unset.
func ProxyGet() (string, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, InternetSettings, registry.QUERY_VALUE)
	if err != nil {
		return "", err
	}
	defer key.Close()
	url, _, err := key.GetStringValue("AutoConfigURL")
	if errors.Is(err, registry.ErrUnexpectedType) {
		// Python's str() of a non-string value.
		n, _, ierr := key.GetIntegerValue("AutoConfigURL")
		if ierr != nil || n == 0 {
			return "", nil
		}
		return strconv.FormatUint(n, 10), nil
	}
	if errors.Is(err, registry.ErrNotExist) {
		return "", nil
	}
	return url, err
}

// ProxySet writes AutoConfigURL ("" removes it) and tells WinINet users to re-read it.
func ProxySet(url string) error {
	key, err := registry.OpenKey(registry.CURRENT_USER, InternetSettings, registry.SET_VALUE)
	if err != nil {
		return err
	}
	if url != "" {
		err = key.SetStringValue("AutoConfigURL", url)
	} else if err = key.DeleteValue("AutoConfigURL"); errors.Is(err, registry.ErrNotExist) {
		err = nil
	}
	key.Close()
	if err != nil {
		return err
	}
	// Tell WinINet users (Edge, Chrome, Firefox with system settings) to re-read the settings.
	procInternetSetOptionW.Call(0, internetOptionSettingsChanged, 0, 0)
	procInternetSetOptionW.Call(0, internetOptionRefresh, 0, 0)
	return nil
}

// --- processes ------------------------------------------------------------

// SystemDirs returns System32 and the Windows folder from the API, not from variables the user can set.
func SystemDirs() (system, windowsDir string, err error) {
	if system, err = windows.GetSystemDirectory(); err != nil {
		return "", "", err
	}
	windowsDir, err = windows.GetSystemWindowsDirectory()
	return system, windowsDir, err
}

// AwakeSeconds is the time the machine was awake. The monotonic clock keeps counting through
// sleep on Windows, so the daemon's resume detection uses this instead.
func AwakeSeconds() float64 {
	var value uint64
	procQueryUnbiasedInterruptTime.Call(uintptr(unsafe.Pointer(&value)))
	return float64(value) / 1e7 // 100 ns units
}

func IsAdmin() bool {
	if procIsUserAnAdmin.Find() != nil {
		return false
	}
	r, _, _ := procIsUserAnAdmin.Call()
	return r != 0
}

// AllowCtrlCForChildren undoes a disabled Ctrl+C: Task Scheduler may start us that way, and
// openconnect would inherit it; then the clean logout by Ctrl+C never arrives and every stop
// becomes a kill.
func AllowCtrlCForChildren() {
	procSetConsoleCtrlHandler.Call(0, 0)
}

var (
	jobMu     sync.Mutex
	jobHandle windows.Handle // never closed: the OS closes it when we exit
)

// KillChildrenWithUs puts this process in a job that kills every child when the last handle
// closes, so a killed daemon (Task Scheduler "End") never leaves openconnect running.
func KillChildrenWithUs() bool {
	jobMu.Lock()
	defer jobMu.Unlock()
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return false
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return false
	}
	if err := windows.AssignProcessToJobObject(job, windows.CurrentProcess()); err != nil {
		windows.CloseHandle(job)
		return false
	}
	jobHandle = job
	return true
}

// CtrlCHelper is the body of the helper process SendCtrlC starts: attach to the console of pid
// and raise Ctrl+C there. Returns the exit code (0 sent, 1 attach failed, 2 event failed).
func CtrlCHelper(pid int) int {
	procFreeConsole.Call()
	if r, _, _ := procAttachConsole.Call(uintptr(uint32(pid))); r == 0 {
		return 1
	}
	procSetConsoleCtrlHandler.Call(0, 1)
	if windows.GenerateConsoleCtrlEvent(windows.CTRL_C_EVENT, 0) != nil {
		return 2
	}
	return 0
}

// SendCtrlC sends Ctrl+C to a process with its own console: openconnect then logs out
// cleanly. A helper process does it, because attaching to another console would detach the
// daemon from its own. helper is the command that ends in CtrlCHelper (the pid is appended),
// e.g. the uni-vpn binary with a hidden subcommand.
func SendCtrlC(pid int, timeout time.Duration, helper ...string) bool {
	if len(helper) == 0 {
		return false
	}
	cmd := exec.Command(helper[0], append(helper[1:], strconv.Itoa(pid))...)
	platform.HideWindow(cmd)
	if err := cmd.Start(); err != nil {
		return false
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err == nil
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		<-done
		return false
	}
}

// HiddenNewConsole gives cmd its own console window, hidden, so SendCtrlC can reach it.
func HiddenNewConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: CreateNewConsole, HideWindow: true}
}

// --- user PATH -------------------------------------------------------------

func broadcastEnvironment() {
	name, _ := windows.UTF16PtrFromString("Environment")
	var result uintptr
	// HWND_BROADCAST, WM_SETTINGCHANGE, SMTO_ABORTIFHUNG
	procSendMessageTimeoutW.Call(0xFFFF, 0x001A, 0, uintptr(unsafe.Pointer(name)), 0x0002, 5000,
		uintptr(unsafe.Pointer(&result)))
}

func openEnvironment() (registry.Key, error) {
	return registry.OpenKey(registry.CURRENT_USER, "Environment", registry.QUERY_VALUE|registry.SET_VALUE)
}

func setPath(key registry.Key, kind uint32, value string) error {
	if kind == registry.EXPAND_SZ {
		return key.SetExpandStringValue("Path", value)
	}
	return key.SetStringValue("Path", value)
}

// AddUserPath appends a directory to the user's PATH (new terminals see it). True if it was added.
func AddUserPath(directory string) (bool, error) {
	key, err := openEnvironment()
	if err != nil {
		return false, err
	}
	value, kind, err := key.GetStringValue("Path")
	if errors.Is(err, registry.ErrNotExist) {
		value, kind, err = "", registry.EXPAND_SZ, nil
	}
	if err != nil {
		key.Close()
		return false, err
	}
	entries := pathEntries(value)
	for _, e := range entries {
		if normcase(e) == normcase(directory) {
			key.Close()
			return false, nil
		}
	}
	err = setPath(key, kind, joinPath(append(entries, directory)))
	key.Close()
	if err != nil {
		return false, err
	}
	broadcastEnvironment()
	return true, nil
}

func RemoveUserPath(directory string) error {
	key, err := openEnvironment()
	if err != nil {
		return err
	}
	value, kind, err := key.GetStringValue("Path")
	if errors.Is(err, registry.ErrNotExist) {
		key.Close()
		return nil
	}
	if err != nil {
		key.Close()
		return err
	}
	target := normcase(directory)
	var keep []string
	for _, e := range pathEntries(value) {
		if normcase(e) != target {
			keep = append(keep, e)
		}
	}
	err = setPath(key, kind, joinPath(keep))
	key.Close()
	if err != nil {
		return err
	}
	broadcastEnvironment()
	return nil
}
