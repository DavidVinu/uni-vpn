// Package tunnel runs openconnect: start, readiness, stop, output classification.
// It mirrors uni_vpn/tunnel.py; argv, stdin, messages and timeouts must stay identical.
package tunnel

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/DavidVinu/uni-vpn/internal/certs"
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

// OncePrefix marks a code the user typed from their phone app instead of a TOTP secret
// ("code:123456"). It signs in once, for example to reach the MFA portal before this computer
// has its own code generator.
const OncePrefix = "code:"

// IsOnce reports whether totp is a typed code (OncePrefix) and returns the code.
func IsOnce(totp string) (string, bool) {
	return strings.CutPrefix(totp, OncePrefix)
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
	once           bool // a typed code goes through openconnect's prompt (OncePrefix)
	stdin          io.WriteCloser
	heldCode       []byte // the typed code, written once the OTP form asks for it, or Duo's "push"
	heldForPrompt  bool   // heldCode is Duo's "push", written at openconnect's next prompt
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
	// A typed one-time code takes the same way into the OTP form.
	t.mu.Lock()
	once := t.once
	t.mu.Unlock()
	if cfg.MFA != "duo_push" && !once {
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
	} else if platform.IsAndroid {
		// openconnect built for Android says "android"; the profiles' user agents name a
		// desktop, and gateways that check both should see what a Linux computer sends.
		args = append(args, "--os=linux-64")
	}
	if cfg.NoExternalAuth {
		args = append(args, noExternalAuthArgs(t.OpenConnect)...)
	}
	return args
}

var usages sync.Map // openconnect path and its file's time -> its usage text

// usage is openconnect's --help text, empty when it can't be run; tests replace it.
var usage = func(openconnect string) []byte {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, openconnect, "--help")
	cmd.WaitDelay = time.Second // a wrapper's leftover child keeps stdout open
	out, _ := cmd.Output()      // usage on stdout, exit 1
	return out
}

// helpText asks each openconnect binary once. An empty answer (no fork, the timeout) is not
// kept, so the next start asks again.
func helpText(openconnect string) []byte {
	key := openconnect
	if info, err := os.Stat(openconnect); err == nil {
		key += "\x00" + info.ModTime().String() // asked again after the distribution updates it
	}
	if v, ok := usages.Load(key); ok {
		return v.([]byte)
	}
	out := usage(openconnect)
	if len(out) > 0 {
		usages.Store(key, out)
	}
	return out
}

// noExternalAuthArgs: --no-external-auth came with openconnect 9.10. The Linux packages use the
// distribution's openconnect (Ubuntu 22.04 and Mint 21: 8.20, Debian 12: 9.01), which stops at
// an unknown option with its usage text. 8.20 never offers the web sign-in. 9.00 to 9.09 always
// offer it, and some gateways answer with the web sign-in of their default group although
// --authgroup names another (Stanford, UIUC); then 9.01 opens the desktop's browser itself
// unless --external-browser names another program. /bin/false opens nothing.
func noExternalAuthArgs(openconnect string) []string {
	if runtime.GOOS != "linux" {
		return []string{"--no-external-auth"}
	}
	help := helpText(openconnect)
	switch {
	case len(help) == 0 || bytes.Contains(help, []byte("--no-external-auth")):
		return []string{"--no-external-auth"}
	case bytes.Contains(help, []byte("--external-browser")):
		return []string{"--external-browser=/bin/false"}
	}
	return nil
}

// gnutlsBuild matches the "Using GnuTLS 3.7.9. Features present: ..." line in the usage of an
// openconnect built with GnuTLS (only "Using" is translated).
var gnutlsBuild = regexp.MustCompile(`GnuTLS \d`)

// strapPriority is openconnect's own default GnuTLS priority without TLS 1.3 (gnutls.c, its
// no_tls13 case).
const strapPriority = "NORMAL:-VERS-SSL3.0:+SHA256:%COMPAT:-VERS-TLS1.3:-3DES-CBC:-ARCFOUR-128"

