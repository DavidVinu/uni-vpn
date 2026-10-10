//go:build !windows

package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DavidVinu/uni-vpn/internal/config"
	"github.com/DavidVinu/uni-vpn/internal/pyjson"
	unis "github.com/DavidVinu/uni-vpn/internal/universities"
)

// The cases of DaemonUpdateTests in tests/test_updater.py.

const (
	commitA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	commitB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

type fakeUpdater struct {
	mu                         sync.Mutex
	commit                     string
	err                        error
	panics                     bool
	pending                    string
	checks, applied, discarded int
}

func (f *fakeUpdater) Installed() string { return commitA }

func (f *fakeUpdater) Pending() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pending
}

func (f *fakeUpdater) Check(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks++
	if f.panics {
		panic("bug in the updater")
	}
	if f.err != nil {
		return "", f.err
	}
	f.pending = f.commit
	return f.commit, nil
}

func (f *fakeUpdater) Apply() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applied++
	commit := f.pending
	f.pending = ""
	return commit, nil
}

func (f *fakeUpdater) Discard() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.discarded++
	f.pending = ""
}

func (f *fakeUpdater) counts() (checks, applied, discarded int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.checks, f.applied, f.discarded
}

func withUpdater(u Updater) func(*Options) { return func(o *Options) { o.Updater = u } }

func timing(first, interval, jitter time.Duration) func(*Daemon) {
	return func(d *Daemon) { d.updateFirstCheck, d.updateInterval, d.updateJitter = first, interval, jitter }
}

func (h *harness) waitDone() {
	h.t.Helper()
	select {
	case <-h.done:
	case <-time.After(3 * time.Second):
		h.t.Fatal("the daemon did not stop")
	}
}

func TestIdleServiceInstallsAndAsksForARestart(t *testing.T) {
	h := newHarness(t)
	fake := &fakeUpdater{commit: commitB}
	d := h.start(withUpdater(fake), timing(50*time.Millisecond, time.Hour, 0))
	h.waitDone()
	if _, applied, _ := fake.counts(); applied != 1 || !d.RestartRequested {
		t.Fatal(applied, d.RestartRequested)
	}
	logs := h.logs.String()
	if !strings.Contains(logs, "Update bbbbbbb ready, installing once the tunnel is idle") ||
		!strings.Contains(logs, "Updated to bbbbbbb, restarting") {
		t.Fatal(logs)
	}
}

func TestUpdateWaitsWhileTheTunnelIsWanted(t *testing.T) {
	h := newHarness(t)
	fake := &fakeUpdater{commit: commitB}
	d := h.start(withUpdater(fake), timing(50*time.Millisecond, time.Hour, 0), func(d *Daemon) { d.explicit = true })
	if !waitFor(3*time.Second, func() bool { c, _, _ := fake.counts(); return c == 1 }) {
		t.Fatal("no check")
	}
	time.Sleep(400 * time.Millisecond)
	if _, applied, _ := fake.counts(); applied != 0 {
		t.Fatal("installed while the tunnel was wanted")
	}
	s, _ := d.Status()
	if !strings.Contains(string(pyjson.Marshal(s)), `"update_pending": "`+commitB+`"`) {
		t.Fatal(string(pyjson.Marshal(s)))
	}
	d.mu.Lock()
	d.explicit = false
	d.mu.Unlock()
	h.waitDone()
	if _, applied, _ := fake.counts(); applied != 1 || !d.RestartRequested {
		t.Fatal("not installed once idle")
	}
}

func TestUpdateWaitsForTheConnectLoop(t *testing.T) {
	h := newHarness(t)
	d := h.start(withUpdater(&fakeUpdater{}))
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.idleForUpdateLocked() {
		t.Fatal("not idle at the start")
	}
	for _, state := range []State{Connecting, Connected, Disconnecting} {
		d.state = state
		if d.idleForUpdateLocked() {
			t.Fatal(state)
		}
	}
	d.state = Idle
	d.loopRunning = true
	if d.idleForUpdateLocked() {
		t.Fatal("idle while the connect loop runs")
	}
	d.loopRunning = false
	d.demandUntil = d.mono() + 10
	if d.idleForUpdateLocked() {
		t.Fatal("idle while there is demand")
	}
	d.demandUntil = 0
}

func TestSwitchedOffChecksNothing(t *testing.T) {
	h := newHarness(t)
	h.cfg.AutoUpdate = false
	fake := &fakeUpdater{commit: commitB}
	h.start(withUpdater(fake), timing(50*time.Millisecond, 50*time.Millisecond, 0))
	time.Sleep(300 * time.Millisecond)
	if checks, _, _ := fake.counts(); checks != 0 {
		t.Fatal(checks)
	}
}

