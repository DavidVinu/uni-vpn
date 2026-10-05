package logsetup

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

var lineRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} (DEBUG|INFO|WARNING|ERROR|CRITICAL) `)

func newLogger(t *testing.T, path string, level slog.Level) (*slog.Logger, *Tail, *bytes.Buffer) {
	t.Helper()
	var stderr bytes.Buffer
	h, tail, err := New(path, level, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	return slog.New(h), tail, &stderr
}

func TestFormatTailAndStderr(t *testing.T) {
	log, tail, stderr := newLogger(t, "", slog.LevelInfo)
	log.Info("connected")
	log.Warn("slow")
	log.Error("broken")
	log.Log(context.Background(), LevelCritical, "dead")
	log.Debug("hidden")
	lines := tail.Lines()
	if len(lines) != 4 {
		t.Fatalf("lines %q", lines)
	}
	for i, want := range []string{"INFO connected", "WARNING slow", "ERROR broken", "CRITICAL dead"} {
		if !lineRe.MatchString(lines[i]) || !strings.HasSuffix(lines[i], want) {
			t.Fatalf("line %q", lines[i])
		}
	}
	if stderr.String() != strings.Join(lines, "\n")+"\n" {
		t.Fatalf("stderr %q", stderr.String())
	}
}

func TestAttrsAreAppended(t *testing.T) {
	log, tail, _ := newLogger(t, "", slog.LevelInfo)
	log.With("a", 1).WithGroup("g").Info("msg", "b", "x")
	if got := tail.Lines()[0]; !strings.HasSuffix(got, " INFO msg a=1 g.b=x") {
		t.Fatalf("got %q", got)
	}
}

func TestTailKeepsLast50(t *testing.T) {
	log, tail, _ := newLogger(t, "", slog.LevelInfo)
	for i := range 60 {
		log.Info(fmt.Sprint("n", i))
	}
	lines := tail.Lines()
	if len(lines) != 50 || !strings.HasSuffix(lines[0], " n10") || !strings.HasSuffix(lines[49], " n59") {
		t.Fatalf("lines %d %q .. %q", len(lines), lines[0], lines[len(lines)-1])
	}
}

func TestFileIsPrivateAndDirCreated(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "uni-vpn")
	path := filepath.Join(dir, "daemon.log")
	log, _, _ := newLogger(t, path, slog.LevelInfo)
	log.Info("hello")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !lineRe.Match(data) || !strings.HasSuffix(string(data), "INFO hello"+newline) {
		t.Fatalf("file %q", data)
	}
	if runtime.GOOS == "windows" {
		return
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %v", st.Mode().Perm())
	}
	if st, _ := os.Stat(dir); st.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", st.Mode().Perm())
	}
}

func TestExistingFileGetsChmod(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes")
	}
	path := filepath.Join(t.TempDir(), "daemon.log")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	newLogger(t, path, slog.LevelInfo)
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}
	if data, _ := os.ReadFile(path); string(data) != "old\n" {
		t.Fatalf("appended file was changed: %q", data)
	}
}

func TestRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	log, _, _ := newLogger(t, path, slog.LevelInfo)
	msg := strings.Repeat("x", 100_000)
	for range 50 {
		log.Info(msg)
	}
	for _, p := range []string{path, path + ".1", path + ".2", path + ".3"} {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if st.Size() >= MaxBytes {
			t.Fatalf("%s has %d bytes", p, st.Size())
		}
		if runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %v", p, st.Mode().Perm())
		}
	}
	if _, err := os.Stat(path + ".4"); err == nil {
		t.Fatal("more than 3 backups")
	}
}
