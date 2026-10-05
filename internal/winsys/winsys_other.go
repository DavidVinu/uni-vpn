//go:build !windows

package winsys

import (
	"os/exec"
	"time"
)

func ProxyGet() (string, error)                                       { return "", ErrUnsupported }
func ProxySet(url string) error                                       { return ErrUnsupported }
func SystemDirs() (system, windowsDir string, err error)              { return "", "", ErrUnsupported }
func AwakeSeconds() float64                                           { return 0 }
func IsAdmin() bool                                                   { return false }
func AllowCtrlCForChildren()                                          {}
func KillChildrenWithUs() bool                                        { return false }
func CtrlCHelper(pid int) int                                         { return 1 }
func SendCtrlC(pid int, timeout time.Duration, helper ...string) bool { return false }
func HiddenNewConsole(cmd *exec.Cmd)                                  {}
func AddUserPath(directory string) (bool, error)                      { return false, ErrUnsupported }
func RemoveUserPath(directory string) error                           { return ErrUnsupported }
