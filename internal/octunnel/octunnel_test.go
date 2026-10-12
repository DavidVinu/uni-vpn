//go:build libopenconnect

package octunnel

import (
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

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