// strapArgs: openconnect 9.00 to 9.09 (Debian 12: 9.01) always send STRAP keys (later versions
// only with the web sign-in) and until 9.20 compute their channel binding wrongly over TLS 1.3
// (openconnect issue 659). Gateways that check it refuse the tunnel after a good sign-in; the
// University of Bremen documents it for Debian 12. Over TLS 1.2 the binding is right. Only a
// GnuTLS build takes a priority string; the Debian and Ubuntu packages are such builds.
func strapArgs(openconnect string) []string {
	if runtime.GOOS != "linux" {
		return nil
	}
	help := helpText(openconnect)
	if bytes.Contains(help, []byte("--external-browser")) && !bytes.Contains(help, []byte("--no-external-auth")) &&
		gnutlsBuild.Match(help) {
		return []string{"--gnutls-priority=" + strapPriority}
	}
	return nil
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
		"--script="+t.script(port),
	)
	cmd = append(cmd, strapArgs(t.OpenConnect)...)
	if ca := t.caFile(); ca != "" {
		cmd = append(cmd, "--cafile="+ca)
	}
	cmd = append(cmd, t.TokenArgs()...)
	return append(cmd, t.Cfg.Host)
}

// CAFileName is the file in the token folder with the certificates of internal/certs.
const CAFileName = "ca.pem"

// caFile is openconnect's --cafile, whose certificates it trusts besides the system's: on
// Android the phone's own, which the app wrote together with the same extra certificates
// (app/android); elsewhere certs.PEM in CAFileName. The openconnect from the .pkg was built
// against Homebrew's certificate file, which a Mac without Homebrew does not have, so on macOS
// the file starts with the certificates macOS itself ships. "" for none.
func (t *Tunnel) caFile() string {
	path := filepath.Join(t.TokenDir, CAFileName)
	switch {
	case platform.IsAndroid:
		return platform.AndroidCAFile()
	case platform.IsMacOS && platform.IsBundled(t.OpenConnect):
		return withSystemCAFile(path, platform.MacOSCAFile)
	}
	return certs.WriteFile(path, nil)
}

// withSystemCAFile writes system's certificates and certs.PEM to path. When path cannot be
// written it returns system itself, which an openconnect without certificates of its own needs
// at least; "" when system is missing.
func withSystemCAFile(path, system string) string {
	st, err := os.Stat(system)
	if err != nil || !st.Mode().IsRegular() {
		return ""
	}
	data, err := os.ReadFile(system)
	if err != nil {
		return ""
	}
	if p := certs.WriteFile(path, data); p != "" {
		return p
	}
	return system
}

