// Package tunnel runs openconnect: start, readiness, stop, output classification.
// It mirrors uni_vpn/tunnel.py; argv, stdin, messages and timeouts must stay identical.
package tunnel

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/DavidVinu/uni-vpn/internal/platform"
)

// Config holds what the tunnel reads from the daemon's configuration. The daemon fills it from
// config.Config: Host, UserAgent (useragent), LoginName (the login_name property: user plus
// username_suffix unless the user typed a realm), AuthGroup, UserGroup, OS, NoExternalAuth,
// MFA (mfa), TOTPSeparator (totp_separator) and StopGrace (stop_grace, used by wintunnel).
type Config struct {
	Host           string
	UserAgent      string
	LoginName      string
	AuthGroup      string
	UserGroup      string
	OS             string
	NoExternalAuth bool
	MFA            string
	TOTPSeparator  string
	StopGrace      time.Duration
}

// CodeFunc computes the six-digit TOTP code of a normalized token ("base32:...") at now.
// The daemon passes totp.Code.
type CodeFunc func(token string, now time.Time) (string, error)

// PasswordEncodingError: the password cannot be passed to openconnect on this system.
type PasswordEncodingError struct{ Msg string }

func (e *PasswordEncodingError) Error() string { return e.Msg }

// TOTPSecretError: the TOTP secret is missing or no code can be computed from it
// (totp_append). The daemon reports it as a final auth_failed with TOTPUnusable.
type TOTPSecretError struct{ Msg string }

func (e *TOTPSecretError) Error() string { return e.Msg }

// Hooks let the Windows tunnel replace parts of the POSIX behaviour. Nil means the default.
type Hooks struct {
	Command       func(port int) []string
	Env           func(env []string, port int) []string
	PasswordBytes func(password []byte) ([]byte, error)
	Prepare       func(cmd *exec.Cmd) // process attributes; default: new session
	AfterExit     func()              // runs after Exited() is closed
	KillWrapper   func()
}

// Tunnel is one openconnect run. It is used once: Start, WaitReady, Stop.
type Tunnel struct {
	Cfg         Config
	OpenConnect string
	Wrapper     string
	OCProxy     string
	Log         *slog.Logger
	TokenDir    string
	TOTPCode    CodeFunc
	Hooks       Hooks

	mu             sync.Mutex
	classifier     *Classifier
	tokenFile      string
	port           int
	cmd            *exec.Cmd
	exited         chan struct{}
	returnCode     *int
	stderrTail     []string
	classification *Verdict
	otpGeneratedAt time.Time // wall clock time when openconnect generated a code
	stoppedByUs    bool
	startedAt      time.Time
	readyAt        time.Time
}

const tailLen = 20