func TestFailedCheckKeepsTheServiceRunning(t *testing.T) {
	h := newHarness(t)
	fake := &fakeUpdater{err: errors.New("checking for updates failed: offline")}
	d := h.start(withUpdater(fake), timing(50*time.Millisecond, 50*time.Millisecond, 0))
	if !waitFor(3*time.Second, func() bool { c, _, _ := fake.counts(); return c >= 2 }) {
		t.Fatal("no second check")
	}
	select {
	case <-h.done:
		t.Fatal("the daemon stopped")
	default:
	}
	if d.RestartRequested || !strings.Contains(h.logs.String(), "Update: checking for updates failed: offline") {
		t.Fatal(h.logs.String())
	}
	fake.mu.Lock()
	fake.err, fake.panics = nil, true
	fake.mu.Unlock()
	checks, _, _ := fake.counts()
	if !waitFor(3*time.Second, func() bool { c, _, _ := fake.counts(); return c >= checks+2 }) {
		t.Fatal("stopped checking after a crash")
	}
	if !strings.Contains(h.logs.String(), "Update check failed") {
		t.Fatal(h.logs.String())
	}
}

func TestSwitchingOffWhileWaitingDiscardsTheUpdate(t *testing.T) {
	h := newHarness(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := config.WriteInitial(path, "u", unis.DefaultID, nil); err != nil {
		t.Fatal(err)
	}
	fake := &fakeUpdater{commit: commitB}
	d := h.start(func(o *Options) { o.Updater, o.ConfigPath = fake, path }, timing(50*time.Millisecond, time.Hour, 0),
		func(d *Daemon) { d.explicit = true })
	if !waitFor(3*time.Second, func() bool { return fake.Pending() == commitB }) {
		t.Fatal("no check")
	}
	if err := d.SetAutoUpdate(false); err != nil {
		t.Fatal(err)
	}
	if !waitFor(3*time.Second, func() bool { _, _, discarded := fake.counts(); return discarded == 1 }) {
		t.Fatal("not discarded")
	}
	if _, applied, _ := fake.counts(); applied != 0 || d.RestartRequested {
		t.Fatal("installed after switching off")
	}
}

// The real schedule: 10 minutes after the start, then every 5 hours plus up to 30 minutes.
func TestUpdateSchedule(t *testing.T) {
	h := newHarness(t)
	fake := &fakeUpdater{}
	var mu sync.Mutex
	var delays []time.Duration
	fire := make(chan time.Time)
	h.start(withUpdater(fake), func(d *Daemon) {
		d.after = func(delay time.Duration) <-chan time.Time {
			mu.Lock()
			defer mu.Unlock()
			delays = append(delays, delay)
			return fire
		}
	})
	for i := 0; i < 3; i++ {
		fire <- time.Now()
	}
	if !waitFor(3*time.Second, func() bool { mu.Lock(); defer mu.Unlock(); return len(delays) == 4 }) {
		t.Fatal(delays)
	}
	mu.Lock()
	defer mu.Unlock()
	if delays[0] != 600*time.Second {
		t.Fatal(delays)
	}
	for _, delay := range delays[1:] {
		if delay < 5*time.Hour || delay > 5*time.Hour+30*time.Minute {
			t.Fatal(delays)
		}
	}
	if checks, _, _ := fake.counts(); checks != 3 {
		t.Fatal(checks)
	}
}

func TestSwitchInSettingsChecksRightAway(t *testing.T) {
	h := newHarness(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := config.WriteInitial(path, "u", unis.DefaultID, nil); err != nil {
		t.Fatal(err)
	}
	fake := &fakeUpdater{}
	d := h.start(func(o *Options) { o.Updater, o.ConfigPath = fake, path })
	r := h.post("/api/auto-update", `{"enabled": false}`)
	if r.status != 200 {
		t.Fatal(r.status, string(r.body))
	}
	if cfg, err := config.Load(path); err != nil || cfg.AutoUpdate {
		t.Fatal("not stored")
	}
	if r := h.post("/api/auto-update", `{"enabled": "no"}`); r.status != 400 {
		t.Fatal(r.status)
	}
	if checks, _, _ := fake.counts(); checks != 0 {
		t.Fatal("checked while off")
	}
	// Switching it on checks right away, like Tailscale.
	if r := h.post("/api/auto-update", `{"enabled": true}`); r.status != 200 {
		t.Fatal(r.status)
	}
	if cfg, err := config.Load(path); err != nil || !cfg.AutoUpdate {
		t.Fatal("not stored")
	}
	if !waitFor(3*time.Second, func() bool { c, _, _ := fake.counts(); return c == 1 }) {
		t.Fatal("no check")
	}
	if !strings.Contains(h.logs.String(), "Automatic updates on") {
		t.Fatal(h.logs.String())
	}
	s, _ := d.Status()
	if !strings.Contains(string(pyjson.Marshal(s)), `"commit": "`+commitA+`"`) {
		t.Fatal(string(pyjson.Marshal(s)))
	}
}
