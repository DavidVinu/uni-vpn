package wintunnel

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/DavidVinu/uni-vpn/internal/tunnel"
	"github.com/DavidVinu/uni-vpn/internal/tunnel/fakeoc"
)

func TestMain(m *testing.M) {
	fakeoc.MaybeRun()
	os.Exit(m.Run())
}

func TestStateFile(t *testing.T) {
	values := ParseState("INTERNAL_IP4_ADDRESS=10.1.2.3\nINTERNAL_IP4_DNS=10.0.0.1 10.0.0.2\njunk\nTUNIDX=\n")
	if values["INTERNAL_IP4_ADDRESS"] != "10.1.2.3" {
		t.Fatal(values)
	}
	if got := DNSServers(values); !slices.Equal(got, []string{"10.0.0.1", "10.0.0.2"}) {
		t.Fatal(got)
	}
	if v, ok := values["TUNIDX"]; !ok || v != "" {
		t.Fatal(values)
	}
	if got := DNSServers(map[string]string{}); len(got) != 0 {
		t.Fatal(got)
	}
	// openconnect lists IPv6 servers in the same variable; the SOCKS source address is IPv4.
	if got := DNSServers(map[string]string{"INTERNAL_IP4_DNS": "fd00::53 10.0.0.1 junk"}); !slices.Equal(got, []string{"10.0.0.1"}) {
		t.Fatal(got)
	}
}

func TestParseStateLikePython(t *testing.T) {
	values := ParseState(" A = 1 = 2 \r\nB=x\rC=y\u0085D=z\nA2=")
	want := map[string]string{"A": "1 = 2", "B": "x", "C": "y", "D": "z", "A2": ""}
	for k, v := range want {
		if values[k] != v {
			t.Fatalf("%s: %q", k, values[k])
		}
	}
	if len(values) != len(want) {
		t.Fatal(values)
	}
}

func TestCommandUsesInterfaceAndScriptNotScriptTun(t *testing.T) {
	tn := New(tunnel.Config{LoginName: "ab1", Host: "vpn.example", UserAgent: "AnyConnect Linux_64 5.1.18.314", MFA: "totp_field"},
		`C:\oc\openconnect.exe`, `C:\Program Files\uni vpn\bin\uni-vpn-vpnc.js`, nil, t.TempDir())
	cmd := tn.Command(4000)
	if !slices.Contains(cmd, "--interface=uni-vpn") || !slices.Contains(cmd, `--script=C:\Program Files\uni vpn\bin\uni-vpn-vpnc.js`) ||
		slices.Contains(cmd, "--script-tun") || !slices.Contains(cmd, "--disable-ipv6") || cmd[len(cmd)-1] != "vpn.example" {
		t.Fatalf("%q", cmd)
	}
	// The embedded tunnel builds the same argv through its hook.
	if !slices.Equal(tn.Tunnel.Command(4000), cmd) {
		t.Fatal("hook not used")
	}
}

func TestCommandUsesTheProfileLikeThePosixTunnel(t *testing.T) {
	cfg := tunnel.Config{LoginName: "jdoe@staff-net.ethz.ch", Host: "sslvpn.ethz.ch", AuthGroup: "staff-net",
		NoExternalAuth: true, MFA: "duo_push", UserAgent: "AnyConnect Linux_64 5.1.18.314"}
	tn := New(cfg, "openconnect.exe", "x.js", nil, t.TempDir())
	cmd := tn.Command(4000)
	auth := tn.AuthArgs()
	if !slices.Equal(cmd[1:1+len(auth)], auth) {
		t.Fatal(cmd)
	}
	if !slices.Contains(cmd, "--authgroup=staff-net") || !slices.Contains(cmd, "--user=jdoe@staff-net.ethz.ch") ||
		slices.Contains(cmd, "--non-inter") || cmd[len(cmd)-1] != "sslvpn.ethz.ch" {
		t.Fatal(cmd)
	}
}