// New creates a tunnel. tokenDir "" means platform.StateDir().
func New(cfg Config, openconnect, wrapper string, log *slog.Logger, ocproxy, tokenDir string) *Tunnel {
	if tokenDir == "" {
		tokenDir = platform.StateDir()
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Tunnel{
		Cfg: cfg, OpenConnect: openconnect, Wrapper: wrapper, OCProxy: ocproxy, Log: log, TokenDir: tokenDir,
		classifier: NewClassifier(cfg.MFA),
		exited:     make(chan struct{}),
	}
}

func (t *Tunnel) logf(level slog.Level, format string, args ...any) {
	t.Log.Log(context.Background(), level, fmt.Sprintf(format, args...))
}

// AuthArgs are the login options from the university profile; the Windows tunnel uses the same.
func (t *Tunnel) AuthArgs() []string {
	cfg := t.Cfg
	args := []string{"--protocol=anyconnect", "--useragent=" + cfg.UserAgent, "--user=" + cfg.LoginName, "--passwd-on-stdin"}
	// With Duo the second password ("push") comes through openconnect's prompt, which
	// --non-inter refuses. stdin is closed after it, so a further prompt ends at EOF.
	if cfg.MFA != "duo_push" {
		args = append(args, "--non-inter")
	}
	if cfg.AuthGroup != "" {
		args = append(args, "--authgroup="+cfg.AuthGroup)
	}
	if cfg.UserGroup != "" {
		args = append(args, "--usergroup="+cfg.UserGroup)
	}
	if cfg.OS != "" {
		args = append(args, "--os="+cfg.OS)
	}
	if cfg.NoExternalAuth {
		args = append(args, "--no-external-auth")
	}
	return args
}

// TokenArgs: the secret is passed via a 0600 file, never via the process list. openconnect
// rereads it for every code it generates, so it stays until the tunnel is up.
func (t *Tunnel) TokenArgs() []string {
	t.mu.Lock()
	f := t.tokenFile
	t.mu.Unlock()
	if f != "" {
		return []string{"--token-mode=totp", "--token-secret=@" + f}
	}
	return nil
}

// Command is the openconnect argv for the given local proxy port.
func (t *Tunnel) Command(port int) []string {
	if t.Hooks.Command != nil {
		return t.Hooks.Command(port)
	}
	cmd := []string{t.OpenConnect}
	cmd = append(cmd, t.AuthArgs()...)
	cmd = append(cmd,
		"--no-dtls",
		"--force-dpd=30",
		"--reconnect-timeout=60",
		"--script-tun",
		// openconnect runs the value via /bin/sh -c, so the path may contain spaces.
		// "exec" so that dash does not leave an sh running next to ocproxy.
		fmt.Sprintf("--script=exec %s %d", ShellQuote(t.Wrapper), port),
	)
	// The openconnect from the .pkg was built against Homebrew's certificate file, which a Mac
	// without Homebrew does not have.
	if platform.IsMacOS && platform.IsBundled(t.OpenConnect) {
		if st, err := os.Stat(platform.MacOSCAFile); err == nil && st.Mode().IsRegular() {
			cmd = append(cmd, "--cafile="+platform.MacOSCAFile)
		}
	}
	cmd = append(cmd, t.TokenArgs()...)
	return append(cmd, t.Cfg.Host)
}

var shellUnsafe = regexp.MustCompile(`[^\w@%+=:,./-]`)

// ShellQuote quotes s for /bin/sh like Python's shlex.quote.
func ShellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if !shellUnsafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// mkdirParents behaves like Path.mkdir(parents=True, exist_ok=True, mode=mode): only the last
// directory gets mode, missing parents get the default.
func mkdirParents(dir string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o777); err != nil {
		return err
	}
	if err := os.Mkdir(dir, mode); err != nil {
		if st, serr := os.Stat(dir); serr == nil && st.IsDir() {
			return nil
		}
		return err
	}
	return nil
}

func (t *Tunnel) writeTokenFile(totp string) error {
	if err := mkdirParents(t.TokenDir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(t.TokenDir, TokenPrefix+"*") // created as 0600
	if err != nil {
		return err
	}
	_, werr := f.WriteString(totp + "\n")
	cerr := f.Close()
	t.mu.Lock()
	t.tokenFile = f.Name()
	t.mu.Unlock()
	if werr != nil {
		return werr
	}
	return cerr
}

// RemoveTokenFile deletes the TOTP secret file, if any.
func (t *Tunnel) RemoveTokenFile() {
	t.mu.Lock()
	f := t.tokenFile
	t.tokenFile = ""
	t.mu.Unlock()
	if f == "" {
		return
	}
	if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.logf(slog.LevelWarn, "Could not delete secret file %s: %v", f, err)
	}
}

// TokenFile is the current secret file path, "" if none.
func (t *Tunnel) TokenFile() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.tokenFile
}

func (t *Tunnel) env(port int) []string {
	env := os.Environ()
	if t.OCProxy != "" {
		env = SetEnv(env, "OCPROXY", t.OCProxy)
	}
	if t.Hooks.Env != nil {
		env = t.Hooks.Env(env, port)
	}
	return env
}