// script is the --script value. Android has no file for a wrapper script that it would run (only
// the app's native library folder may hold programs), so there openconnect starts ocproxy itself
// with the arguments bin/uni-vpn-ocproxy passes.
func (t *Tunnel) script(port int) string {
	if platform.IsAndroid {
		return fmt.Sprintf("exec %s -D 127.0.0.1:%d -k 30", ShellQuote(t.OCProxy), port)
	}
	return fmt.Sprintf("exec %s %d", ShellQuote(t.Wrapper), port)
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
	if !platform.IsWindows {
		// A distribution's openconnect (and Homebrew's) prints its messages in the system
		// language; the classifier reads the English ones. LC_CTYPE keeps the character set,
		// which openconnect uses to read passwords outside ASCII.
		if all := os.Getenv("LC_ALL"); all != "" {
			env = SetEnv(env, "LC_CTYPE", all)
		}
		env = SetEnv(SetEnv(SetEnv(env, "LC_ALL", ""), "LANGUAGE", ""), "LC_MESSAGES", "C")
	}
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
	if code, once := IsOnce(totp); once {
		pw, err := t.passwordBytes(password)
		if err != nil {
			return nil, err
		}
		if t.Cfg.MFA == "totp_append" {
			return append(append(append(append([]byte{}, pw...), t.Cfg.TOTPSeparator...), code...), '\n'), nil
		}
		return append(append(append([]byte{}, pw...), '\n'), code+"\n"...), nil
	}
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
	_, once := IsOnce(totp)
	t.mu.Lock()
	t.once = once
	t.mu.Unlock()
	if totp != "" && t.Cfg.MFA == "totp_field" && !once {
		if err := t.writeTokenFile(totp); err != nil {
			t.RemoveTokenFile() // a disk that is full: no part of the secret stays behind
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
	// A typed code waits for the OTP form, Duo's "push" for a prompt: after a refused password
	// openconnect asks for the password again and would send either as one (a second failed
	// sign-in).
	var held []byte
	if once && t.Cfg.MFA != "totp_append" || !once && t.Cfg.MFA == "duo_push" {
		end := bytes.LastIndexByte(data[:len(data)-1], '\n') + 1 // it is the last line
		data, held = data[:end], data[end:]
	}
	t.mu.Lock()
	t.cmd = cmd
	if held != nil {
		t.stdin, t.heldCode, t.heldForPrompt = stdin, held, !once
		if once {
			// A server that words its OTP form differently (classify.go) gets the code anyway.
			time.AfterFunc(otpFormWait, t.releaseCode)
		}
	}
	t.mu.Unlock()
	go t.readOutput(r, cmd)
	_, _ = stdin.Write(data) // a broken pipe means openconnect is already gone, the reader sees it
	if held == nil {
		_ = stdin.Close()
	}
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

// promptWait is how long openconnect's output must stop at an unfinished line before it counts
// as a prompt waiting for stdin: prompts ("Second Password:") end without a newline.
const promptWait = 300 * time.Millisecond

// promptReader calls onPrompt when the reader waits for more output after an unfinished line.
// bufio asks for more only once every full line before it was handled, so a verdict in them
// is known by then.
type promptReader struct {
	r        io.Reader
	partial  bool
	onPrompt func()
}

func (p *promptReader) Read(b []byte) (int, error) {
	var timer *time.Timer
	if p.partial {
		timer = time.AfterFunc(promptWait, p.onPrompt)
	}
	n, err := p.r.Read(b)
	if timer != nil {
		timer.Stop()
	}
	if n > 0 {
		p.partial = b[n-1] != '\n'
	}
	return n, err
}

// atPrompt answers openconnect's prompt with the held "push"; after a verdict
// releaseCodeLocked has already closed stdin without it.
func (t *Tunnel) atPrompt() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.heldForPrompt {
		t.releaseCodeLocked()
	}
}

// dropHeldLocked closes stdin without the held line: the login is over.
func (t *Tunnel) dropHeldLocked() {
	if t.stdin != nil {
		_ = t.stdin.Close()
	}
	t.stdin, t.heldCode, t.heldForPrompt = nil, nil, false
}

func (t *Tunnel) readOutput(r io.ReadCloser, cmd *exec.Cmd) {
	br := bufio.NewReader(&promptReader{r: r, onPrompt: t.atPrompt})
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
	t.dropHeldLocked()
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
			if v == (Verdict{StateAuthFailed, SAMLRequired}) && t.readyAt.IsZero() && t.cmd != nil {
				// A web sign-in never succeeds here, and openconnect may wait for one for ever.
				go t.abort(t.cmd.Process)
			}
		}
	}
	if t.classifier.OTPGenerated && t.otpGeneratedAt.IsZero() {
		t.otpGeneratedAt = time.Now()
	}
	if t.classifier.OTPGenerated || t.classification != nil {
		t.releaseCodeLocked()
	}
}

// abortGrace is how long openconnect has to end a sign-in that cannot succeed.
const abortGrace = 5 * time.Second

// abort ends openconnect without counting as stopped by us: WaitReady returns once it exited
// and the daemon reports the verdict.
func (t *Tunnel) abort(proc *os.Process) {
	_ = terminate(proc)
	select {
	case <-t.exited:
	case <-time.After(abortGrace):
		t.logf(slog.LevelWarn, "openconnect does not respond to SIGTERM, sending SIGKILL")
		_ = proc.Kill()
	}
}

// otpFormWait is how long a typed code waits for the OTP form before it is sent anyway.
const otpFormWait = 15 * time.Second

func (t *Tunnel) releaseCode() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.releaseCodeLocked()
}

// releaseCodeLocked writes the held typed code and closes stdin. After a verdict (wrong
// password) it only closes: openconnect's next password prompt then ends at EOF.
func (t *Tunnel) releaseCodeLocked() {
	if t.stdin == nil {
		return
	}
	if t.classification == nil {
		_, _ = t.stdin.Write(t.heldCode)
	}
	t.dropHeldLocked()
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
	t.dropHeldLocked() // signed in without asking (a Duo push approved on the phone)
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
	name := "ocproxy"
	if platform.IsAndroid {
		name = filepath.Base(t.OCProxy) // libocproxy.so
	}
	pattern := fmt.Sprintf("%s -D 127.0.0.1:%d ", name, port)
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

// FreePort returns a port on 127.0.0.1 nobody listens on right now, for openconnect's proxy,
// which listens on it after the sign-in. Not one of the system's own ports for outgoing
// connections (Linux and Android from 32768, macOS, iOS and Windows from 49152): a browser's
// connection to Uni VPN could take that one meanwhile, and the proxy could not listen.
func FreePort() (int, error) {
	for range 20 {
		port := 20000 + rand.IntN(12000)
		if l, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port))); err == nil {
			l.Close()
			return port, nil
		}
	}
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
