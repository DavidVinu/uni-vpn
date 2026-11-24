package guard

import (
	"bufio"
	"errors"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DavidVinu/uni-vpn/internal/sysproxy"
)

type service struct {
	url     string
	enabled bool
}

// mac is a Mac as the guard sees it: who is at the screen, who listens where, and a
// networksetup that keeps each service's setting.
type mac struct {
	mu        sync.Mutex
	console   uint32 // 0: the login window
	listeners map[int]uint32
	listenErr error
	names     []string
	services  map[string]*service
	changes   [][]string
	now       time.Time
	saved     []byte
}

func newMac(services ...string) *mac {
	m := &mac{listeners: map[int]uint32{}, services: map[string]*service{}, now: time.Unix(1_800_000_000, 0)}
	for i := 0; i+1 < len(services); i += 2 {
		m.names = append(m.names, services[i])
		s := &service{}
		if url := services[i+1]; url != "" {
			s.url, s.enabled = strings.TrimPrefix(url, "off:"), !strings.HasPrefix(url, "off:")
		}
		m.services[services[i]] = s
	}
	return m
}

func (m *mac) run(args []string, _ time.Duration) (sysproxy.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if args[0] != "networksetup" {
		return sysproxy.Result{Code: 1}, nil
	}
	switch args[1] {
	case "-listallnetworkservices":
		return sysproxy.Result{Stdout: "An asterisk (*) denotes that a network service is disabled.\n" + strings.Join(m.names, "\n") + "\n"}, nil
	case "-getautoproxyurl":
		s := m.services[args[2]]
		url, on := "(null)", "No"
		if s.url != "" {
			url = s.url
		}
		if s.enabled {
			on = "Yes"
		}
		return sysproxy.Result{Stdout: "URL: " + url + "\nEnabled: " + on + "\n"}, nil
	case "-setautoproxyurl":
		m.changes = append(m.changes, args)
		*m.services[args[2]] = service{url: args[3], enabled: true}
	case "-setautoproxystate":
		m.changes = append(m.changes, args)
		m.services[args[2]].enabled = args[3] == "on"
	}
	return sysproxy.Result{}, nil
}

