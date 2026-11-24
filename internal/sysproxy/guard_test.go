package sysproxy

import (
	"errors"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

type fakeGuard struct {
	got    []string
	answer string
	err    error
}

func (g *fakeGuard) ask(request string) (string, error) {
	g.got = append(g.got, request)
	return g.answer, g.err
}

// guardedMac: a standard account's Mac (networksetup refuses every change) with the guard.
func guardedMac(r *runner, e *elevated, g *fakeGuard) *System {
	s := standardMac(r, e)
	s.Guard = g.ask
	return s
}

func changes(r *runner) [][]string {
	var got [][]string
	for _, c := range r.calls {
		if len(c) > 1 && strings.HasPrefix(c[1], "-set") {
			got = append(got, c)
		}
	}
	return got
}

// The guard switches the rule on: setup only backs up and tells it, and asks nobody for a
// password, also for a service with no rule and one with ours switched off.
func TestGuardedInstallLeavesTheRuleToTheGuard(t *testing.T) {
	e, g := &elevated{}, &fakeGuard{answer: "on 7"}
	r := &runner{answers: standardAnswers(
		answer{"networksetup -getautoproxyurl USB LAN", 0, "URL: http://127.0.0.1:1081/proxy.pac?v=5\nEnabled: No\n"})}
	backup := tempBackup(t, "b.json")
	got, err := guardedMac(r, e, g).Install(1081, backup)
	if err != nil || got != "ok" || len(e.cmds) != 0 || len(changes(r)) != 0 {
		t.Fatal(got, err, e.cmds, changes(r))
	}
	if !reflect.DeepEqual(g.got, []string{"want 1081"}) {
		t.Fatal(g.got)
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatal("no backup:", err)
	}
}

// A setting that is not ours stays the user's to replace: with an administrator's password on
// a standard account, at the guard's address; then the guard looks again.
func TestGuardedInstallReplacesAnotherSettingWithThePassword(t *testing.T) {
	e, g := &elevated{}, &fakeGuard{answer: "on 7"}
	r := &runner{answers: standardAnswers(
		answer{"networksetup -getautoproxyurl Wi-Fi", 0, "URL: http://proxy.example/pac\nEnabled: Yes\n"})}
	got, _ := guardedMac(r, e, g).Install(1081, tempBackup(t, "b.json"))
	want := [][]string{{"networksetup", "-setautoproxyurl", "Wi-Fi", "http://127.0.0.1:1081/proxy.pac?v=7"}}
	if got != "replaced" || !reflect.DeepEqual(e.cmds, want) || len(g.got) != 2 {
		t.Fatal(got, e.cmds, g.got)
	}
	// Cancelled: failed, as before.
	e, g = &elevated{err: errors.New("cancelled")}, &fakeGuard{answer: "off"}
	r = &runner{answers: standardAnswers(
		answer{"networksetup -getautoproxyurl Wi-Fi", 0, "URL: http://proxy.example/pac\nEnabled: No\n"})}
	if got, _ := guardedMac(r, e, g).Install(1081, tempBackup(t, "b.json")); got != "failed" {
		t.Fatal(got)
	}
}

// A guard that does not answer (stopped, an older Mac): everything as before.
func TestGuardedInstallWithoutAnAnswerWorksAsBefore(t *testing.T) {
	e, g := &elevated{}, &fakeGuard{err: errors.New("no guard")}
	r := &runner{answers: standardAnswers()}
	if got, _ := guardedMac(r, e, g).Install(1081, tempBackup(t, "b.json")); got != "ok" || len(e.cmds) != 2 {
		t.Fatal(got, e.cmds)
	}
}

// Uninstall: the guard switches ours off; only what was not ours is put back, and only where it
// differs now.
func TestGuardedUninstallPutsBackOnlyWhatWasNotOurs(t *testing.T) {
	backup := tempBackup(t, "b.json")
	saved := `{"services": {"Wi-Fi": {"url": "http://proxy.example/pac", "enabled": true}, ` +
		`"USB LAN": {"url": "", "enabled": false}, "Dock": {"url": "http://proxy.example/old", "enabled": false}}}`
	if err := os.WriteFile(backup, []byte(saved), 0o600); err != nil {
		t.Fatal(err)
	}
	e, g := &elevated{}, &fakeGuard{answer: "off"}
	r := &runner{answers: standardAnswers(
		answer{"networksetup -listallnetworkservices", 0, "An asterisk (*) denotes that a network service is disabled.\nWi-Fi\nUSB LAN\nDock\n"},
		answer{"networksetup -getautoproxyurl Wi-Fi", 0, "URL: http://127.0.0.1:1081/proxy.pac?v=7\nEnabled: No\n"},
		answer{"networksetup -getautoproxyurl Dock", 0, "URL: http://proxy.example/old\nEnabled: No\n"})}
	got, err := guardedMac(r, e, g).Uninstall(backup)
	want := [][]string{{"networksetup", "-setautoproxyurl", "Wi-Fi", "http://proxy.example/pac"}}
	if err != nil || got != "restored" || !reflect.DeepEqual(e.cmds, want) || !reflect.DeepEqual(g.got, []string{"unwant"}) {
		t.Fatal(got, err, e.cmds, g.got)
	}
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Fatal("backup kept:", err)
	}
	// Nothing that was not ours: no password at all.
	if err := os.WriteFile(backup, []byte(`{"services": {"Wi-Fi": {"url": "", "enabled": false}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	e, g = &elevated{}, &fakeGuard{answer: "off"}
	r = &runner{answers: standardAnswers()}
	if got, _ := guardedMac(r, e, g).Uninstall(backup); got != "restored" || len(e.cmds) != 0 || len(changes(r)) != 0 {
		t.Fatal(got, e.cmds, changes(r))
	}
}

// The core's own looks at the rule only tell the guard.
func TestGuardedRefreshCoverAndWithdrawTellTheGuard(t *testing.T) {
	g := &fakeGuard{answer: "on 7"}
	r := &runner{answers: standardAnswers(
		answer{"networksetup -getautoproxyurl", 0, "URL: http://127.0.0.1:1081/proxy.pac?v=5\nEnabled: Yes\n"})}
	s := guardedMac(r, nil, g)
	s.Refresh(1081)
	s.Cover(1081, tempBackup(t, "b.json"))
	if !s.Withdraw(1081) {
		t.Fatal("Withdraw: the guard switches it off")
	}
	if !reflect.DeepEqual(g.got, []string{"refresh", "want 1081", "state"}) || len(r.calls) != 0 {
		t.Fatal(g.got, r.calls)
	}
	// Without an answer, as before.
	g = &fakeGuard{err: errors.New("no guard")}
	r = &runner{answers: []answer{
		{"networksetup -listallnetworkservices", 0, "An asterisk (*) denotes that a network service is disabled.\nWi-Fi\n"},
		{"networksetup -getautoproxyurl Wi-Fi", 0, "URL: http://127.0.0.1:1081/proxy.pac?v=5\nEnabled: Yes\n"},
	}}
	s = guardedMac(r, nil, g)
	if !s.Withdraw(1081) || !r.called("networksetup", "-setautoproxystate", "Wi-Fi", "off") {
		t.Fatal(r.calls)
	}
}

// Another local tool's rule (a proxy app's PAC at 127.0.0.1:1089/proxy.pac) is not ours: only
// uni-vpn writes ?v=, and the plain address only at 1081.
func TestLooksOurs(t *testing.T) {
	for url, want := range map[string]bool{
		"http://127.0.0.1:1081/proxy.pac":        true,
		"http://127.0.0.1:2081/proxy.pac?v=17":   true,
		"http://127.0.0.1:1089/proxy.pac":        false,
		"http://127.0.0.1:10810/proxy.pac":       false,
		"http://127.0.0.1:1081/proxy.pac?v=x":    false,
		"http://localhost:1081/proxy.pac":        false,
		"http://127.0.0.1:1081/other.pac":        false,
		"http://127.0.0.1:1081/proxy.pac?v=1&a=": false,
	} {
		if LooksOurs(url) != want {
			t.Error(url)
		}
	}
}

// Audit 9 (core): setup replaced another tool's local rule; uninstall puts it back.
func TestGuardedUninstallPutsBackAnotherToolsLocalRule(t *testing.T) {
	backup := tempBackup(t, "b.json")
	if err := os.WriteFile(backup, []byte(`{"services": {"Wi-Fi": {"url": "http://127.0.0.1:1089/proxy.pac", "enabled": true}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	e, g := &elevated{}, &fakeGuard{answer: "off"}
	r := &runner{answers: standardAnswers(
		answer{"networksetup -getautoproxyurl Wi-Fi", 0, "URL: http://127.0.0.1:1081/proxy.pac?v=7\nEnabled: No\n"})}
	got, err := guardedMac(r, e, g).Uninstall(backup)
	want := [][]string{{"networksetup", "-setautoproxyurl", "Wi-Fi", "http://127.0.0.1:1089/proxy.pac"}}
	if err != nil || got != "restored" || !reflect.DeepEqual(e.cmds, want) {
		t.Fatal(got, err, e.cmds)
	}
}

// Without a backup (or a broken one) the guard still hears that this account no longer wants the
// rule.
func TestGuardedUninstallWithoutABackupStillUnwants(t *testing.T) {
	for _, data := range []string{"", "{broken"} {
		backup := tempBackup(t, "b.json")
		if data != "" {
			if err := os.WriteFile(backup, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		g := &fakeGuard{answer: "off"}
		got, _ := guardedMac(&runner{answers: standardAnswers()}, &elevated{}, g).Uninstall(backup)
		if got != "unset" || !reflect.DeepEqual(g.got, []string{"unwant"}) {
			t.Fatal(data, got, g.got)
		}
	}
}

// The guard's version: current, older (it does not know "version", or names a smaller one) or
// not running.
func TestGuardState(t *testing.T) {
	ask := func(answers map[string]string) GuardClient {
		return func(request string) (string, error) {
			if a, ok := answers[request]; ok {
				return a, nil
			}
			return "", errors.New("the guard refused " + request)
		}
	}
	current := strconv.Itoa(GuardVersion)
	older := strconv.Itoa(GuardVersion - 1)
	for _, c := range []struct {
		answers          map[string]string
		running, current bool
	}{
		{map[string]string{"version": current, "state": "off"}, true, true},
		{map[string]string{"version": strconv.Itoa(GuardVersion + 1), "state": "off"}, true, true},
		{map[string]string{"version": older, "state": "off"}, true, false},
		{map[string]string{"state": "on 7"}, true, false},
		{map[string]string{}, false, false},
	} {
		if running, cur := GuardState(ask(c.answers)); running != c.running || cur != c.current {
			t.Error(c.answers, running, cur)
		}
	}
	if running, cur := GuardState(nil); running || cur {
		t.Error("nil:", running, cur)
	}
}
