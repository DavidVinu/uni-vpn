package tunnel

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DavidVinu/uni-vpn/internal/tunnel/fakeoc"
)

func TestMain(m *testing.M) {
	fakeoc.MaybeRun()
	os.Exit(m.Run())
}

const (
	wrapper = "/nonexistent/uni-vpn-ocproxy"
	token   = "base32:GEZDGNBVGY3TQOJQ"
)

// heidelberg is Config(user="ab123") of the Python core: the default profile.
func heidelberg() Config {
	return Config{
		Host: "vpn-ac.uni-heidelberg.de", UserAgent: "AnyConnect Linux_64 5.1.18.314", LoginName: "ab123",
		MFA: "totp_field", StopGrace: 15 * time.Second,
	}
}

func fixedCode(string, time.Time) (string, error) { return "123456", nil }

func makeProfile(t *testing.T, edit func(*Config)) *Tunnel {
	cfg := heidelberg()
	if edit != nil {
		edit(&cfg)
	}
	tn := New(cfg, "/usr/bin/openconnect", wrapper, nil, "", t.TempDir())
	tn.TOTPCode = fixedCode
	return tn
}

func TestHeidelbergCommandIsUnchanged(t *testing.T) {
	// The command line from before profiles existed, argument for argument.
	want := []string{
		"/usr/bin/openconnect", "--protocol=anyconnect", "--useragent=AnyConnect Linux_64 5.1.18.314",
		"--user=ab123", "--passwd-on-stdin", "--non-inter", "--no-dtls", "--force-dpd=30",
		"--reconnect-timeout=60", "--script-tun", "--script=exec " + wrapper + " 4321", "vpn-ac.uni-heidelberg.de"}
	if got := makeProfile(t, nil).Command(4321); !slices.Equal(got, want) {
		t.Fatalf("%q", got)
	}
}

func TestProfileFlags(t *testing.T) {
	// The daemon builds LoginName from user and username_suffix (config.login_name).
	cmd := makeProfile(t, func(c *Config) {
		c.Host, c.AuthGroup, c.UserGroup, c.OS, c.UserAgent = "sslvpn.ethz.ch", "staff-net", "exchange", "win", "AnyConnect"
		c.LoginName, c.NoExternalAuth = "ab123@staff-net.ethz.ch", true
	}).Command(1)
	for _, arg := range []string{"--authgroup=staff-net", "--usergroup=exchange", "--os=win", "--useragent=AnyConnect",
		"--user=ab123@staff-net.ethz.ch", "--no-external-auth", "--non-inter"} {
		if !slices.Contains(cmd, arg) {
			t.Fatal(arg)
		}
	}
	if cmd[len(cmd)-1] != "sslvpn.ethz.ch" {
		t.Fatal(cmd)
	}
}

func TestGroupNamesWithSpacesStayOneArgument(t *testing.T) {
	cmd := makeProfile(t, func(c *Config) { c.AuthGroup = "RWTH-VPN (Split Tunnel)" }).Command(1)
	if !slices.Contains(cmd, "--authgroup=RWTH-VPN (Split Tunnel)") {
		t.Fatal(cmd)
	}
}

func TestDuoPushDropsNonInter(t *testing.T) {
	if slices.Contains(makeProfile(t, func(c *Config) { c.MFA = "duo_push" }).Command(1), "--non-inter") {
		t.Fatal()
	}
}

func TestStdinPerMode(t *testing.T) {
	for _, c := range []struct {
		mfa, sep, totp, want string
	}{
		{"none", "", "", "pw\n"},
		{"totp_field", "", token, "pw\n"},
		{"duo_push", "", "", "pw\npush\n"},
		{"totp_append", "", token, "pw123456\n"},
		{"totp_append", ",", token, "pw,123456\n"},
	} {
		tn := makeProfile(t, func(cfg *Config) { cfg.MFA, cfg.TOTPSeparator = c.mfa, c.sep })
		got, err := tn.StdinBytes([]byte("pw"), c.totp)
		if err != nil || string(got) != c.want {
			t.Fatalf("%s: %q %v", c.mfa, got, err)
		}
	}
}

func TestTOTPAppendRecordsWhenTheCodeWasMade(t *testing.T) {
	tn := makeProfile(t, func(c *Config) { c.MFA = "totp_append" })
	if _, err := tn.StdinBytes([]byte("pw"), token); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(tn.OTPGeneratedAt()); d < 0 || d > 5*time.Second {
		t.Fatal(d)
	}
	if _, err := makeProfile(t, func(c *Config) { c.MFA = "totp_append" }).StdinBytes([]byte("pw"), ""); err == nil {
		t.Fatal("expected an error without a secret")
	}
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{
		"":                 "''",
		"/usr/bin/x":       "/usr/bin/x",
		"a b":              "'a b'",
		"o'proxy":          `'o'"'"'proxy'`,
		"x@y%z+=:,./-_A09": "x@y%z+=:,./-_A09",
		"ä":                "'ä'",
	} {
		if got := ShellQuote(in); got != want {
			t.Fatalf("%q: %q", in, got)
		}
	}
}

func TestRemoveStaleTokenFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"totp-123", "totp-456", "daemon.log"} {
		if err := os.WriteFile(dir+"/"+name, []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(dir+"/totp-dir", 0o700); err != nil {
		t.Fatal(err)
	}
	if n := RemoveStaleTokenFiles(dir); n != 2 {
		t.Fatal(n)
	}
	if got := names(t, dir); !slices.Equal(got, []string{"daemon.log", "totp-dir"}) {
		t.Fatal(got)
	}
	if RemoveStaleTokenFiles(dir+"/does-not-exist") != 0 {
		t.Fatal()
	}
}

func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func TestFreePortIsClosed(t *testing.T) {
	port, err := FreePort()
	if err != nil {
		t.Fatal(err)
	}
	if PortOpen(port) {
		t.Fatal("free port is open")
	}
	l, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	if !PortOpen(port) {
		t.Fatal("listening port is closed")
	}
	l.Close()
}

func TestSetEnvReplaces(t *testing.T) {
	got := SetEnv([]string{"A=1", "OCPROXY=old", "=C:=C:\\x", "B=2"}, "OCPROXY", "new")
	if !slices.Equal(got, []string{"A=1", "=C:=C:\\x", "B=2", "OCPROXY=new"}) {
		t.Fatal(got)
	}
}

func TestKillWrapperArgv(t *testing.T) {
	// Finding 4: pattern without a leading hyphen, "--" before the pattern,
	// only our own processes, return code in the log.
	var buf syncBuffer
	tn := New(heidelberg(), "openconnect", wrapper, newLogger(&buf), "", t.TempDir())
	tn.port = 4321
	var calls [][]string
	old := runCommand
	runCommand = func(argv []string) (int, error) { calls = append(calls, argv); return 1, nil }
	defer func() { runCommand = old }()
	tn.killWrapper()
	want := []string{"pkill", "-9", "-U", itoa(os.Getuid()), "-f", "--", "ocproxy -D 127.0.0.1:4321 "}
	if len(calls) != 1 || !slices.Equal(calls[0], want) {
		t.Fatalf("%q", calls)
	}
	if !strings.Contains(buf.String(), "pkill exit code 1 ") {
		t.Fatal(buf.String())
	}
}

func TestCommandWithTokenFileEnablesTOTP(t *testing.T) {
	dir := t.TempDir()
	tn := New(Config{Host: "vpn.example", LoginName: "u", MFA: "totp_field"}, "openconnect", wrapper, nil, "", dir)
	tn.tokenFile = dir + "/totp-1"
	cmd := tn.Command(4321)
	if !slices.Contains(cmd, "--token-mode=totp") || !slices.Contains(cmd, "--token-secret=@"+dir+"/totp-1") {
		t.Fatal(cmd)
	}
	// The secret itself never appears on the command line.
	for _, a := range cmd {
		if strings.Contains(a, "base32") {
			t.Fatal(a)
		}
	}
}

func TestMissingBinary(t *testing.T) {
	tn := New(Config{Host: "vpn.example", LoginName: "u", MFA: "totp_field"}, "/nonexistent/openconnect", wrapper, nil, "", t.TempDir())
	if err := tn.Start([]byte("x"), ""); err == nil {
		t.Fatal("expected an error")
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestReadLineCutsOverlongLines(t *testing.T) {
	br := bufio.NewReaderSize(strings.NewReader("0123456789abcdefghijklmnopqrstuv\nok\n"+strings.Repeat("z", 30)), 16)
	var got []string
	for {
		line, err := readLine(br, 8)
		if len(line) > 0 {
			got = append(got, string(line))
		}
		if err != nil {
			break
		}
	}
	if !slices.Equal(got, []string{"01234567", "ok\n", "zzzzzzzz"}) {
		t.Fatalf("%q", got)
	}
}

func TestTOTPAppendWithAMalformedSecretIsATOTPSecretError(t *testing.T) {
	tn := makeProfile(t, func(c *Config) { c.MFA = "totp_append" })
	tn.TOTPCode = func(string, time.Time) (string, error) { return "", errors.New("Secret is not valid Base32") }
	var se *TOTPSecretError
	if _, err := tn.StdinBytes([]byte("pw"), "base32:!!!"); !errors.As(err, &se) || se.Msg != "Secret is not valid Base32" {
		t.Fatal(err)
	}
	if _, err := makeProfile(t, func(c *Config) { c.MFA = "totp_append" }).StdinBytes([]byte("pw"), ""); !errors.As(err, &se) {
		t.Fatal(err)
	}
}