func (m *mac) world() World {
	return World{
		Console: func() (uint32, bool) { return m.console, true },
		Listener: func(port int) (uint32, bool, error) {
			uid, ok := m.listeners[port]
			return uid, ok, m.listenErr
		},
		Run:  m.run,
		Now:  func() time.Time { return m.now },
		Save: func(data []byte) error { m.saved = data; return nil },
		Log:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func (m *mac) state(name string) string {
	s := m.services[name]
	if s.url == "" {
		return ""
	}
	if !s.enabled {
		return "off:" + s.url
	}
	return s.url
}

func (m *mac) took() int {
	n := len(m.changes)
	m.changes = nil
	return n
}

const (
	student = 501
	sibling = 502
	ours    = "http://127.0.0.1:1081/proxy.pac?v=1800000000"
)

// The rule is on for the account at the screen while its own program holds its port: a
// service with no rule and one with ours switched off get it, another setting stays.
func TestTheRuleIsOnForTheAccountAtTheScreenOnly(t *testing.T) {
	m := newMac("Wi-Fi", "", "USB LAN", "off:http://127.0.0.1:1081/proxy.pac?v=5", "Work", "http://proxy.example/pac")
	m.console, m.listeners[1081] = student, student
	g := New(m.world(), nil)
	if got := g.Handle(student, "want 1081"); got != "on 1800000000" {
		t.Fatal(got)
	}
	if m.state("Wi-Fi") != ours || m.state("USB LAN") != ours || m.state("Work") != "http://proxy.example/pac" {
		t.Fatal(m.services["Wi-Fi"], m.services["USB LAN"], m.services["Work"])
	}
	// Nothing changed: nothing set.
	m.took()
	g.Look()
	if n := m.took(); n != 0 {
		t.Fatal("set again:", n)
	}
}

// SV-1: another account's program takes the port (the student's core is not running): the
// rule goes off at the next look, whoever the student is, and the student hears "held".
func TestAnotherAccountsProgramOnThePortSwitchesTheRuleOff(t *testing.T) {
	m := newMac("Wi-Fi", "", "Work", "http://proxy.example/pac")
	m.console, m.listeners[1081] = student, student
	g := New(m.world(), nil)
	g.Handle(student, "want 1081")
	m.listeners[1081] = sibling
	g.Look()
	if m.state("Wi-Fi") != "off:"+ours || m.state("Work") != "http://proxy.example/pac" {
		t.Fatal(m.services["Wi-Fi"], m.services["Work"])
	}
	if got := g.Handle(student, "state"); got != "held" {
		t.Fatal(got)
	}
	// The sibling asking changes nothing: it is not at the screen.
	if got := g.Handle(sibling, "want 1081"); got != "off" || m.state("Wi-Fi") != "off:"+ours {
		t.Fatal(got, m.services["Wi-Fi"])
	}
	// Its own program again: on.
	m.listeners[1081] = student
	g.Look()
	if m.state("Wi-Fi") != ours {
		t.Fatal(m.services["Wi-Fi"])
	}
}

// Nobody with Uni VPN at the screen: off. The login window, an account that never asked,
// nothing on the port, a table that cannot be read.
func TestTheRuleIsOffForEveryoneElse(t *testing.T) {
	for name, change := range map[string]func(m *mac){
		"login window":    func(m *mac) { m.console = 0 },
		"never asked":     func(m *mac) { m.console = sibling; m.listeners[1081] = sibling },
		"nothing listens": func(m *mac) { delete(m.listeners, 1081) },
		"table unknown":   func(m *mac) { m.listenErr = errors.New("sysctl") },
	} {
		m := newMac("Wi-Fi", "")
		m.console, m.listeners[1081] = student, student
		g := New(m.world(), nil)
		g.Handle(student, "want 1081")
		change(m)
		g.Look()
		if m.state("Wi-Fi") != "off:"+ours {
			t.Error(name, m.services["Wi-Fi"])
		}
	}
}

// Set up before the guard: our rule at 1081 is ours before anyone asks, and goes off.
func TestARuleFromBeforeTheGuardIsOurs(t *testing.T) {
	m := newMac("Wi-Fi", "http://127.0.0.1:1081/proxy.pac?v=5", "Lab", "http://127.0.0.1:2081/proxy.pac")
	m.console = student
	New(m.world(), nil).Look()
	if m.state("Wi-Fi") != "off:http://127.0.0.1:1081/proxy.pac?v=5" || m.state("Lab") != "http://127.0.0.1:2081/proxy.pac" {
		t.Fatal(m.services["Wi-Fi"], m.services["Lab"])
	}
}

// A port that changed, or an account that no longer wants the rule: the old address is still
// known as ours, also after a restart.
func TestOldPortsStayOurs(t *testing.T) {
	m := newMac("Wi-Fi", "")
	m.console, m.listeners[2081], m.listeners[3081] = student, student, student
	g := New(m.world(), nil)
	g.Handle(student, "want 2081")
	g.Handle(student, "want 3081")
	if m.state("Wi-Fi") != "http://127.0.0.1:3081/proxy.pac?v=1800000001" {
		t.Fatal(m.services["Wi-Fi"])
	}
	g = New(m.world(), m.saved)
	if got := g.Handle(student, "unwant"); got != "off" || m.state("Wi-Fi") != "off:http://127.0.0.1:3081/proxy.pac?v=1800000001" {
		t.Fatal(got, m.services["Wi-Fi"])
	}
	*m.services["Wi-Fi"] = service{url: "http://127.0.0.1:2081/proxy.pac?v=9", enabled: true}
	New(m.world(), m.saved).Look()
	if m.state("Wi-Fi") != "off:http://127.0.0.1:2081/proxy.pac?v=9" {
		t.Fatal(m.services["Wi-Fi"])
	}
}

// refresh: a new address each time, also twice within one second; kept after a restart.
func TestRefreshGivesANewAddress(t *testing.T) {
	m := newMac("Wi-Fi", "")
	m.console, m.listeners[1081] = student, student
	g := New(m.world(), nil)
	g.Handle(student, "want 1081")
	a, b := g.Handle(student, "refresh"), g.Handle(student, "refresh")
	if a != "on 1800000001" || b != "on 1800000002" || m.state("Wi-Fi") != "http://127.0.0.1:1081/proxy.pac?v=1800000002" {
		t.Fatal(a, b, m.services["Wi-Fi"])
	}
	if got := New(m.world(), m.saved).Handle(student, "state"); got != "on 1800000002" {
		t.Fatal(got)
	}
	if got := g.Handle(sibling, "refresh"); got != "off" {
		t.Fatal("an account that never asked:", got)
	}
}

func TestRequestsItDoesNotKnow(t *testing.T) {
	m := newMac("Wi-Fi", "")
	m.console, m.listeners[1081] = student, student
	g := New(m.world(), nil)
	for _, request := range []string{"", "want", "want 80", "want 70000", "want x", "want 1081 1082", "set Wi-Fi http://evil/pac", "refresh now"} {
		if got := g.Handle(student, request); got != "error" {
			t.Errorf("%q: %s", request, got)
		}
	}
	if got := g.Handle(0, "want 1081"); got != "error" {
		t.Error("root:", got)
	}
	if m.state("Wi-Fi") != "" || m.saved != nil {
		t.Fatal(m.services["Wi-Fi"], string(m.saved))
	}
}

// It reads the settings again after a minute: a service added since gets the rule.
func TestAServiceAddedSinceGetsTheRuleWithinAMinute(t *testing.T) {
	m := newMac("Wi-Fi", "")
	m.console, m.listeners[1081] = student, student
	g := New(m.world(), nil)
	g.Handle(student, "want 1081")
	m.names = append(m.names, "iPhone USB")
	m.services["iPhone USB"] = &service{}
	g.Look()
	if m.state("iPhone USB") != "" {
		t.Fatal("read again before a minute passed")
	}
	m.now = m.now.Add(Every)
	g.Look()
	if m.state("iPhone USB") != ours {
		t.Fatal(m.services["iPhone USB"])
	}
}

func TestBrokenStateFileStartsEmpty(t *testing.T) {
	m := newMac("Wi-Fi", "")
	for _, data := range []string{"", "{", `{"accounts": {"x": {"port": 1081}, "501": {"port": 80}}, "ports": [1, 99999]}`} {
		g := New(m.world(), []byte(data))
		if len(g.accounts) != 0 || len(g.ports) != 1 || g.ports[0] != DefaultPort {
			t.Errorf("%q: %v %v", data, g.accounts, g.ports)
		}
	}
}

// The socket: the peer's account, one line, answered; a long line or none gets no answer.
func TestServeAnswersOneLinePerConnection(t *testing.T) {
	m := newMac("Wi-Fi", "")
	m.console, m.listeners[1081] = student, student
	g := New(m.world(), nil)
	path := filepath.Join(t.TempDir(), "guard.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Skip("no Unix sockets here:", err)
	}
	defer l.Close()
	go func() { _ = g.Serve(l, func(net.Conn) (uint32, error) { return student, nil }) }()
	ask := func(send string) string {
		c, err := net.Dial("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		_, _ = c.Write([]byte(send))
		line, _ := bufio.NewReader(c).ReadString('\n')
		return line
	}
	if got := ask("want 1081\n"); got != "on 1800000000\n" {
		t.Fatalf("%q", got)
	}
	if got := ask(strings.Repeat("x", 200) + "\n"); got != "" {
		t.Fatalf("long line: %q", got)
	}
	start := time.Now()
	if got := ask("want 1081"); got != "" || time.Since(start) > 4*time.Second {
		t.Fatalf("no newline: %q after %s", got, time.Since(start))
	}
}

// A failed accept (too many open files) does not leave the guard running without answers;
// only a closed socket ends Serve.
func TestServeKeepsAnsweringAfterAFailedAccept(t *testing.T) {
	m := newMac("Wi-Fi", "")
	m.console, m.listeners[1081] = student, student
	g := New(m.world(), nil)
	l := &flakyListener{conns: make(chan net.Conn), fails: 3}
	done := make(chan error, 1)
	go func() { done <- g.Serve(l, func(net.Conn) (uint32, error) { return student, nil }) }()
	mine, theirs := net.Pipe()
	defer mine.Close()
	l.conns <- theirs
	_ = mine.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = mine.Write([]byte("want 1081\n"))
	if line, _ := bufio.NewReader(mine).ReadString('\n'); line != "on 1800000000\n" {
		t.Fatalf("%q", line)
	}
	close(l.conns)
	select {
	case err := <-done:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not end with the socket")
	}
}

type flakyListener struct {
	conns chan net.Conn
	fails int
}

func (l *flakyListener) Accept() (net.Conn, error) {
	if l.fails > 0 {
		l.fails--
		return nil, errors.New("accept: too many open files")
	}
	c, ok := <-l.conns
	if !ok {
		return nil, net.ErrClosed
	}
	return c, nil
}
func (l *flakyListener) Close() error   { return nil }
func (l *flakyListener) Addr() net.Addr { return &net.UnixAddr{Name: "fake", Net: "unix"} }

// Set up without the app (get.sh, a clone): nothing goes to the Trash, so the guard ends once
// the last account that wanted the rule no longer does.
func TestTheLastUnwantEndsAGuardWithoutTheApp(t *testing.T) {
	m := newMac("Wi-Fi", "")
	m.console, m.listeners[1081] = student, student
	g := New(m.world(), nil)
	if g.Abandoned() {
		t.Fatal("abandoned before anyone asked")
	}
	g.Handle(student, "want 1081")
	g.Handle(sibling, "want 2081")
	g.Handle(student, "unwant")
	if g.Abandoned() {
		t.Fatal("abandoned while another account wants it")
	}
	g.Handle(sibling, "unwant")
	if !g.Abandoned() || m.state("Wi-Fi") == ours {
		t.Fatal(g.Abandoned(), m.services["Wi-Fi"])
	}
	// Wanted again before it went: not abandoned.
	g.Handle(student, "want 1081")
	if g.Abandoned() {
		t.Fatal("abandoned after a new want")
	}
}

func TestPlistRunsTheHelperAsTheGuard(t *testing.T) {
	for _, app := range []bool{false, true} {
		p := Plist(app)
		for _, want := range []string{"<string>" + Label + "</string>", "<string>" + Helper + "</string>", "<string>guard</string>", "<key>KeepAlive</key>"} {
			if !strings.Contains(p, want) {
				t.Error(app, want)
			}
		}
		// Only a guard set up from the app watches for it in the Trash.
		if strings.Contains(p, "<string>--app</string>") != app {
			t.Error(app, p)
		}
	}
}
