// Package logsetup: rotating file (0600), log tail for the status page, stderr for
// journal/launchd. It mirrors uni_vpn/logsetup.py: same line format, same rotation
// (1,000,000 bytes, 3 backups: daemon.log.1 .. daemon.log.3).
package logsetup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	MaxBytes    = 1_000_000
	BackupCount = 3
	TailLines   = 50
)

// LevelCritical is Python's CRITICAL; slog has no name for it.
const LevelCritical = slog.Level(12)

// Python's text mode file writes CRLF on Windows.
var newline = func() string {
	if runtime.GOOS == "windows" {
		return "\r\n"
	}
	return "\n"
}()

// Tail keeps the last formatted lines for the status page.
type Tail struct {
	mu    sync.Mutex
	lines []string
	max   int
}

func newTail(max int) *Tail { return &Tail{max: max} }

func (t *Tail) add(line string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines = append(t.lines, line)
	if len(t.lines) > t.max {
		t.lines = append(t.lines[:0:0], t.lines[len(t.lines)-t.max:]...)
	}
}

// Lines returns a copy, oldest first.
func (t *Tail) Lines() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.lines...)
}

// rotatingFile is RotatingFileHandler with the 0600 open of PrivateRotatingFileHandler.
type rotatingFile struct {
	path string
	f    *os.File
}

// open creates the log file with mode 0600, also after every rotation and
// regardless of the umask.
func (r *rotatingFile) open() error {
	f, err := os.OpenFile(r.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	r.f = f
	return nil
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func (r *rotatingFile) shouldRollover(msg string) bool {
	// Never roll over anything other than regular files (bpo-45401).
	if st, err := os.Stat(r.path); err == nil && !st.Mode().IsRegular() {
		return false
	}
	if r.f == nil {
		if r.open() != nil {
			return false
		}
	}
	pos, err := r.f.Seek(0, io.SeekEnd)
	if err != nil {
		return false
	}
	// Python compares the byte position with the length in characters.
	return pos+int64(utf8.RuneCountInString(msg)) >= MaxBytes
}

func (r *rotatingFile) rollover() error {
	if r.f != nil {
		r.f.Close()
		r.f = nil
	}
	for i := BackupCount - 1; i > 0; i-- {
		src, dst := fmt.Sprintf("%s.%d", r.path, i), fmt.Sprintf("%s.%d", r.path, i+1)
		if exists(src) {
			if exists(dst) {
				if err := os.Remove(dst); err != nil {
					return err
				}
			}
			if err := os.Rename(src, dst); err != nil {
				return err
			}
		}
	}
	dst := r.path + ".1"
	if exists(dst) {
		if err := os.Remove(dst); err != nil {
			return err
		}
	}
	if exists(r.path) {
		if err := os.Rename(r.path, dst); err != nil {
			return err
		}
	}
	return r.open()
}

func (r *rotatingFile) write(line string) error {
	msg := line + "\n"
	if r.shouldRollover(msg) {
		if err := r.rollover(); err != nil {
			return err
		}
	}
	if r.f == nil {
		if err := r.open(); err != nil {
			return err
		}
	}
	_, err := r.f.WriteString(line + newline)
	return err
}

// core is shared by a Handler and its WithAttrs/WithGroup children.
type core struct {
	mu     sync.Mutex
	level  slog.Leveler
	tail   *Tail
	file   *rotatingFile
	stderr io.Writer
}

// Handler writes "2006-01-02 15:04:05 LEVEL message" to the tail, the file and stderr.
type Handler struct {
	c      *core
	attrs  string // preformatted " key=value" pairs
	prefix string // group prefix "a.b."
}

func levelName(l slog.Level) string {
	switch l {
	case slog.LevelDebug:
		return "DEBUG"
	case slog.LevelInfo:
		return "INFO"
	case slog.LevelWarn:
		return "WARNING"
	case slog.LevelError:
		return "ERROR"
	case LevelCritical:
		return "CRITICAL"
	}
	return fmt.Sprintf("Level %d", int(l))
}

func (h *Handler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.c.level.Level() }

func appendAttr(b *strings.Builder, prefix string, a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}
	if a.Value.Kind() == slog.KindGroup {
		p := prefix
		if a.Key != "" {
			p += a.Key + "."
		}
		for _, g := range a.Value.Group() {
			appendAttr(b, p, g)
		}
		return
	}
	fmt.Fprintf(b, " %s%s=%v", prefix, a.Key, a.Value.Any())
}

// Format renders one record. Attributes are a Go addition: Python messages carry none.
func (h *Handler) format(r slog.Record) string {
	t := r.Time
	if t.IsZero() {
		t = time.Now()
	}
	var b strings.Builder
	b.WriteString(t.Format("2006-01-02 15:04:05"))
	b.WriteByte(' ')
	b.WriteString(levelName(r.Level))
	b.WriteByte(' ')
	b.WriteString(r.Message)
	b.WriteString(h.attrs)
	r.Attrs(func(a slog.Attr) bool {
		appendAttr(&b, h.prefix, a)
		return true
	})
	return b.String()
}

func (h *Handler) Handle(_ context.Context, r slog.Record) error {
	line := h.format(r)
	h.c.mu.Lock()
	defer h.c.mu.Unlock()
	h.c.tail.add(line)
	var errs []error
	if h.c.file != nil {
		// Like logging.Handler.handleError: report, keep logging.
		if err := h.c.file.write(line); err != nil {
			fmt.Fprintf(h.c.stderr, "--- Logging error ---\n%v\n", err)
			errs = append(errs, err)
		}
	}
	if _, err := io.WriteString(h.c.stderr, line+"\n"); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	var b strings.Builder
	for _, a := range attrs {
		appendAttr(&b, h.prefix, a)
	}
	return &Handler{c: h.c, attrs: h.attrs + b.String(), prefix: h.prefix}
}

func (h *Handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return &Handler{c: h.c, attrs: h.attrs, prefix: h.prefix + name + "."}
}

// Close closes the log file.
func (h *Handler) Close() error {
	h.c.mu.Lock()
	defer h.c.mu.Unlock()
	if h.c.file == nil || h.c.file.f == nil {
		return nil
	}
	err := h.c.file.f.Close()
	h.c.file.f = nil
	return err
}

// mkdirParents mirrors Path.mkdir(parents=True, exist_ok=True, mode=0o700): only the
// last directory gets 0700, created parents get the default mode.
func mkdirParents(dir string) error {
	if st, err := os.Stat(dir); err == nil && st.IsDir() {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o777); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) {
		return err
	}
	return nil
}

// New builds the handler. path "" means no log file.
func New(path string, level slog.Leveler, stderr io.Writer) (*Handler, *Tail, error) {
	c := &core{level: level, tail: newTail(TailLines), stderr: stderr}
	if path != "" {
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, nil, err
		}
		if err := mkdirParents(filepath.Dir(abs)); err != nil {
			return nil, nil, err
		}
		c.file = &rotatingFile{path: abs}
		if err := c.file.open(); err != nil {
			return nil, nil, err
		}
		if err := os.Chmod(abs, 0o600); err != nil { // existing file from earlier versions
			c.file.f.Close()
			return nil, nil, err
		}
	}
	return &Handler{c: c}, c.tail, nil
}

// Setup is setup_logging: tail, optional rotating file (platform.LogFile()), stderr.
func Setup(path string, level slog.Level) (*slog.Logger, *Tail, error) {
	h, tail, err := New(path, level, os.Stderr)
	if err != nil {
		return nil, nil, err
	}
	return slog.New(h), tail, nil
}
