package credentials

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestMain doubles as a helper process for the real-process tests (Python ran sys.executable).
func TestMain(m *testing.M) {
	switch os.Getenv("CREDENTIALS_TEST_HELPER") {
	case "":
		os.Exit(m.Run())
	case "print":
		fmt.Fprint(os.Stdout, os.Getenv("CREDENTIALS_TEST_OUT"))
		os.Exit(0)
	case "exit1":
		os.Exit(1)
	case "silent":
		os.Exit(0)
	case "sleep":
		time.Sleep(5 * time.Second)
		os.Exit(0)
	}
}

// helper returns a command that reruns this test binary as the given helper.
func helper(t *testing.T, mode, out string) []string {
	t.Helper()
	t.Setenv("CREDENTIALS_TEST_HELPER", mode)
	t.Setenv("CREDENTIALS_TEST_OUT", out)
	t.Setenv("GORACE", "atexit_sleep_ms=0") // the race detector otherwise waits 1 s at exit
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return []string{exe}
}

type call struct {
	cmd     []string
	input   []byte
	timeout time.Duration
}

type recorder struct {
	calls []call
	res   Result
	err   error
}

func (r *recorder) run(cmd []string, input []byte, timeout time.Duration) (Result, error) {
	r.calls = append(r.calls, call{cmd, input, timeout})
	return r.res, r.err
}

func secretTool(string) string { return "/usr/bin/secret-tool" }

func linux(r *recorder) *Keyring {
	return &Keyring{GOOS: "linux", FindBinary: secretTool, Run: r.run}
}

func macos(r *recorder) *Keyring {
	return &Keyring{GOOS: "darwin", Run: r.run}
}

// --- GetPasswordTests (real processes) ---

func TestReturnsBytesWithoutNewline(t *testing.T) {
	k := &Keyring{GOOS: "linux"}
	out, err := k.GetPassword(context.Background(), "u", 2*time.Second, helper(t, "print", "secret"))
	if err != nil || string(out) != "secret" {
		t.Fatalf("got %q, %v", out, err)
	}
}

func TestMacOSStripsExactlyOneNewline(t *testing.T) {
	k := &Keyring{GOOS: "darwin"}
	out, err := k.GetPassword(context.Background(), "u", 2*time.Second, helper(t, "print", "pw \n"))
	if err != nil || string(out) != "pw " {
		t.Fatalf("got %q, %v", out, err)
	}
	out, _ = k.GetPassword(context.Background(), "u", 2*time.Second, helper(t, "print", "pw\n\n"))
	if string(out) != "pw\n" {
		t.Fatalf("got %q", out)
	}
}

func TestLinuxKeepsNewline(t *testing.T) {
	k := &Keyring{GOOS: "linux"}
	out, _ := k.GetPassword(context.Background(), "u", 2*time.Second, helper(t, "print", "pw\n"))
	if string(out) != "pw\n" {
		t.Fatalf("got %q", out)
	}
}

func TestMissing(t *testing.T) {
	k := &Keyring{GOOS: "linux"}
	_, err := k.GetPassword(context.Background(), "u", 2*time.Second, helper(t, "exit1", ""))
	if !errors.Is(err, ErrPasswordMissing) || err.Error() != "No password saved" {
		t.Fatalf("got %v", err)
	}
}

func TestEmptyOutputIsMissing(t *testing.T) {
	k := &Keyring{GOOS: "linux"}
	_, err := k.GetPassword(context.Background(), "u", 2*time.Second, helper(t, "silent", ""))
	if !errors.Is(err, ErrPasswordMissing) {
		t.Fatalf("got %v", err)
	}
}

func TestTimeoutMeansLocked(t *testing.T) {
	k := &Keyring{GOOS: "linux"}
	start := time.Now()
	_, err := k.GetPassword(context.Background(), "u", 300*time.Millisecond, helper(t, "sleep", ""))
	var locked *LockedError
	if !errors.As(err, &locked) || err.Error() != "Keyring locked or access denied" {
		t.Fatalf("got %v", err)
	}
	if time.Since(start) > 4*time.Second {
		t.Fatalf("took %v", time.Since(start))
	}
}

