// Package atomicfile replaces small files in one step, so that a reader (the status page, the
// browsers' proxy rule) never finds one empty or half written, and nor does the next start
// after a crash.
package atomicfile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Windows refuses to swap a file someone has open (the status page, a virus scanner), and to
// open one while it is being swapped: both wait up to a second.
const (
	tries = 50
	pause = 20 * time.Millisecond
)

// Write replaces path with data, keeping its mode (0600 for a new file).
func Write(path string, data []byte) error {
	mode := fs.FileMode(0o600)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), mode)
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
		for try := 0; err != nil && runtime.GOOS == "windows" && try < tries; try++ {
			time.Sleep(pause)
			err = os.Rename(tmp.Name(), path)
		}
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}

// Read is os.ReadFile, waiting out a Write on Windows. There a file being replaced can also
// read as missing for a moment (Go's cmd/go robustio knows it too): that waits while a Write's
// temporary file is there, so a missing file costs one pause only.
func Read(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	for try := 0; err != nil && runtime.GOOS == "windows" && try < tries; try++ {
		if errors.Is(err, fs.ErrNotExist) && try > 0 && !writing(path) {
			break
		}
		time.Sleep(pause)
		data, err = os.ReadFile(path)
	}
	return data, err
}

// writing: a Write's temporary file (os.CreateTemp: the name, a dot, digits) is next to path.
func writing(path string) bool {
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		return false
	}
	prefix := filepath.Base(path) + "."
	for _, e := range entries {
		rest, ok := strings.CutPrefix(e.Name(), prefix)
		if ok && rest != "" && strings.Trim(rest, "0123456789") == "" {
			return true
		}
	}
	return false
}
