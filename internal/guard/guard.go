// Package guard is the Mac's guard of the proxy rule (docs/mac-guard.md). The rule is the
// computer's and points at a port that any account may take, and macOS lets only an
// administrator change it. The guard runs as root and switches our rule on only for the account
// at the screen, and only while the program that listens on that account's status port is that
// account's. Accounts tell it over a Unix socket that they want the rule; it decides from what
// it sees.
package guard

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DavidVinu/uni-vpn/internal/sysproxy"
)

const (
	Label     = "de.davidvinu.uni-vpn.guard"
	Helper    = "/Library/PrivilegedHelperTools/" + Label
	PlistPath = "/Library/LaunchDaemons/" + Label + ".plist"
	StatePath = "/Library/Application Support/Uni VPN/guard.json"
	LogPath   = "/Library/Logs/uni-vpn-guard.log"
	// PackageCore is the core in the app the .pkg installs: gone, the app went to the Trash.
	PackageCore = "/Applications/Uni VPN.app/Contents/Resources/app/bin/uni-vpn-core"

	// DefaultPort: a rule at it is ours also before any account asked for one (set up before
	// the guard).
	DefaultPort = 1081

	maxRequest = 64
	maxConns   = 8
	readWithin = 2 * time.Second
	// Tick: how often it looks at the screen's account and the listeners; Every: how often it
	// reads the settings anyway (a service added since, a change by someone else).
	Tick  = 2 * time.Second
	Every = time.Minute
	// Gone: how long the app must be missing before the guard removes itself.
	Gone     = 10 * time.Minute
	maxPorts = 16
)

// World is what the guard looks at and changes; tests replace it.
type World struct {
	// Console is the account at the screen (the owner of /dev/console); ok false for none.
	Console func() (uid uint32, ok bool)
	// Listener is the account of the program that listens on 127.0.0.1:port.
	Listener func(port int) (uid uint32, found bool, err error)
	// Run runs networksetup (as root).
	Run sysproxy.Runner
	Now func() time.Time
	// Save writes the accounts' entries (StatePath).
	Save func(data []byte) error
	Log  *slog.Logger
}

type entry struct {
	Port    int   `json:"port"`
	Version int64 `json:"version"`
}

type saved struct {
	Accounts map[string]entry `json:"accounts"`
	Ports    []int            `json:"ports"`
}

// Guard holds the accounts that want the rule and what it set last.
type Guard struct {
	mu       sync.Mutex
	w        World
	accounts map[uint32]entry
	ports    []int  // every port our rule had, newest last
	applied  string // what it set last ("on P V" or "off"), "" before the first look
	lastRead time.Time
}

// New starts from data (StatePath), nil or broken for none.
func New(w World, data []byte) *Guard {
	g := &Guard{w: w, accounts: map[uint32]entry{}, ports: []int{DefaultPort}}
	var s saved
	if json.Unmarshal(data, &s) == nil {
		for key, e := range s.Accounts {
			if uid, err := strconv.ParseUint(key, 10, 32); err == nil && validPort(e.Port) {
				g.accounts[uint32(uid)] = e
			}
		}
		for _, p := range s.Ports {
			g.addPort(p)
		}
	}
	return g
}

func validPort(p int) bool { return p >= 1024 && p <= 65535 }

func (g *Guard) addPort(p int) {
	if !validPort(p) {
		return
	}
	g.ports = append(slices.DeleteFunc(g.ports, func(q int) bool { return q == p }), p)
	if len(g.ports) > maxPorts {
		g.ports = g.ports[len(g.ports)-maxPorts:]
	}
}

// decision is what the rule should be: on at port and version, for the account at the screen.
type decision struct {
	on      bool
	uid     uint32 // the account at the screen, 0 for none
	held    bool   // another account's program listens on its port
	port    int
	version int64
}

func (d decision) key() string {
	if d.on {
		return fmt.Sprintf("on %d %d", d.port, d.version)
	}
	return "off"
}

func (g *Guard) decideLocked() decision {
	uid, ok := g.w.Console()
	if !ok || uid == 0 {
		return decision{}
	}
	e, ok := g.accounts[uid]
	if !ok {
		return decision{uid: uid}
	}
	owner, found, err := g.w.Listener(e.Port)
	if err != nil || !found || owner != uid {
		// Unknown counts as not this account's.
		return decision{uid: uid, held: err == nil && found, port: e.Port}
	}
	return decision{on: true, uid: uid, port: e.Port, version: e.Version}
}

func (g *Guard) ours(url string) bool {
	for _, p := range g.ports {
		if sysproxy.IsOurs(url, p) {
			return true
		}
	}
	return false
}

// commands sets the rule to d: on, every enabled service whose rule is ours or unset gets our
// address; off, every service with our rule on gets it switched off. A setting that is not
// ours is never touched.
func (g *Guard) commands(snap *sysproxy.Snapshot, d decision) [][]string {
	url := sysproxy.PacURLVersion(d.port, d.version)
	var cmds [][]string
	for _, svc := range snap.Mac.Services {
		ours := g.ours(svc.URL)
		switch {
		case d.on && svc.Enabled && svc.URL == url:
		case d.on && (svc.URL == "" || ours):
			cmds = append(cmds, []string{"networksetup", "-setautoproxyurl", svc.Name, url})
		case !d.on && svc.Enabled && ours:
			cmds = append(cmds, []string{"networksetup", "-setautoproxystate", svc.Name, "off"})
		}
	}
	return cmds
}