// SetEnv sets key in an os.Environ style list, replacing an existing entry.
func SetEnv(env []string, key, value string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		k := kv
		// Windows has entries like "=C:=C:\dir"; the key ends at the first "=" after the start.
		if i := strings.Index(kv[min(1, len(kv)):], "="); i >= 0 {
			k = kv[:i+min(1, len(kv))]
		}
		if k == key || (platform.IsWindows && strings.EqualFold(k, key)) {
			continue
		}
		out = append(out, kv)
	}
	return append(out, key+"="+value)
}

func (t *Tunnel) passwordBytes(password []byte) ([]byte, error) {
	if t.Hooks.PasswordBytes != nil {
		return t.Hooks.PasswordBytes(password)
	}
	return password, nil
}

// StdinBytes is what openconnect reads on stdin, by the profile's second factor.
func (t *Tunnel) StdinBytes(password []byte, totp string) ([]byte, error) {
	switch t.Cfg.MFA {
	case "totp_append":
		if totp == "" {
			return nil, &TOTPSecretError{"totp_append needs a TOTP secret"}
		}
		if t.TOTPCode == nil {
			return nil, errors.New("totp_append needs a TOTP code function")
		}
		now := time.Now()
		code, err := t.TOTPCode(totp, now)
		if err != nil {
			return nil, &TOTPSecretError{err.Error()}
		}
		t.mu.Lock()
		t.otpGeneratedAt = time.Now()
		t.mu.Unlock()
		joined := append(append(append([]byte{}, password...), t.Cfg.TOTPSeparator...), code...)
		data, err := t.passwordBytes(joined)
		if err != nil {
			return nil, err
		}
		return append(data, '\n'), nil
	}
	pw, err := t.passwordBytes(password)
	if err != nil {
		return nil, err
	}
	data := append(append([]byte{}, pw...), '\n')
	if t.Cfg.MFA == "duo_push" {
		data = append(data, "push\n"...)
	}
	return data, nil
}

// Start launches openconnect and feeds it the password (and Duo's "push") on stdin.
// totp is the normalized secret, "" for none.
func (t *Tunnel) Start(password []byte, totp string) error {
	data, err := t.StdinBytes(password, totp)
	if err != nil {
		return err
	}
	port, err := FreePort()
	if err != nil {
		return err
	}
	t.mu.Lock()
	t.port = port
	t.startedAt = time.Now()
	t.mu.Unlock()
	env := t.env(port)
	if totp != "" && t.Cfg.MFA == "totp_field" {
		if err := t.writeTokenFile(totp); err != nil {
			return err
		}
	}
	argv := t.Command(port)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.RemoveTokenFile()
		return err
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.RemoveTokenFile()
		return err
	}
	cmd.Stdout = w
	cmd.Stderr = w
	if t.Hooks.Prepare != nil {
		t.Hooks.Prepare(cmd)
	} else {
		newSession(cmd)
	}
	err = cmd.Start()
	w.Close()
	if err != nil {
		r.Close()
		t.RemoveTokenFile()
		return err
	}
	t.mu.Lock()
	t.cmd = cmd
	t.mu.Unlock()
	go t.readOutput(r, cmd)
	_, _ = stdin.Write(data) // a broken pipe means openconnect is already gone, the reader sees it
	_ = stdin.Close()
	t.mu.Lock()
	stopped := t.stoppedByUs
	t.mu.Unlock()
	if stopped { // Stop() came while the process was being started
		t.RemoveTokenFile()
		_ = cmd.Process.Kill()
	}
	return nil
}

// readLine returns the next line including its newline; bytes beyond limit are dropped.
func readLine(br *bufio.Reader, limit int) ([]byte, error) {
	var kept []byte
	for {
		part, err := br.ReadSlice('\n')
		if len(kept) < limit {
			kept = append(kept, part[:min(len(part), limit-len(kept))]...)
		}
		if err != bufio.ErrBufferFull {
			return kept, err
		}
	}
}

