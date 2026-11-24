package sysproxy

import (
	"bufio"
	"errors"
	"net"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// GuardSocket is where the Mac's guard of the proxy rule answers (internal/guard,
// docs/mac-guard.md): a program of root that switches our rule on only for the account at the
// screen, and only while that account's program holds the status port.
const GuardSocket = "/var/run/de.davidvinu.uni-vpn.guard.sock"

// GuardClient sends one request to the guard and returns its answer ("on V", "off", "held").
type GuardClient func(request string) (string, error)

// AskGuard is the GuardClient of this Mac.
func AskGuard(request string) (string, error) {
	c, err := net.DialTimeout("unix", GuardSocket, 2*time.Second)
	if err != nil {
		return "", err
	}
	defer c.Close()
	// It runs networksetup for each service before it answers.
	_ = c.SetDeadline(time.Now().Add(60 * time.Second))
	if _, err := c.Write([]byte(request + "\n")); err != nil {
		return "", err
	}
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		return "", err
	}
	answer := strings.TrimSpace(line)
	if answer == "" || answer == "error" {
		return "", errors.New("the guard refused " + strconv.Quote(request))
	}
	return answer, nil
}

// guardOnMac is the guard where it runs: Default leaves it nil without one, and everything
// works as before.
func guardOnMac() GuardClient {
	if _, err := os.Stat(GuardSocket); err != nil {
		return nil
	}
	return AskGuard
}

// guarded: the guard decides about our rule on this Mac.
func (s *System) guarded() bool { return s.macOS() && s.Guard != nil }

// guardVersion is V of "on V", else 0.
func guardVersion(answer string) int64 {
	if v, ok := strings.CutPrefix(answer, "on "); ok {
		n, _ := strconv.ParseInt(v, 10, 64)
		return n
	}
	return 0
}

var reOurs = regexp.MustCompile(`^http://127\.0\.0\.1:[0-9]+/proxy\.pac(\?v=[0-9]+)?$`)

// LooksOurs: url is the address of a uni-vpn rule, at any port.
func LooksOurs(url string) bool { return reOurs.MatchString(url) }

// installGuarded is Install where the guard runs: it switches our rule on and off itself, so
// only a setting that is not ours is replaced here (as Apply did, with an administrator's
// password where macOS asks). ok is false when the guard did not answer: then Install goes on
// as before.
func (s *System) installGuarded(httpPort int, before *Snapshot) (result string, ok bool) {
	want := "want " + strconv.Itoa(httpPort)
	answer, err := s.Guard(want)
	if err != nil {
		return "", false
	}
	version := guardVersion(answer)
	if version == 0 {
		version = s.Now().Unix()
	}
	var cmds [][]string
	for _, svc := range before.Mac.Services {
		if svc.URL != "" && !IsOurs(svc.URL, httpPort) {
			cmds = append(cmds, []string{"networksetup", "-setautoproxyurl", svc.Name, PacURLVersion(httpPort, version)})
		}
	}
	if len(cmds) > 0 {
		if err := s.macSet(cmds); err != nil {
			return "failed", true
		}
		// Ours now: off again at once where the guard says so.
		_, _ = s.Guard(want)
	}
	if s.isForeign(before, httpPort) {
		return "replaced", true
	}
	return "ok", true
}

// uninstallGuarded is Restore where the guard runs: it switches our rule off once this account
// no longer wants it, so only what was not ours is put back, and only where it differs now.
// ok is false when the guard did not answer.
func (s *System) uninstallGuarded(saved *Snapshot) (err error, ok bool) {
	if _, err := s.Guard("unwant"); err != nil {
		return nil, false
	}
	if saved.Mac == nil {
		return nil, true
	}
	names, err := s.listServices(true)
	known := err == nil && len(names) > 0
	now := map[string]Service{}
	if snap := s.Current(); snap != nil && snap.Mac != nil {
		for _, svc := range snap.Mac.Services {
			now[svc.Name] = svc
		}
	}
	var cmds [][]string
	for _, e := range saved.Mac.Services {
		if e.URL == "" || LooksOurs(e.URL) || (known && !slices.Contains(names, e.Name)) {
			continue
		}
		if svc, ok := now[e.Name]; ok && svc.URL == e.URL && svc.Enabled == e.Enabled {
			continue
		}
		cmds = append(cmds, []string{"networksetup", "-setautoproxyurl", e.Name, e.URL})
		if !e.Enabled {
			cmds = append(cmds, []string{"networksetup", "-setautoproxystate", e.Name, "off"})
		}
	}
	return s.macSet(cmds), true
}
