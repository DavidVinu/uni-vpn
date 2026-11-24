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
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"

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
	// ServicePlist is each account's own Uni VPN service (internal/service), in
	// ~/Library/LaunchAgents.
	ServicePlist = "de.davidvinu.uni-vpn.plist"

	maxRequest = 64
	// maxConns connections at once, at most maxPerAccount of one account.
	maxConns      = 32
	maxPerAccount = 4
	readWithin    = 2 * time.Second
	// Tick: how often it looks at the screen's account and the listeners; Every: how often it
	// reads the settings anyway (a service added since, a change by someone else).
	Tick  = 2 * time.Second
	Every = time.Minute
	// Gone: how long the app must be missing before the guard removes itself.
	Gone = 10 * time.Minute
	// maxPorts: how many old ports it remembers (a rule left at one is ours to switch off).
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
	ports    []int  // the ports our rule had, newest last (maxPorts)
	applied  string // what it set last ("on P V" or "off"), "" before the first look
	lastRead time.Time
	// again: read the settings at the next look (a want less than a second after a read).
	again bool
	// unread: the last read of the settings failed.
	unread bool
	// abandoned: the last account that wanted the rule no longer does (Abandoned).
	abandoned bool
	// conns: each account's connections now (Serve).
	conns map[uint32]int
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

// ours: url is our rule, at 1081, at the port of an account's entry or at an old port. Old ports
// may be forgotten (any account can name many), the others never.
func (g *Guard) ours(url string) bool {
	if sysproxy.IsOurs(url, DefaultPort) {
		return true
	}
	for _, e := range g.accounts {
		if sysproxy.IsOurs(url, e.Port) {
			return true
		}
	}
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

// setLocked reads the settings and changes what differs from d. Settings it could not read
// (networksetup failed, or named no service: a Mac has one at least) are read again at the
// next look.
func (g *Guard) setLocked(d decision) {
	g.lastRead, g.again = g.w.Now(), false
	snap := (&sysproxy.System{GOOS: "darwin", Run: g.w.Run}).Current()
	if snap == nil || snap.Mac == nil || len(snap.Mac.Services) == 0 {
		if !g.unread {
			g.w.Log.Error("Could not read the network settings")
		}
		g.unread, g.applied = true, ""
		return
	}
	g.unread, g.applied = false, d.key()
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

// lookLocked changes the settings when the rule should change and reads them every Every;
// force reads them now, but at most once a second (else at the next look).
func (g *Guard) lookLocked(force bool) {
	d := g.decideLocked()
	since := g.w.Now().Sub(g.lastRead)
	if (force || g.again) && since < time.Second {
		force, g.again = false, true
	} else if g.again {
		force = true
	}
	if force || d.key() != g.applied || since >= Every {
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

// Adopt gives each account of found (uid: status port) that has no entry one: an account set up
// before the guard, whose core may never ask for the rule (it is older than the guard, and with
// automatic updates off it stays so). guard install adopts every account with a Uni VPN service.
// It returns how many it adopted.
func (g *Guard) Adopt(found map[uint32]int) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for uid, port := range found {
		if _, known := g.accounts[uid]; known || uid == 0 || !validPort(port) {
			continue
		}
		g.accounts[uid] = entry{Port: port, Version: g.w.Now().Unix()}
		g.addPort(port)
		n++
	}
	if n > 0 {
		g.abandoned = false
		g.saveLocked()
	}
	return n
}

// PortOf is the status port in the text of an account's config.toml (http_port), DefaultPort
// where it has none or it cannot be read.
func PortOf(config []byte) int {
	var c struct {
		HTTPPort int64 `toml:"http_port"`
	}
	if _, err := toml.Decode(string(config), &c); err != nil || !validPort(int(c.HTTPPort)) {
		return DefaultPort
	}
	return int(c.HTTPPort)
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

// Handle answers one request of the account uid: "want P", "refresh", "unwant", "state" or
// "version". The answer says what the rule is for that account: "on V", "held" (another
// account's program listens on its port while it is at the screen) or "off"; to "version" the
// guard's version (sysproxy.GuardVersion); "error" for a request it does not know.
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
		g.abandoned = false
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
			g.abandoned = len(g.accounts) == 0
			g.saveLocked()
		}
	case fields[0] == "state" && len(fields) == 1:
	case fields[0] == "version" && len(fields) == 1:
		return strconv.Itoa(sysproxy.GuardVersion)
	default:
		return "error"
	}
	// Only a change for the account at the screen changes the settings. A want reads them
	// again: setup may just have replaced a setting that is not ours with our address.
	g.lookLocked(fields[0] == "want")
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

// Abandoned: the last account that wanted the rule said "unwant" and none asked since. A guard
// set up without the app ends then, since no Trash will tell it.
func (g *Guard) Abandoned() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.abandoned
}

// Serve answers each connection of l with one request; peer names the account that connected.
// At most maxConns at once and maxPerAccount of one account, each within readWithin. Only a
// closed l ends it: after another failed accept (too many open files) it waits a moment and
// goes on.
func (g *Guard) Serve(l net.Listener, peer func(net.Conn) (uint32, error)) error {
	slots := make(chan struct{}, maxConns)
	for {
		c, err := l.Accept()
		if errors.Is(err, net.ErrClosed) {
			return err
		}
		if err != nil {
			g.w.Log.Error("Could not accept", "err", err)
			time.Sleep(100 * time.Millisecond)
			continue
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
	if err != nil || !g.enter(uid) {
		return
	}
	defer g.leave(uid)
	line, err := bufio.NewReader(io.LimitReader(c, maxRequest+1)).ReadString('\n')
	if err != nil || len(line) > maxRequest {
		return
	}
	answer := g.Handle(uid, strings.TrimSuffix(line, "\n"))
	_ = c.SetWriteDeadline(time.Now().Add(readWithin))
	_, _ = c.Write([]byte(answer + "\n"))
}

// enter counts a connection of uid; false when it has maxPerAccount already.
func (g *Guard) enter(uid uint32) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.conns == nil {
		g.conns = map[uint32]int{}
	}
	if g.conns[uid] >= maxPerAccount {
		return false
	}
	g.conns[uid]++
	return true
}

func (g *Guard) leave(uid uint32) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.conns[uid]--; g.conns[uid] <= 0 {
		delete(g.conns, uid)
	}
}

// Plist is the LaunchDaemon that runs the guard as root from boot; app: it was set up from the
// app (PackageCore) and removes itself once the app has gone to the Trash.
func Plist(app bool) string {
	arg := ""
	if app {
		arg = "\n    <string>--app</string>"
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>` + Label + `</string>
  <key>ProgramArguments</key>
  <array>
    <string>` + Helper + `</string>
    <string>guard</string>` + arg + `
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