func decodeReplace(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var sb strings.Builder
	for len(b) > 0 {
		r, size := utf8.DecodeRune(b)
		if r == utf8.RuneError && size == 1 {
			sb.WriteRune(utf8.RuneError)
		} else {
			sb.Write(b[:size])
		}
		b = b[size:]
	}
	return sb.String()
}

func (t *Tunnel) readOutput(r io.ReadCloser, cmd *exec.Cmd) {
	br := bufio.NewReader(r)
	for {
		line, err := readLine(br, MaxLine)
		if len(line) > 0 {
			t.handleLine(strings.TrimRightFunc(decodeReplace(line), unicode.IsSpace))
		}
		if err != nil {
			break
		}
	}
	r.Close()
	_ = cmd.Wait()
	code := exitCode(cmd.ProcessState)
	t.mu.Lock()
	t.returnCode = &code
	t.mu.Unlock()
	t.RemoveTokenFile()
	t.logf(slog.LevelInfo, "openconnect exited with code %d", code)
	close(t.exited)
	if t.Hooks.AfterExit != nil {
		t.Hooks.AfterExit()
	}
}

func (t *Tunnel) handleLine(text string) {
	t.mu.Lock()
	t.stderrTail = append(t.stderrTail, text)
	if len(t.stderrTail) > tailLen {
		t.stderrTail = t.stderrTail[len(t.stderrTail)-tailLen:]
	}
	t.mu.Unlock()
	t.logf(slog.LevelInfo, "openconnect: %s", text)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.classification == nil {
		if v, ok := t.classifier.Feed(text); ok {
			t.classification = &v
		}
	}
	if t.classifier.OTPGenerated && t.otpGeneratedAt.IsZero() {
		t.otpGeneratedAt = time.Now()
	}
}

func exitCode(ps *os.ProcessState) int {
	if ps == nil {
		return -1
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return -int(ws.Signal())
	}
	return ps.ExitCode()
}

// WaitReady polls the local proxy port until it accepts connections.
func (t *Tunnel) WaitReady(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if t.HasExited() {
			return false
		}
		if port := t.Port(); port != 0 && PortOpen(port) {
			t.MarkReady()
			return true
		}
		select {
		case <-t.exited:
		case <-time.After(250 * time.Millisecond):
		}
	}
	return false
}

// MarkReady records the ready time and removes the secret file.
func (t *Tunnel) MarkReady() {
	t.mu.Lock()
	t.readyAt = time.Now()
	t.mu.Unlock()
	t.RemoveTokenFile()
}

// MarkStopping records that we end the attempt. It reports whether a process is running
// that still has to be ended; a process that already exited on its own does not count as
// stopped by us.
func (t *Tunnel) MarkStopping() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cmd == nil {
		t.stoppedByUs = true // still starting: the attempt does not count
		return false
	}
	select {
	case <-t.exited:
		return false
	default:
	}
	t.stoppedByUs = true
	return true
}

