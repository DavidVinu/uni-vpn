//go:build !windows

package tunnel

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DavidVinu/uni-vpn/internal/tunnel/fakeoc"
)

// flow is the fixture of the Python TunnelTests: the test binary acts as openconnect.
type flow struct {
	t                         *testing.T
	cfg                       Config
	pwfile, tokenfile, tokdir string
	fake                      string
}

func newFlow(t *testing.T) *flow {
	tmp := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f := &flow{t: t, cfg: Config{Host: "vpn.example", LoginName: "u", UserAgent: "AnyConnect Linux_64 5.1.18.314", MFA: "totp_field"},
		pwfile: filepath.Join(tmp, "pw"), tokenfile: filepath.Join(tmp, "token"), tokdir: filepath.Join(tmp, "state"), fake: exe}
	if err := os.Mkdir(f.tokdir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(fakeoc.EnvVar, "openconnect")
	// A race-enabled fake would sleep a second before every exit.
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	t.Setenv("FAKE_PASSWORD_FILE", f.pwfile)
	t.Setenv("FAKE_TOKEN_FILE", f.tokenfile)
	t.Setenv("FAKE_MODE", "ok")
	t.Setenv("FAKE_DELAY", "0.2")
	return f
}

func (f *flow) make() *Tunnel {
	tn := New(f.cfg, f.fake, wrapper, nil, "", f.tokdir)
	tn.TOTPCode = fixedCode
	f.t.Cleanup(func() { tn.Kill() })
	return tn
}

func (f *flow) tokenFiles() []string { return names(f.t, f.tokdir) }

func (f *flow) start(tn *Tunnel, password, totp string) {
	f.t.Helper()
	if err := tn.Start([]byte(password), totp); err != nil {
		f.t.Fatal(err)
	}
}

func TestCommand(t *testing.T) {
	f := newFlow(t)
	cmd := f.make().Command(4321)
	if cmd[0] != f.fake {
		t.Fatal(cmd)
	}
	for _, a := range []string{"--protocol=anyconnect", "--user=u", "--passwd-on-stdin", "--non-inter", "--no-dtls",
		// "exec" in front: openconnect runs the value via /bin/sh -c, and dash would otherwise
		// leave an sh running next to ocproxy (seen in the process list on 2026-09-08).
		"--script=exec " + wrapper + " 4321"} {
		if !slices.Contains(cmd, a) {
			t.Fatal(a)
		}
	}
	if cmd[len(cmd)-1] != "vpn.example" || slices.Contains(cmd, "--dump-http-traffic") {
		t.Fatal(cmd)
	}
	for _, a := range cmd {
		if strings.HasPrefix(a, "--token") {
			t.Fatal(a)
		}
	}
}

func TestTOTPSecretReachesOpenconnectByFileAndIsRemovedWhenReady(t *testing.T) {
	f := newFlow(t)
	tn := f.make()
	f.start(tn, "secret", token)
	files := f.tokenFiles()
	if len(files) != 1 || !strings.HasPrefix(files[0], "totp-") {
		t.Fatal(files)
	}
	st, err := os.Stat(filepath.Join(f.tokdir, files[0]))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatal(st, err)
	}
	if !tn.WaitReady(3 * time.Second) {
		t.Fatal("not ready")
	}
	if data, _ := os.ReadFile(f.tokenfile); string(data) != token+"\n" {
		t.Fatalf("%q", data)
	}
	if got := f.tokenFiles(); len(got) != 0 {
		t.Fatal("secret file must be gone once connected", got)
	}
	tn.Stop(2 * time.Second)
}

