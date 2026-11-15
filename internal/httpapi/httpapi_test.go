package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"html"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DavidVinu/uni-vpn/internal/i18n"
	"github.com/DavidVinu/uni-vpn/internal/pac"
)

func TestAllowedOrigin(t *testing.T) {
	if !AllowedOrigin("", false, 1081) || !AllowedOrigin("http://127.0.0.1:1081", true, 1081) ||
		!AllowedOrigin("http://localhost:1081", true, 1081) {
		t.Fatal("own origins refused")
	}
	// Without the extension there is no longer any foreign origin allowed to POST. Sandboxed
	// iframes and data: pages send "null": not a trustworthy origin.
	for _, origin := range []string{"http://localhost:9999", "chrome-extension://abcdef", "moz-extension://1234-5678",
		"https://evil.example", "http://127.0.0.1:9999", "null", "chrome-extension://abc\nX-Injected: 1",
		"chrome-extension://abc\r\n", "moz-extension://abc/def", "chrome-extension://a b", "chrome-extension://",
		"xchrome-extension://abc"} {
		if AllowedOrigin(origin, true, 1081) {
			t.Fatal(origin)
		}
	}
}

func TestAllowedHost(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "127.0.0.1:1081", "localhost", "localhost:1081"} {
		if !AllowedHost(host, true, 1081) {
			t.Fatal(host)
		}
	}
	for _, host := range []string{"evil.example:1081", "127.0.0.1:9", "", "LOCALHOST"} {
		if AllowedHost(host, true, 1081) {
			t.Fatal(host)
		}
	}
	if AllowedHost("localhost", false, 1081) {
		t.Fatal("missing Host header accepted")
	}
}

func TestPyStrip(t *testing.T) {
	if got := PyStrip("\x1c  ab c　\t"); got != "ab c" {
		t.Fatalf("%q", got)
	}
}

// pacOnly answers the PAC file; the other routes are not reached in these tests.
type pacOnly struct{ Daemon }

func (pacOnly) PAC() (string, error) {
	return "function FindProxyForURL(url, host) { return \"DIRECT\"; }", nil
}

func get(t *testing.T, s *Server, method, path, extra string) (string, []byte) {
	t.Helper()
	c, err := net.Dial("tcp", s.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte(method + " " + path + " HTTP/1.1\r\nHost: 127.0.0.1\r\n" + extra + "\r\n"))
	data, _ := io.ReadAll(c)
	head, body, _ := bytes.Cut(data, []byte("\r\n\r\n"))
	return string(head), body
}

func TestAnotherAccountGetsOnlyThePAC(t *testing.T) {
	// Fast user switching: the second account's window, app and browser reach the first
	// account's uni-vpn.
	s := New(pacOnly{}, "127.0.0.1", 0, nil)
	s.OtherAccount = func(net.Conn) (bool, error) { return true, nil }
	var seen atomic.Int64
	s.OtherAccountSeen = func() { seen.Add(1) }
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	head, body := get(t, s, "GET", "/?app=1", "Accept-Language: de-DE,en\r\n")
	german := i18n.Catalog("de")["app.other_account"]
	if !strings.HasPrefix(head, "HTTP/1.1 403 ") || !strings.Contains(head, "text/html") || german == "" ||
		!bytes.Contains(body, []byte(german)) {
		t.Fatal(head, string(body))
	}
	// It keeps asking until its own uni-vpn answers, also while the window is hidden: a single
	// reload in the gap after the other account signed out would leave this text for good.
	if !bytes.Contains(body, []byte(`setInterval(`)) || !bytes.Contains(body, []byte(`fetch("/status.json"`)) ||
		!bytes.Contains(body, []byte(`location.reload()`)) || bytes.Contains(body, []byte(`http-equiv="refresh"`)) {
		t.Fatal(string(body))
	}
	for _, req := range [][2]string{{"GET", "/status.json?menu=en"}, {"POST", "/api/sync/offer"}, {"POST", "/api/connect"}} {
		head, body := get(t, s, req[0], req[1], "X-Uni-VPN: 1\r\n")
		if !strings.HasPrefix(head, "HTTP/1.1 403 ") || !bytes.Contains(body, []byte(`"key": "app.other_account"`)) ||
			!bytes.Contains(body, []byte(i18n.T("app.other_account").String())) {
			t.Fatal(req, head, string(body))
		}
	}
	// Its browser goes directly: this account's VPN is not its to use.
	if head, body := get(t, s, "GET", "/proxy.pac", ""); !strings.HasPrefix(head, "HTTP/1.1 200 ") || string(body) != pac.Direct {
		t.Fatal(head, string(body))
	}
	// Each request (its uni-vpn asks for the PAC file after switching the rule off on macOS).
	if seen.Load() != 5 {
		t.Fatal(seen.Load())
	}
}