// Stop sends SIGTERM, SIGKILL after grace, then makes sure ocproxy released the port.
func (t *Tunnel) Stop(grace time.Duration) {
	t.RemoveTokenFile()
	if !t.MarkStopping() {
		return
	}
	proc := t.process()
	_ = terminate(proc)
	select {
	case <-t.exited:
	case <-time.After(grace):
		t.logf(slog.LevelWarn, "openconnect does not respond to SIGTERM, sending SIGKILL")
		_ = proc.Kill()
		<-t.exited
	}
	for range 20 {
		if port := t.Port(); !(port != 0 && PortOpen(port)) {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	if t.Hooks.KillWrapper != nil {
		t.Hooks.KillWrapper()
	} else {
		t.killWrapper()
	}
}

// runCommand runs argv and returns its exit code; replaced in tests.
var runCommand = func(argv []string) (int, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	err := cmd.Run()
	if cmd.ProcessState != nil {
		return exitCode(cmd.ProcessState), nil
	}
	return 0, err
}

func (t *Tunnel) killWrapper() {
	// Pattern without a leading hyphen and after "--", otherwise pkill reads it as an option.
	port := t.Port()
	pattern := fmt.Sprintf("ocproxy -D 127.0.0.1:%d ", port)
	t.logf(slog.LevelWarn, "ocproxy still holds port %d, running pkill", port)
	code, err := runCommand([]string{"pkill", "-9", "-U", strconv.Itoa(os.Getuid()), "-f", "--", pattern})
	if err != nil {
		t.logf(slog.LevelWarn, "pkill failed: %v", err)
		return
	}
	t.logf(slog.LevelWarn, "pkill exit code %d (0 = matched, 1 = no match, 2 = syntax error)", code)
}

// Reconnect asks openconnect to reconnect (SIGUSR2) where the platform has that signal.
func (t *Tunnel) Reconnect() {
	if proc := t.process(); proc != nil && !t.HasExited() {
		_ = reconnectSignal(proc)
	}
}

func (t *Tunnel) process() *os.Process {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cmd == nil {
		return nil
	}
	return t.cmd.Process
}

// Kill sends SIGKILL (TerminateProcess on Windows) if the process exists.
func (t *Tunnel) Kill() {
	if proc := t.process(); proc != nil {
		_ = proc.Kill()
	}
}

// Pid of openconnect, 0 before it started.
func (t *Tunnel) Pid() int {
	if proc := t.process(); proc != nil {
		return proc.Pid
	}
	return 0
}

// Exited is closed once openconnect exited and its output was read.
func (t *Tunnel) Exited() <-chan struct{} { return t.exited }

func (t *Tunnel) HasExited() bool {
	select {
	case <-t.exited:
		return true
	default:
		return false
	}
}

// Port is the local proxy port, 0 before Start.
func (t *Tunnel) Port() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.port
}

// ReturnCode is the exit code (negative signal number when killed by a signal); ok is false
// while the process runs or never started.
func (t *Tunnel) ReturnCode() (int, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.returnCode == nil {
		return 0, false
	}
	return *t.returnCode, true
}

// Classification is the verdict about the output, if any.
func (t *Tunnel) Classification() (Verdict, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.classification == nil {
		return Verdict{}, false
	}
	return *t.classification, true
}

// SetClassification overrides the verdict (the Windows tunnel reports setup failures so).
func (t *Tunnel) SetClassification(v Verdict) {
	t.mu.Lock()
	t.classification = &v
	t.mu.Unlock()
}

// ClassifierMFA is the second factor the classifier words its messages by.
func (t *Tunnel) ClassifierMFA() string { return t.classifier.MFA }

// OTPGeneratedAt is when a one-time code was generated, zero if none.
func (t *Tunnel) OTPGeneratedAt() time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.otpGeneratedAt
}

func (t *Tunnel) StoppedByUs() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stoppedByUs
}

// StartedAt and ReadyAt are zero until set; both carry a monotonic reading.
func (t *Tunnel) StartedAt() time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.startedAt
}

func (t *Tunnel) ReadyAt() time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.readyAt
}

// StderrTail is the last 20 output lines.
func (t *Tunnel) StderrTail() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.stderrTail...)
}

// RemoveStaleTokenFiles removes secret files left behind by a crashed daemon. Returns how many.
func RemoveStaleTokenFiles(directory string) int {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return 0
	}
	removed := 0
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), TokenPrefix) {
			continue
		}
		p := filepath.Join(directory, e.Name())
		if st, err := os.Stat(p); err != nil || !st.Mode().IsRegular() {
			continue
		}
		if os.Remove(p) == nil {
			removed++
		}
	}
	return removed
}

// FreePort returns a port on 127.0.0.1 nobody listens on right now.
func FreePort() (int, error) {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// PortOpen reports whether something accepts connections on 127.0.0.1:port.
func PortOpen(port int) bool {
	c, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 250*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}