func TestTokenFileRemovedWhenOpenconnectExitsEarly(t *testing.T) {
	f := newFlow(t)
	t.Setenv("FAKE_MODE", "auth_fail")
	tn := f.make()
	f.start(tn, "x", token)
	if tn.WaitReady(3 * time.Second) {
		t.Fatal("ready")
	}
	time.Sleep(100 * time.Millisecond)
	if got := f.tokenFiles(); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestTokenFileRemovedOnStopBeforeReady(t *testing.T) {
	f := newFlow(t)
	t.Setenv("FAKE_MODE", "never_ready")
	tn := f.make()
	f.start(tn, "x", token)
	if got := f.tokenFiles(); len(got) != 1 {
		t.Fatal(got)
	}
	tn.Stop(time.Second)
	if got := f.tokenFiles(); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestWithoutTOTPNoTokenFile(t *testing.T) {
	f := newFlow(t)
	tn := f.make()
	f.start(tn, "secret", "")
	if !tn.WaitReady(3 * time.Second) {
		t.Fatal("not ready")
	}
	if got := f.tokenFiles(); len(got) != 0 {
		t.Fatal(got)
	}
	if _, err := os.Stat(f.tokenfile); err == nil {
		t.Fatal("token file was read")
	}
	tn.Stop(2 * time.Second)
}

func TestRejectedSoftTokenIsClassified(t *testing.T) {
	f := newFlow(t)
	t.Setenv("FAKE_MODE", "totp_rejected")
	tn := f.make()
	f.start(tn, "x", token)
	if tn.WaitReady(3 * time.Second) {
		t.Fatal("ready")
	}
	if code, ok := tn.ReturnCode(); !ok || code != 1 {
		t.Fatal(code, ok)
	}
	v, ok := tn.Classification()
	if !ok || v != (Verdict{"auth_failed", TOTPRejected}) {
		t.Fatal(v)
	}
	at := tn.OTPGeneratedAt()
	if at.IsZero() || time.Since(at) > 10*time.Second {
		t.Fatal(at)
	}
}

func TestOnlyTOTPFieldWritesATokenFile(t *testing.T) {
	f := newFlow(t)
	for _, mfa := range []string{"none", "duo_push", "totp_append"} {
		f.cfg.MFA = mfa
		tn := f.make()
		f.start(tn, "secret", token)
		if got := f.tokenFiles(); len(got) != 0 {
			t.Fatal(mfa, got)
		}
		for _, a := range tn.Command(1) {
			if strings.HasPrefix(a, "--token") {
				t.Fatal(mfa, a)
			}
		}
		if !tn.WaitReady(3 * time.Second) {
			t.Fatal(mfa, "not ready")
		}
		tn.Stop(2 * time.Second)
	}
}

func TestDuoPushSendsPushAsTheSecondLine(t *testing.T) {
	f := newFlow(t)
	f.cfg.MFA = "duo_push"
	tn := f.make()
	f.start(tn, "secret", "")
	if !tn.WaitReady(3 * time.Second) {
		t.Fatal("not ready")
	}
	tn.Stop(2 * time.Second)
	if data, _ := os.ReadFile(f.pwfile); string(data) != "secret\npush\n" {
		t.Fatalf("%q", data)
	}
}

func TestOTPGeneratedAtIsZeroWithoutToken(t *testing.T) {
	f := newFlow(t)
	tn := f.make()
	f.start(tn, "secret", "")
	if !tn.WaitReady(3 * time.Second) {
		t.Fatal("not ready")
	}
	tn.Stop(2 * time.Second)
	if !tn.OTPGeneratedAt().IsZero() {
		t.Fatal(tn.OTPGeneratedAt())
	}
}

func TestStartReadyStop(t *testing.T) {
	f := newFlow(t)
	tn := f.make()
	f.start(tn, "secret", "")
	if !tn.WaitReady(3 * time.Second) {
		t.Fatal("not ready")
	}
	if !PortOpen(tn.Port()) {
		t.Fatal("port closed")
	}
	if data, _ := os.ReadFile(f.pwfile); string(data) != "secret\n" {
		t.Fatalf("%q", data)
	}
	tn.Stop(2 * time.Second)
	if !tn.HasExited() || !tn.StoppedByUs() {
		t.Fatal("not stopped")
	}
	if code, ok := tn.ReturnCode(); !ok || code != 0 {
		t.Fatal(code, ok)
	}
	if PortOpen(tn.Port()) {
		t.Fatal("port still open")
	}
	if tn.ReadyAt().Before(tn.StartedAt()) {
		t.Fatal("ready before start")
	}
}

func TestAuthFail(t *testing.T) {
	f := newFlow(t)
	t.Setenv("FAKE_MODE", "auth_fail")
	tn := f.make()
	f.start(tn, "x", "")
	if tn.WaitReady(3 * time.Second) {
		t.Fatal("ready")
	}
	if !tn.HasExited() {
		t.Fatal("running")
	}
	if code, _ := tn.ReturnCode(); code != 1 {
		t.Fatal(code)
	}
	v, _ := tn.Classification()
	if v != (Verdict{"auth_failed", PasswordRejected}) {
		t.Fatal(v)
	}
	if !slices.ContainsFunc(tn.StderrTail(), func(l string) bool { return strings.Contains(l, "Failed to complete") }) {
		t.Fatal(tn.StderrTail())
	}
}

func TestInputRequiredMessage(t *testing.T) {
	f := newFlow(t)
	t.Setenv("FAKE_MODE", "input_required")
	tn := f.make()
	f.start(tn, "x", "")
	if tn.WaitReady(3 * time.Second) {
		t.Fatal("ready")
	}
	v, _ := tn.Classification()
	// Finding 6: a wrong password produces the same sequence of lines.
	if v != (Verdict{"auth_failed", AuthRejected}) || !strings.Contains(v.Message, "password") {
		t.Fatal(v)
	}
}

func TestScriptValueSurvivesShWithSpacesAndQuote(t *testing.T) {
	// Finding 5: openconnect runs --script via /bin/sh -c.
	base := filepath.Join(t.TempDir(), "with spaces")
	if err := os.Mkdir(base, 0o755); err != nil {
		t.Fatal(err)
	}
	wrap := filepath.Join(base, "o'proxy wrapper")
	argfile := filepath.Join(base, "args")
	if err := os.WriteFile(wrap, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > '"+argfile+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	tn := New(Config{Host: "vpn.example", LoginName: "u"}, "openconnect", wrap, nil, "", t.TempDir())
	var script string
	for _, a := range tn.Command(4321) {
		if s, ok := strings.CutPrefix(a, "--script="); ok {
			script = s
		}
	}
	if out, err := exec.Command("/bin/sh", "-c", script).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	if data, _ := os.ReadFile(argfile); string(data) != "4321\n" {
		t.Fatalf("%q", data)
	}
}

func TestKillWrapperKillsPortHolder(t *testing.T) {
	// Finding 4: a process whose command line looks like the wrapper exec and that
	// holds the port must be gone after killWrapper().
	if _, err := exec.LookPath("pkill"); err != nil {
		t.Skip("pkill not installed")
	}
	port, err := FreePort()
	if err != nil {
		t.Fatal(err)
	}
	exe, _ := os.Executable()
	dummy := exec.Command(exe, "ocproxy", "-D", "127.0.0.1:"+itoa(port), "-k", "30")
	dummy.Env = append(os.Environ(), fakeoc.EnvVar+"=portholder")
	if err := dummy.Start(); err != nil {
		t.Fatal(err)
	}
	defer dummy.Process.Kill()
	waitFor(3*time.Second, func() bool { return PortOpen(port) })
	if !PortOpen(port) {
		t.Fatal("dummy does not hold the port")
	}
	tn := New(heidelberg(), "openconnect", wrapper, nil, "", t.TempDir())
	tn.port = port
	tn.killWrapper()
	waitFor(2*time.Second, func() bool { return !PortOpen(port) })
	if PortOpen(port) {
		t.Fatal("port still open after killWrapper()")
	}
	_ = dummy.Wait()
	if code := exitCode(dummy.ProcessState); code != -9 {
		t.Fatal(code)
	}
}

func waitFor(d time.Duration, cond func() bool) {
	deadline := time.Now().Add(d)
	for !cond() && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
}

func TestIgnoreSigtermGetsKilled(t *testing.T) {
	f := newFlow(t)
	t.Setenv("FAKE_MODE", "ignore_sigterm")
	tn := f.make()
	f.start(tn, "x", "")
	if !tn.WaitReady(3 * time.Second) {
		t.Fatal("not ready")
	}
	tn.Stop(500 * time.Millisecond)
	if !tn.HasExited() {
		t.Fatal("running")
	}
	if code, _ := tn.ReturnCode(); code != -9 {
		t.Fatal(code)
	}
}

func TestNeverReadyTimeout(t *testing.T) {
	f := newFlow(t)
	t.Setenv("FAKE_MODE", "never_ready")
	tn := f.make()
	f.start(tn, "x", "")
	if tn.WaitReady(600 * time.Millisecond) {
		t.Fatal("ready")
	}
	if tn.HasExited() {
		t.Fatal("exited")
	}
	tn.Stop(time.Second)
	if !tn.HasExited() {
		t.Fatal("running")
	}
}

func TestReconnectSendsSIGUSR2(t *testing.T) {
	f := newFlow(t)
	tn := f.make()
	f.start(tn, "x", "")
	if !tn.WaitReady(3 * time.Second) {
		t.Fatal("not ready")
	}
	tn.Reconnect()
	time.Sleep(300 * time.Millisecond)
	if !slices.ContainsFunc(tn.StderrTail(), func(l string) bool { return strings.Contains(l, "SIGUSR2") }) {
		t.Fatal(tn.StderrTail())
	}
	tn.Stop(2 * time.Second)
}

func TestStopBeforeStartKillsTheProcess(t *testing.T) {
	f := newFlow(t)
	tn := f.make()
	tn.Stop(2 * time.Second)
	if !tn.StoppedByUs() {
		t.Fatal("not marked")
	}
	f.start(tn, "x", token)
	if tn.WaitReady(3 * time.Second) {
		t.Fatal("ready")
	}
	if code, _ := tn.ReturnCode(); code != -9 {
		t.Fatal(code)
	}
	if got := f.tokenFiles(); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestOCProxyIsPassedInTheEnvironment(t *testing.T) {
	f := newFlow(t)
	tn := f.make()
	tn.OCProxy = "/usr/bin/ocproxy"
	env := tn.env(1)
	if !slices.Contains(env, "OCPROXY=/usr/bin/ocproxy") {
		t.Fatal(env)
	}
}
