package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Only a Write's own temporary file means a Write is under way, not a file that merely shares
// the name (sync.json.pending next to sync.json, a backup).
func TestWritingSeesOnlyAWritesTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "domains.txt")
	if writing(path) {
		t.Fatal("nothing there")
	}
	for _, name := range []string{"domains.txt.pending", "domains.txt.", "domains.txt.12a", "other.txt.123"} {
		os.WriteFile(filepath.Join(dir, name), nil, 0o600)
	}
	if writing(path) {
		t.Fatal("no temporary file of a Write there")
	}
	os.WriteFile(filepath.Join(dir, "domains.txt.3141592653"), nil, 0o600)
	if !writing(path) {
		t.Fatal("a Write's temporary file is there")
	}
}

// A temporary file left by a crash or a failed Remove is no Write under way: a missing file
// must not wait a second at every read from then on.
func TestWritingIgnoresAnOldTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "domains.txt")
	tmp := filepath.Join(dir, "domains.txt.3141592653")
	os.WriteFile(tmp, nil, 0o600)
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(tmp, old, old); err != nil {
		t.Fatal(err)
	}
	if writing(path) {
		t.Fatal("an old temporary file counts as a Write under way")
	}
}

func TestReadAfterWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if _, err := Read(path); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := Write(path, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if data, err := Read(path); err != nil || string(data) != "x" {
		t.Fatal(string(data), err)
	}
	if writing(path) {
		t.Fatal("Write left its temporary file")
	}
}
