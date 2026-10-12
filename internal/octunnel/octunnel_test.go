//go:build libopenconnect

package octunnel

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/DavidVinu/uni-vpn/internal/messages"
	"github.com/DavidVinu/uni-vpn/internal/tunnel"
	"github.com/DavidVinu/uni-vpn/internal/universities"
)

// A made-up account at Heidelberg's real gateway: the login fails with the program's lines.
func TestHeidelbergRejectsMadeUpAccount(t *testing.T) {
	if os.Getenv("UNI_VPN_NET_TESTS") != "1" {
		t.Skip("set UNI_VPN_NET_TESTS=1 to reach Heidelberg's gateway")
	}
	p, ok := universities.Get("heidelberg")
	if !ok {
		t.Fatal("no heidelberg profile")
	}
	var mu sync.Mutex
	var lines []string
	s, err := Connect(Options{
		Host: p.Host, UserAgent: p.Useragent, User: "ab123" + p.UsernameSuffix, AuthGroup: p.Authgroup,
		UserGroup: p.Usergroup, OS: "linux-64", Password: []byte("wrong-password"),
		TOTPSecret: "base32:JBSWY3DPEHPK3PXP", ReconnectTimeout: 60,
		Line: func(l string) { mu.Lock(); lines = append(lines, l); mu.Unlock() },
	})
	if err == nil {
		s.Free()
		t.Fatal("logged in with a made-up account")
	}
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 1 {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	all := strings.Join(lines, "\n")
	t.Log(all)
	if !strings.Contains(all, "Login failed") || !strings.Contains(all, "Failed to complete authentication") {
		t.Fatal("missing the program's lines")
	}
}

// Without a server the lines and the code are the program's too.
func TestUnreachableServer(t *testing.T) {
	var lines []string
	_, err := Connect(Options{Host: "127.0.0.1:1", UserAgent: "AnyConnect Linux_64 5.1.18.314", User: "x",
		Password: []byte("y"), Line: func(l string) { lines = append(lines, l) }})
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 1 {
		t.Fatal(err)
	}
	if last := lines[len(lines)-1]; last != "Failed to complete authentication" {
		t.Fatalf("%q", lines)
	}
}

func TestInvalidSoftToken(t *testing.T) {
	var lines []string
	_, err := Connect(Options{Host: "127.0.0.1:1", User: "x", Password: []byte("y"), TOTPSecret: "base32:!!",
		Line: func(l string) { lines = append(lines, l) }})
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 1 || lines[len(lines)-1] != "Soft token string is invalid" {
		t.Fatal(err, lines)
	}
}

// gateway is a TLS server with a certificate nobody trusts; it records the User-Agent.
func gateway(t *testing.T) (host string, agents func() []string) {
	var mu sync.Mutex
	seen := []string{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.UserAgent())
		mu.Unlock()
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "https://"), func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}

func connectWithTrust(t *testing.T, trust int, host string) ([]string, tunnel.Verdict, bool) {
	old := testTrust
	testTrust = trust
	t.Cleanup(func() { testTrust = old })
	var lines []string
	_, err := Connect(Options{Host: host, UserAgent: "AnyConnect Linux_64 5.1.18.314", User: "x",
		Password: []byte("y"), Line: func(l string) { lines = append(lines, l) }})
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 1 {
		t.Fatal(err)
	}
	c := tunnel.NewClassifier("none")
	for _, l := range lines {
		if v, ok := c.Feed(l); ok {
			return lines, v, true
		}
	}
	return lines, tunnel.Verdict{}, false
}

// On iOS the library's "Server certificate verify failed" comes before the phone's trust
// store has had its say; once that accepts, nothing may read as an untrusted server.
func TestTrustedByThePhoneIsNotUntrusted(t *testing.T) {
	host, agents := gateway(t)
	lines, v, _ := connectWithTrust(t, trustAccept, host)
	if v.Message == messages.ServerUntrusted.Text {
		t.Fatalf("untrusted: %q", lines)
	}
	for _, l := range lines {
		if strings.Contains(strings.ToLower(l), "certificate") {
			t.Fatalf("%q", lines)
		}
	}
	// The name the gateway answers under, which a redirect may change.
	if got := lastTrustHost(); got != "127.0.0.1" {
		t.Fatal(got)
	}
	// Exactly the profile's user agent, like --useragent.
	if got := agents(); len(got) == 0 || got[0] != "AnyConnect Linux_64 5.1.18.314" {
		t.Fatalf("%q", got)
	}
}

func TestRejectedByThePhoneIsUntrusted(t *testing.T) {
	host, _ := gateway(t)
	for _, trust := range []int{trustReject, 0} {
		lines, v, ok := connectWithTrust(t, trust, host)
		if !ok || v.Message != messages.ServerUntrusted.Text ||
			!strings.HasPrefix(strings.Join(lines, "\n"), "POST") ||
			!strings.Contains(strings.Join(lines, "\n"), "Server certificate verify failed") {
			t.Fatalf("%d: %q", trust, lines)
		}
	}
}

func TestQuitReasonOnExit(t *testing.T) {
	var got []string
	o := Options{Line: func(l string) { got = append(got, l) }}
	s := newSession(o)
	s.lines.write("Send BYE packet: Server request\n")
	if code := s.exitCode(-5000); code != 1 || got[len(got)-1] != "Server request; exiting" {
		t.Fatal(code, got)
	}
	s = newSession(o)
	if s.exitCode(-5000); got[len(got)-1] != "Unknown error; exiting." {
		t.Fatal(got)
	}
}
