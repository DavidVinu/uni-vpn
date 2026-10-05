package daemon

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"syscall"
)

// Python words OS errors its own way; messages that reach the page or the log keep that wording.

func strerror(errno syscall.Errno) string {
	s := strings.TrimSuffix(errno.Error(), ".")
	if s != "" {
		s = strings.ToUpper(s[:1]) + s[1:]
	}
	return s
}

func errnoPrefix(errno syscall.Errno) string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf("[WinError %d] %s", int(errno), strerror(errno))
	}
	return fmt.Sprintf("[Errno %d] %s", int(errno), strerror(errno))
}

// pyErr is str(OSError) for errors that carry an errno, else err.Error().
func pyErr(err error) string {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return err.Error()
	}
	var pe *os.PathError
	if errors.As(err, &pe) {
		return fmt.Sprintf("%s: '%s'", errnoPrefix(errno), pe.Path)
	}
	var le *os.LinkError
	if errors.As(err, &le) {
		return fmt.Sprintf("%s: '%s' -> '%s'", errnoPrefix(errno), le.Old, le.New)
	}
	return errnoPrefix(errno)
}

// bindErrorString is str(exc) of asyncio.start_server failing on 127.0.0.1:port.
func bindErrorString(err error, port int) string {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return err.Error()
	}
	return fmt.Sprintf("[Errno %d] error while attempting to bind on address ('127.0.0.1', %d): %s",
		int(errno), port, strings.ToLower(strerror(errno)))
}