func TestOwnRequestsAreNotAnotherAccounts(t *testing.T) {
	s := New(pacOnly{}, "127.0.0.1", 0, nil)
	s.OtherAccount = func(net.Conn) (bool, error) { return false, nil }
	var seen atomic.Int64
	s.OtherAccountSeen = func() { seen.Add(1) }
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	if head, _ := get(t, s, "GET", "/proxy.pac", ""); !strings.HasPrefix(head, "HTTP/1.1 200 ") || seen.Load() != 0 {
		t.Fatal(head, seen.Load())
	}
}

// The page this account's app window shows while another account holds the port: in the
// language setting, else as the page picks the system's; nothing in a text ends the script.
func TestOtherAccountWindow(t *testing.T) {
	page := OtherAccountWindow("")
	for _, l := range i18n.Languages {
		text, _ := json.Marshal(i18n.Catalog(l.Code)["app.other_account"])
		if !strings.Contains(page, strings.Trim(string(text), `"`)) {
			t.Fatal(l.Code, page)
		}
	}
	english := html.EscapeString(i18n.Catalog("en")["app.other_account"])
	if !strings.Contains(page, `<p id="t" style="font:16px/1.5 system-ui,sans-serif;margin:2em">`+english+`</p>`) ||
		!strings.Contains(page, `setting = "";`) || !strings.Contains(page, `navigator.languages`) ||
		strings.Count(page, "</script") != 1 || strings.Count(page, "<script") != 1 {
		t.Fatal(page)
	}
	if !strings.Contains(OtherAccountWindow("de"), `setting = "de";`) || !strings.Contains(OtherAccountWindow(`x"; alert(1); "`), `setting = "";`) {
		t.Fatal("setting")
	}
}

func TestOwnerLookupFailsOpen(t *testing.T) {
	var logs bytes.Buffer
	s := New(pacOnly{}, "127.0.0.1", 0, slog.New(slog.NewTextHandler(&logs, nil)))
	s.OtherAccount = func(net.Conn) (bool, error) { return false, errors.New("no table") }
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if head, _ := get(t, s, "GET", "/", ""); !strings.HasPrefix(head, "HTTP/1.1 200 ") {
			t.Fatal(head)
		}
	}
	s.Stop()
	if strings.Count(logs.String(), "Could not tell which account") != 1 {
		t.Fatal(logs.String())
	}
}

// The screenshot limit is for the app only: a request the server is going to refuse (no
// token, a foreign Host or Origin, another account) keeps the small limit, so it is answered
// from its Content-Length alone instead of the server taking 32 MB first.
func TestPictureLimitOnlyForRequestsThatGetThrough(t *testing.T) {
	length := "Content-Length: " + strconv.Itoa(MaxBody+1) + "\r\n"
	for _, c := range []struct {
		name, host, extra string
		token             string
		other             bool
	}{
		{name: "no token", host: "127.0.0.1", token: "t0ken"},
		{name: "wrong token", host: "127.0.0.1", token: "t0ken", extra: "X-Uni-VPN-Token: wrong\r\n"},
		{name: "foreign host", host: "evil.example"},
		{name: "foreign origin", host: "127.0.0.1", extra: "Origin: https://evil.example\r\n"},
		{name: "other account", host: "127.0.0.1", other: true},
	} {
		s := New(pacOnly{}, "127.0.0.1", 0, nil)
		s.Token = c.token
		s.OtherAccount = func(net.Conn) (bool, error) { return c.other, nil }
		if err := s.Start(); err != nil {
			t.Fatal(err)
		}
		conn, err := net.Dial("tcp", s.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		// Only the head: a server waiting for the body does not answer before its read times out.
		conn.Write([]byte("POST " + pictureRoute + " HTTP/1.1\r\nHost: " + c.host + "\r\nX-Uni-VPN: 1\r\n" + length + c.extra + "\r\n"))
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		data, _ := io.ReadAll(conn)
		conn.Close()
		s.Stop()
		if !bytes.HasPrefix(data, []byte("HTTP/1.1 413 ")) {
			t.Errorf("%s: %q", c.name, data)
		}
	}
}

func TestOwnAccountGetsThePage(t *testing.T) {
	// The real lookup, with this test's own connection.
	s := New(pacOnly{}, "127.0.0.1", 0, nil)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	if head, _ := get(t, s, "GET", "/", ""); !strings.HasPrefix(head, "HTTP/1.1 200 ") {
		t.Fatal(head)
	}
}
