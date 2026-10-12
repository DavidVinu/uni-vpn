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

// Read is os.ReadFile, waiting out a Write on Windows.
func Read(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	for try := 0; err != nil && runtime.GOOS == "windows" && !errors.Is(err, fs.ErrNotExist) && try < tries; try++ {
		time.Sleep(pause)
		data, err = os.ReadFile(path)
	}
	return data, err
}