func TestPasswordThatIsNotUTF8IsRejected(t *testing.T) {
	_, err := encodePassword([]byte{'p', 0xff})
	var pe *tunnel.PasswordEncodingError
	if !errors.As(err, &pe) || !strings.HasPrefix(pe.Msg, "The password contains characters openconnect cannot read on this Windows (") {
		t.Fatal(err)
	}
	tn := New(tunnel.Config{MFA: "none"}, "openconnect.exe", "x.js", nil, t.TempDir())
	if _, err := tn.StdinBytes([]byte{0xff}, ""); !errors.As(err, &pe) {
		t.Fatal(err)
	}
}

func TestChildEnvironmentKeepsNoUserControlledPaths(t *testing.T) {
	current := map[string]string{"ComSpec": `C:\Users\a\evil.exe`, "Path": `C:\Users\a\bin`, "TEMP": `C:\T`,
		"SystemRoot": `C:\Users\a\fake`, "P11_KIT_SERVER_ADDRESS": "x", "OPENSSL_CONF": "y",
		"UNI_VPN_STATE": "dropped by TrustedEnv, set again by env"}
	env := TrustedEnv(current, `C:\Windows\system32`, `C:\Windows`)
	if env["ComSpec"] != filepath.Join(`C:\Windows\system32`, "cmd.exe") || env["SystemRoot"] != `C:\Windows` ||
		!strings.HasPrefix(env["PATH"], `C:\Windows\system32;`) || env["TEMP"] != `C:\T` {
		t.Fatal(env)
	}
	for _, name := range []string{"Path", "P11_KIT_SERVER_ADDRESS", "OPENSSL_CONF", "UNI_VPN_STATE"} {
		if _, ok := env[name]; ok {
			t.Fatal(name)
		}
	}
}

func TestTrustedEnvLooksUpNamesCaseInsensitively(t *testing.T) {
	// Python's os.environ upper-cases the names on Windows.
	env := TrustedEnv(map[string]string{"PROGRAMFILES(X86)": `C:\P86`, "COMPUTERNAME": "pc"}, `C:\W\s`, `C:\W`)
	if env["ProgramFiles(x86)"] != `C:\P86` || env["COMPUTERNAME"] != "pc" || env["PATHEXT"] != ".COM;.EXE;.BAT;.CMD;.VBS;.JS;.WSF" {
		t.Fatal(env)
	}
}

func TestEnvMapRoundTrip(t *testing.T) {
	m := envMap([]string{"A=1", "=C:=C:\\x", "B=x=y", "bad"})
	if m["A"] != "1" || m["=C:"] != "C:\\x" || m["B"] != "x=y" || len(m) != 3 {
		t.Fatal(m)
	}
	if got := envList(map[string]string{"B": "2", "A": "1"}); !slices.Equal(got, []string{"A=1", "B=2"}) {
		t.Fatal(got)
	}
}

func TestRemoveStaleStateFiles(t *testing.T) {
	tmp := t.TempDir()
	for _, name := range []string{"tunnel-123.env", "daemon.log"} {
		if err := os.WriteFile(filepath.Join(tmp, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if n := RemoveStaleStateFiles(tmp); n != 1 {
		t.Fatal(n)
	}
	entries, _ := os.ReadDir(tmp)
	if len(entries) != 1 || entries[0].Name() != "daemon.log" {
		t.Fatal(entries)
	}
	if RemoveStaleStateFiles(filepath.Join(tmp, "missing")) != 0 {
		t.Fatal()
	}
}

func TestRunCtrlCHelperIgnoresOtherArgs(t *testing.T) {
	RunCtrlCHelper([]string{"uni-vpn", "daemon"})
	RunCtrlCHelper([]string{"uni-vpn"})
	if runtime.GOOS != "windows" && defaultCtrlC(1) {
		t.Fatal("no Ctrl+C outside Windows")
	}
}