func TestGetSecretUsesLookupCommandWithoutStdin(t *testing.T) {
	r := &recorder{res: Result{Stdout: []byte("pw")}}
	out, err := linux(r).GetPassword(context.Background(), "ab1", 7*time.Second, nil)
	if err != nil || string(out) != "pw" {
		t.Fatalf("got %q, %v", out, err)
	}
	want := call{[]string{"/usr/bin/secret-tool", "lookup", "service", "uni-vpn", "user", "ab1"}, nil, 7 * time.Second}
	if !reflect.DeepEqual(r.calls, []call{want}) {
		t.Fatalf("calls %v", r.calls)
	}
}

// --- TotpEntryTests ---

func TestLookupCommandsUseSeparateServiceNames(t *testing.T) {
	k := linux(nil)
	got, _ := k.LookupCommand("ab1", KindPassword)
	if want := []string{"/usr/bin/secret-tool", "lookup", "service", "uni-vpn", "user", "ab1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	got, _ = k.LookupCommand("ab1", KindTOTP)
	if want := []string{"/usr/bin/secret-tool", "lookup", "service", "uni-vpn-totp", "user", "ab1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	got, _ = macos(nil).LookupCommand("ab1", KindTOTP)
	if want := []string{"/usr/bin/security", "find-generic-password", "-s", "uni-vpn-totp", "-a", "ab1", "-w"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestGetTotpReturnsToken(t *testing.T) {
	k := &Keyring{GOOS: "linux"}
	out, err := k.GetTotp(context.Background(), "u", 2*time.Second, helper(t, "print", "base32:GEZDGNBVGY3TQOJQ"))
	if err != nil || string(out) != "base32:GEZDGNBVGY3TQOJQ" {
		t.Fatalf("got %q, %v", out, err)
	}
}

func TestGetTotpMissingIsItsOwnError(t *testing.T) {
	k := &Keyring{GOOS: "linux"}
	_, err := k.GetTotp(context.Background(), "u", 2*time.Second, helper(t, "exit1", ""))
	if !errors.Is(err, ErrTotpMissing) || err.Error() != "No TOTP secret saved" {
		t.Fatalf("got %v", err)
	}
	if errors.Is(err, ErrPasswordMissing) {
		t.Fatal("TOTP missing must not be password missing")
	}
	if !errors.Is(err, ErrSecretMissing) || !errors.Is(ErrPasswordMissing, ErrSecretMissing) {
		t.Fatal("both must be SecretMissing")
	}
}

func TestStoreTotpLinux(t *testing.T) {
	r := &recorder{}
	if err := linux(r).StoreTotp("ab1", "base32:GEZDGNBVGY3TQOJQ"); err != nil {
		t.Fatal(err)
	}
	want := []string{"/usr/bin/secret-tool", "store", "--label", "Uni VPN (second factor)",
		"service", "uni-vpn-totp", "user", "ab1"}
	if !reflect.DeepEqual(r.calls[0].cmd, want) || string(r.calls[0].input) != "base32:GEZDGNBVGY3TQOJQ" {
		t.Fatalf("calls %v", r.calls)
	}
	if r.calls[0].timeout != 60*time.Second {
		t.Fatalf("timeout %v", r.calls[0].timeout)
	}
}

func TestStoreTotpMacOSDeletesThenAddsUnderOwnService(t *testing.T) {
	r := &recorder{}
	if err := macos(r).StoreTotp("ab1", "base32:GEZDGNBVGY3TQOJQ"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"/usr/bin/security", "delete-generic-password", "-s", "uni-vpn-totp", "-a", "ab1"}; !reflect.DeepEqual(r.calls[0].cmd, want) {
		t.Fatalf("got %v", r.calls[0].cmd)
	}
	script := string(r.calls[1].input)
	if !strings.Contains(script, `-s "uni-vpn-totp"`) || !strings.Contains(script, `-w "base32:GEZDGNBVGY3TQOJQ"`) {
		t.Fatalf("script %q", script)
	}
}

func TestDeleteTotpUsesOwnService(t *testing.T) {
	r := &recorder{}
	ok, err := linux(r).DeleteTotp("ab1")
	if !ok || err != nil {
		t.Fatalf("got %v, %v", ok, err)
	}
	if len(r.calls) != 1 || !reflect.DeepEqual(r.calls[0].cmd, []string{"/usr/bin/secret-tool", "clear", "service", "uni-vpn-totp", "user", "ab1"}) {
		t.Fatalf("calls %v", r.calls)
	}
}

// --- StoreTests ---

func TestLinuxStorePipesPasswordWithoutNewline(t *testing.T) {
	r := &recorder{}
	if err := linux(r).StorePassword("ab1", "päss word"); err != nil {
		t.Fatal(err)
	}
	c := r.calls[0]
	if !reflect.DeepEqual(c.cmd[:2], []string{"/usr/bin/secret-tool", "store"}) {
		t.Fatalf("cmd %v", c.cmd)
	}
	want := []string{"/usr/bin/secret-tool", "store", "--label", "Uni VPN", "service", "uni-vpn", "user", "ab1"}
	if !reflect.DeepEqual(c.cmd, want) || string(c.input) != "päss word" {
		t.Fatalf("call %v %q", c.cmd, c.input)
	}
}

func TestMacOSStoreUsesInteractiveSecurity(t *testing.T) {
	r := &recorder{}
	if err := macos(r).StorePassword("ab1", `a"b\c`); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.calls[0].cmd[:2], []string{"/usr/bin/security", "delete-generic-password"}) {
		t.Fatalf("first %v", r.calls[0].cmd)
	}
	c := r.calls[1]
	if !reflect.DeepEqual(c.cmd, []string{"/usr/bin/security", "-i"}) {
		t.Fatalf("cmd %v", c.cmd)
	}
	script := string(c.input)
	want := "add-generic-password -a \"ab1\" -s \"uni-vpn\" -T /usr/bin/security -w \"a\\\"b\\\\c\"\n"
	if script != want {
		t.Fatalf("script %q, want %q", script, want)
	}
	if strings.Contains(script, " -U ") {
		t.Fatal("must not update in place")
	}
}

func TestMacOSStoreQuotesUser(t *testing.T) {
	r := &recorder{}
	if err := macos(r).StorePassword(`a"b\`, "pw"); err != nil {
		t.Fatal(err)
	}
	if script := string(r.calls[1].input); !strings.HasPrefix(script, `add-generic-password -a "a\"b\\" -s`) {
		t.Fatalf("script %q", script)
	}
}

func TestStoreRejectsNewlineBeforeRunningAnything(t *testing.T) {
	r := &recorder{}
	for _, k := range []*Keyring{linux(r), macos(r), {GOOS: "windows", Cred: &fakeCred{}}} {
		for _, pw := range []string{"a\nb", "a\rb", "pw\"\ndelete-generic-password -s uni-vpn"} {
			err := k.StorePassword("ab1", pw)
			var ke *KeyringError
			if !errors.As(err, &ke) || err.Error() != "Password must not contain a line break" {
				t.Fatalf("%q: got %v", pw, err)
			}
		}
		if f, ok := k.Cred.(*fakeCred); ok && len(f.entries) != 0 {
			t.Fatal("wrote to Credential Manager")
		}
	}
	if len(r.calls) != 0 {
		t.Fatalf("calls %v", r.calls)
	}
}

func TestStoreFailureRaises(t *testing.T) {
	r := &recorder{res: Result{Code: 1, Stderr: []byte("broken\n")}}
	err := linux(r).StorePassword("ab1", "x")
	var ke *KeyringError
	if !errors.As(err, &ke) || err.Error() != "Uni VPN could not be saved: broken" {
		t.Fatalf("got %v", err)
	}
	r = &recorder{res: Result{Code: 3}}
	if err := linux(r).StorePassword("ab1", "x"); err == nil || err.Error() != "Uni VPN could not be saved: 3" {
		t.Fatalf("got %v", err)
	}
}

func TestStoreTimeout(t *testing.T) {
	r := &recorder{err: ErrTimeout}
	err := linux(r).StorePassword("ab1", "x")
	if err == nil || err.Error() != "No answer from the keyring (locked, or a dialog is waiting)" {
		t.Fatalf("got %v", err)
	}
	ok, err := linux(r).DeletePassword("ab1")
	if ok || err != nil {
		t.Fatalf("delete got %v, %v", ok, err)
	}
}

func TestMissingSecretTool(t *testing.T) {
	k := &Keyring{GOOS: "linux", FindBinary: func(string) string { return "" }}
	_, err := k.LookupCommand("ab1", KindPassword)
	var ke *KeyringError
	if !errors.As(err, &ke) || err.Error() != "secret-tool is missing (package libsecret-tools)" {
		t.Fatalf("got %v", err)
	}
	if _, err := k.DeletePassword("ab1"); !errors.As(err, &ke) {
		t.Fatalf("delete got %v", err)
	}
}

func TestDeleteReportsFailure(t *testing.T) {
	r := &recorder{res: Result{Code: 44}}
	ok, err := macos(r).DeletePassword("ab1")
	if ok || err != nil {
		t.Fatalf("got %v, %v", ok, err)
	}
	if want := []string{"/usr/bin/security", "delete-generic-password", "-s", "uni-vpn", "-a", "ab1"}; !reflect.DeepEqual(r.calls[0].cmd, want) || r.calls[0].input != nil {
		t.Fatalf("call %v", r.calls[0])
	}
}

func TestDecodeReplace(t *testing.T) {
	if got := decodeReplace([]byte("a\xff\xfeb")); got != "a��b" {
		t.Fatalf("got %q", got)
	}
}

// --- Windows logic with a fake Credential Manager ---

type fakeCred struct {
	entries  map[string][]byte
	users    map[string]string
	comments map[string]string
	readErr  error
	writeErr error
	block    chan struct{}
}

func (f *fakeCred) Read(target string) ([]byte, error) {
	if f.block != nil {
		<-f.block
	}
	if f.readErr != nil {
		return nil, f.readErr
	}
	return f.entries[target], nil
}

func (f *fakeCred) Write(target, user string, secret []byte, comment string) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	if f.entries == nil {
		f.entries, f.users, f.comments = map[string][]byte{}, map[string]string{}, map[string]string{}
	}
	f.entries[target], f.users[target], f.comments[target] = secret, user, comment
	return nil
}

func (f *fakeCred) Delete(target string) bool {
	_, ok := f.entries[target]
	delete(f.entries, target)
	return ok
}

func TestCredentialTarget(t *testing.T) {
	if got := CredentialTarget("uni-vpn-totp", "ab1"); got != "uni-vpn-totp:ab1" {
		t.Fatalf("got %q", got)
	}
}

func TestWindowsStoreReadDelete(t *testing.T) {
	f := &fakeCred{}
	k := &Keyring{GOOS: "windows", Cred: f, Run: func([]string, []byte, time.Duration) (Result, error) {
		t.Fatal("no command on Windows")
		return Result{}, nil
	}}
	if err := k.StorePassword("ab1", "pä$$ w0rd"); err != nil {
		t.Fatal(err)
	}
	if err := k.StoreTotp("ab1", "base32:X"); err != nil {
		t.Fatal(err)
	}
	// UTF-8 blob, label as comment, user name set.
	if string(f.entries["uni-vpn:ab1"]) != "pä$$ w0rd" || f.users["uni-vpn:ab1"] != "ab1" || f.comments["uni-vpn:ab1"] != "Uni VPN" {
		t.Fatalf("entries %v %v %v", f.entries, f.users, f.comments)
	}
	if f.comments["uni-vpn-totp:ab1"] != "Uni VPN (second factor)" {
		t.Fatalf("comments %v", f.comments)
	}
	out, err := k.GetPassword(context.Background(), "ab1", time.Second, nil)
	if err != nil || string(out) != "pä$$ w0rd" {
		t.Fatalf("got %q, %v", out, err)
	}
	if ok, _ := k.DeleteTotp("ab1"); !ok {
		t.Fatal("delete failed")
	}
	if _, err := k.GetTotp(context.Background(), "ab1", time.Second, nil); !errors.Is(err, ErrTotpMissing) {
		t.Fatalf("got %v", err)
	}
	if ok, _ := k.DeleteTotp("ab1"); ok {
		t.Fatal("second delete succeeded")
	}
}

func TestWindowsEmptyBlobIsMissing(t *testing.T) {
	f := &fakeCred{entries: map[string][]byte{"uni-vpn:ab1": {}}}
	k := &Keyring{GOOS: "windows", Cred: f}
	if _, err := k.GetPassword(context.Background(), "ab1", time.Second, nil); !errors.Is(err, ErrPasswordMissing) {
		t.Fatalf("got %v", err)
	}
}

func TestWindowsErrors(t *testing.T) {
	k := &Keyring{GOOS: "windows", Cred: &fakeCred{
		readErr:  &CredentialError{"CredRead failed: Access is denied."},
		writeErr: &CredentialError{"CredWrite failed: Access is denied."},
	}}
	_, err := k.GetPassword(context.Background(), "ab1", time.Second, nil)
	var ke *KeyringError
	if !errors.As(err, &ke) || err.Error() != "CredRead failed: Access is denied." {
		t.Fatalf("got %v", err)
	}
	err = k.StoreTotp("ab1", "x")
	if !errors.As(err, &ke) || err.Error() != "Uni VPN (second factor) could not be saved: CredWrite failed: Access is denied." {
		t.Fatalf("got %v", err)
	}
}

func TestWindowsTimeoutMeansLocked(t *testing.T) {
	f := &fakeCred{block: make(chan struct{})}
	defer close(f.block)
	k := &Keyring{GOOS: "windows", Cred: f}
	_, err := k.GetPassword(context.Background(), "ab1", 50*time.Millisecond, nil)
	var locked *LockedError
	if !errors.As(err, &locked) || err.Error() != "Credential Manager did not answer" {
		t.Fatalf("got %v", err)
	}
}

func TestWindowsCommandOverrideRunsCommand(t *testing.T) {
	r := &recorder{res: Result{Stdout: []byte("pw\n")}}
	k := &Keyring{GOOS: "windows", Cred: &fakeCred{}, Run: r.run}
	out, err := k.GetPassword(context.Background(), "ab1", time.Second, []string{"x"})
	if err != nil || string(out) != "pw\n" || len(r.calls) != 1 {
		t.Fatalf("got %q, %v, %v", out, err, r.calls)
	}
}

func TestContextCancel(t *testing.T) {
	f := &fakeCred{block: make(chan struct{})}
	defer close(f.block)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	k := &Keyring{GOOS: "windows", Cred: f}
	if _, err := k.GetPassword(ctx, "ab1", time.Minute, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestStoreRejectsNewlineInUser(t *testing.T) {
	r := &recorder{}
	f := &fakeCred{}
	for _, k := range []*Keyring{linux(r), macos(r), {GOOS: "windows", Cred: f}} {
		for _, user := range []string{"a\nb", "a\rb"} {
			err := k.StorePassword(user, "pw")
			var ke *KeyringError
			if !errors.As(err, &ke) || err.Error() != "University ID must not contain a line break" {
				t.Fatalf("%q: got %v", user, err)
			}
		}
		// The value check comes first.
		if err := k.StorePassword("a\nb", "p\nw"); err == nil || err.Error() != "Password must not contain a line break" {
			t.Fatalf("got %v", err)
		}
	}
	if len(r.calls) != 0 || len(f.entries) != 0 {
		t.Fatalf("calls %v entries %v", r.calls, f.entries)
	}
}

func TestKeychainText(t *testing.T) {
	for in, want := range map[string]string{
		"70c3a4737320776f7264": "p\u00e4ss word", // non-ASCII: decoded
		"6869090a":             "hi\t\n",         // control characters: decoded
		"c3a4\n":               "\u00e4",         // Python's $ matches before a final newline
		"cafe":                 "cafe",           // decodes to non-ASCII bytes
		"616263":               "616263",         // printable ASCII: kept
		"abc":                  "abc",            // odd length
		"CAFE":                 "CAFE",           // upper case is not security's hex
		"c3a4\n\n":             "c3a4\n\n",
		"secret":               "secret",
	} {
		if in == "cafe" {
			want = "\xca\xfe"
		}
		if got := string(KeychainText([]byte(in))); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

func TestMacOSLookupDecodesHexOnlyWithoutOverride(t *testing.T) {
	r := &recorder{res: Result{Stdout: []byte("c3a4\n")}}
	out, err := macos(r).GetPassword(context.Background(), "ab1", time.Second, nil)
	if err != nil || string(out) != "\u00e4" {
		t.Fatalf("got %q, %v", out, err)
	}
	out, _ = macos(r).GetPassword(context.Background(), "ab1", time.Second, []string{"x"})
	if string(out) != "c3a4" {
		t.Fatalf("override got %q", out)
	}
	out, _ = linux(r).GetPassword(context.Background(), "ab1", time.Second, nil)
	if string(out) != "c3a4\n" {
		t.Fatalf("linux got %q", out)
	}
}
