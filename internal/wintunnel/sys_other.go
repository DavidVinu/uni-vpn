//go:build !windows

package wintunnel

import (
	"os/exec"
	"unicode/utf8"

	"github.com/DavidVinu/uni-vpn/internal/tunnel"
)

// Elsewhere the tunnel only runs in tests: started like on POSIX (new session).
var prepare func(*exec.Cmd)

func systemDirs() (string, string) { return "", "" }

// encodePassword: outside Windows the locale encoding is UTF-8.
func encodePassword(password []byte) ([]byte, error) {
	if !utf8.Valid(password) {
		return nil, &tunnel.PasswordEncodingError{Msg: passwordError("utf-8")}
	}
	return password, nil
}

// Without Windows consoles there is no Ctrl+C to send; the caller then kills.
func defaultCtrlC(int) bool { return false }

// CtrlCHelperMain only works on Windows.
func CtrlCHelperMain(int) int { return 1 }