// setLocked reads the settings and changes what differs from d.
func (g *Guard) setLocked(d decision) {
	g.lastRead = g.w.Now()
	g.applied = d.key()
	snap := (&sysproxy.System{GOOS: "darwin", Run: g.w.Run}).Current()
	if snap == nil || snap.Mac == nil {
		g.w.Log.Error("Could not read the network settings")
		return
	}
	for _, c := range g.commands(snap, d) {
		r, err := g.w.Run(c, sysproxy.Timeout)
		if err == nil && r.Code != 0 {
			err = fmt.Errorf("exit status %d: %s", r.Code, strings.TrimSpace(r.Stdout+r.Stderr))
		}
		if err != nil {
			g.w.Log.Error("networksetup failed", "args", strings.Join(c[1:], " "), "err", err)
		} else {
			g.w.Log.Info("Set", "args", strings.Join(c[1:], " "))
		}
	}
}

// Look is the guard's regular look (every Tick): it changes the settings when the rule should
// change, and reads them at least every Every.
func (g *Guard) Look() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.lookLocked(false)
}

func (g *Guard) lookLocked(force bool) {
	d := g.decideLocked()
	if force || d.key() != g.applied || g.w.Now().Sub(g.lastRead) >= Every {
		if d.key() != g.applied {
			g.w.Log.Info("The rule for the websites", "is", d.key(), "account", d.uid)
		}
		g.setLocked(d)
	}
}

// Off switches our rule off everywhere (the guard removes itself).
func (g *Guard) Off() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.setLocked(decision{})
}

func (g *Guard) saveLocked() {
	s := saved{Accounts: map[string]entry{}, Ports: g.ports}
	for uid, e := range g.accounts {
		s.Accounts[strconv.FormatUint(uint64(uid), 10)] = e
	}
	data, _ := json.Marshal(s)
	if err := g.w.Save(data); err != nil {
		g.w.Log.Error("Could not save the accounts", "err", err)
	}
}

// Handle answers one request of the account uid: "want P", "refresh", "unwant" or "state".
// The answer says what the rule is for that account: "on V", "held" (another account's program
// listens on its port while it is at the screen) or "off"; "error" for a request it does not
// know.
func (g *Guard) Handle(uid uint32, request string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	fields := strings.Fields(request)
	if len(fields) == 0 {
		return "error"
	}
	now := g.w.Now().Unix()
	e, known := g.accounts[uid]
	switch {
	case fields[0] == "want" && len(fields) == 2:
		port, err := strconv.Atoi(fields[1])
		if err != nil || !validPort(port) || uid == 0 {
			return "error"
		}
		if !known || e.Port != port {
			g.accounts[uid] = entry{Port: port, Version: max(now, e.Version+1)}
			g.addPort(port) // ours from now on, also after this account's port changes again
			g.saveLocked()
		}
	case fields[0] == "refresh" && len(fields) == 1:
		if !known {
			return "off"
		}
		// A new address each time, also for more changes within one second.
		e.Version = max(now, e.Version+1)
		g.accounts[uid] = e
		g.saveLocked()
	case fields[0] == "unwant" && len(fields) == 1:
		if known {
			delete(g.accounts, uid)
			g.saveLocked()
		}
	case fields[0] == "state" && len(fields) == 1:
	default:
		return "error"
	}
	// Only a change for the account at the screen changes the settings.
	g.lookLocked(false)
	d := g.decideLocked()
	switch {
	case d.uid != uid:
		return "off"
	case d.on:
		return "on " + strconv.FormatInt(d.version, 10)
	case d.held:
		return "held"
	}
	return "off"
}

// Serve answers each connection of l with one request; peer names the account that connected.
// At most maxConns at once, each within readWithin.
func (g *Guard) Serve(l net.Listener, peer func(net.Conn) (uint32, error)) error {
	slots := make(chan struct{}, maxConns)
	for {
		c, err := l.Accept()
		if err != nil {
			return err
		}
		select {
		case slots <- struct{}{}:
			go func() {
				defer func() { <-slots }()
				g.serve(c, peer)
			}()
		default:
			c.Close()
		}
	}
}

func (g *Guard) serve(c net.Conn, peer func(net.Conn) (uint32, error)) {
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(readWithin))
	uid, err := peer(c)
	if err != nil {
		return
	}
	line, err := bufio.NewReader(io.LimitReader(c, maxRequest+1)).ReadString('\n')
	if err != nil || len(line) > maxRequest {
		return
	}
	answer := g.Handle(uid, strings.TrimSuffix(line, "\n"))
	_ = c.SetWriteDeadline(time.Now().Add(readWithin))
	_, _ = c.Write([]byte(answer + "\n"))
}

// Plist is the LaunchDaemon that runs the guard as root from boot.
func Plist() string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>` + Label + `</string>
  <key>ProgramArguments</key>
  <array>
    <string>` + Helper + `</string>
    <string>guard</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardErrorPath</key>
  <string>` + LogPath + `</string>
</dict>
</plist>
`
}
