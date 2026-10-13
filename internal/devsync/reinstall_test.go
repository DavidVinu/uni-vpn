package devsync

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

// The app removed with a language change not sent yet, then installed again and set up by hand:
// the new config.toml's default is not a change, the group's language stays.
func TestUnsentChangeThenInstalledAgainKeepsTheGroups(t *testing.T) {
	ctx := context.Background()
	_, devices := lossyDevices(t, "Desktop")
	laptop, desktop := devices[0], devices[1]
	laptop.local.set(KeyLanguage, "de")
	laptop.Notify()
	syncAll(t, laptop, desktop, laptop)
	desktop.local.set(KeyLanguage, "fr") // not sent yet (offline, no round before the removal)
	desktop.Notify()
	// Removed and installed again.
	desktop.local.mu.Lock()
	desktop.local.setup = false
	delete(desktop.local.values, KeyAccount)
	delete(desktop.local.values, KeyLanguage)
	delete(desktop.local.values, KeyAutoUpdate)
	desktop.local.mu.Unlock()
	again := device{New(desktop.opts), desktop.local, desktop.store}
	again.Load(ctx)
	// Set up by hand, as completeSetup does.
	again.Defaulted(KeyLanguage, KeyAutoUpdate)
	desktop.local.mu.Lock()
	desktop.local.setup = true
	desktop.local.mu.Unlock()
	desktop.local.set(KeyAccount, account)
	desktop.local.set(KeyPassword, "typed")
	desktop.local.set(KeyTOTP, "JBSWY3DP")
	desktop.local.set(KeyLanguage, "")
	desktop.local.set(KeyAutoUpdate, true)
	again.Notify(KeyAccount, KeyPassword, KeyTOTP)
	syncAll(t, again, laptop, again)
	for _, d := range []device{laptop, again} {
		t.Logf("%s: language %s", d.opts.Name, d.local.get(KeyLanguage))
		if lang := d.local.get(KeyLanguage); lang == `""` {
			t.Errorf("%s: language %s: the new config.toml's default replaced the group's", d.opts.Name, lang)
		}
	}
}

// As above, but the keyring did not answer at start (its unlock prompt dismissed) and the
// language is picked after the setup, while offline: the later pick holds.
func TestLanguagePickedAfterSetUpByHandWithTheKeysNotReadAtStart(t *testing.T) {
	ctx := context.Background()
	relay, devices := lossyDevices(t, "Desktop")
	laptop, desktop := devices[0], devices[1]
	laptop.local.set(KeyLanguage, "de")
	laptop.Notify()
	syncAll(t, laptop, desktop, laptop)
	// Removed and installed again.
	desktop.local.mu.Lock()
	desktop.local.setup = false
	delete(desktop.local.values, KeyAccount)
	delete(desktop.local.values, KeyLanguage)
	delete(desktop.local.values, KeyAutoUpdate)
	desktop.local.mu.Unlock()
	store := &keyringStore{misses: 1}
	store.value = desktop.store.value
	opts := desktop.opts
	opts.Store = store
	again := device{New(opts), desktop.local, desktop.store}
	again.Load(ctx)
	// Set up by hand, as completeSetup does: the keyring answers again for the password.
	desktop.local.mu.Lock()
	desktop.local.setup = true
	desktop.local.mu.Unlock()
	desktop.local.set(KeyAccount, account)
	desktop.local.set(KeyPassword, "typed")
	desktop.local.set(KeyTOTP, "JBSWY3DP")
	desktop.local.set(KeyLanguage, "")
	desktop.local.set(KeyAutoUpdate, true)
	again.Defaulted(KeyLanguage, KeyAutoUpdate)
	again.Notify(KeyAccount, KeyPassword, KeyTOTP)
	// Settings opens offline, then French is picked.
	relay.mu.Lock()
	relay.rng, relay.odds = rand.New(rand.NewPCG(1, 1)), 1
	relay.mu.Unlock()
	if err := again.Refresh(ctx); err == nil {
		t.Fatal("the round went through offline")
	}
	desktop.local.set(KeyLanguage, "fr")
	again.Notify()
	relay.mu.Lock()
	relay.odds = 0
	relay.mu.Unlock()
	syncAll(t, again, laptop)
	for _, d := range []device{laptop, again} {
		if lang := d.local.get(KeyLanguage); lang != `"fr"` {
			t.Errorf("%s: language %s, want the later pick fr", d.opts.Name, lang)
		}
	}
}

// A language taken over from the group with a website added while the desktop's round takes over de (a Notify between
// the round's start and its first save), the write and the next rounds failing (offline). The
// laptop changes the language again; the desktop changes something else, then a crash leaves
// its sync.json unreadable. The laptop's last language must stay.
func TestTakenOverValueAfterANotifyDuringTheRound(t *testing.T) {
	ctx := context.Background()
	relay, devices := lossyDevices(t, "Desktop")
	laptop, desktop := devices[0], devices[1]
	syncAll(t, desktop, laptop, desktop)
	desktop.local.set(KeyLanguage, "fr") // offline, not sent
	desktop.Notify()
	laptop.local.set(KeyLanguage, "de")
	laptop.Notify()
	syncAll(t, laptop)
	opts := desktop.opts
	hooked := &snapHookLocal{fakeLocal: desktop.local, after: func() {
		desktop.local.setDomains(append(desktop.local.getDomains(), "lsf.uni-heidelberg.de")...)
		e := desktopEngine.Load()
		e.Notify()
	}}
	opts.Local = hooked
	d := New(opts)
	desktopEngine.Store(d)
	d.Load(ctx)
	// The round takes de over; nothing of the desktop's goes out in it (fr is older), so it
	// writes nothing. The round the website's Notify asked for fails (offline).
	hooked.armed.Store(true)
	if err := d.Sync(ctx); err != nil || desktop.local.get(KeyLanguage) != `"de"` {
		t.Fatalf("want de taken over: %v %s", err, desktop.local.get(KeyLanguage))
	}
	_ = relay
	laptop.local.set(KeyLanguage, "es")
	laptop.Notify()
	syncAll(t, laptop)
	desktop.local.set(KeyAutoUpdate, false)
	d.Notify()
	pend, _ := os.ReadFile(desktop.opts.StatePath + ".pending")
	t.Logf("pending %s", pend)
	if err := os.WriteFile(desktop.opts.StatePath, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	again := device{New(desktop.opts), desktop.local, desktop.store}
	again.Load(ctx)
	syncAll(t, again, laptop, again)
	for _, dv := range []device{laptop, again} {
		if got := dv.local.get(KeyLanguage); got != `"es"` {
			t.Errorf("%s: language %s, want the laptop's later es", dv.opts.Name, got)
		}
	}
}

var desktopEngine atomic.Pointer[Engine]

// snapHookLocal runs after once, right after the next Snapshot with secrets: between a round's
// start and its first save.
type snapHookLocal struct {
	*fakeLocal
	armed atomic.Bool
	after func()
}

func (l *snapHookLocal) Snapshot(ctx context.Context, withSecrets bool) (map[string]json.RawMessage, error) {
	out, err := l.fakeLocal.Snapshot(ctx, withSecrets)
	if l.armed.CompareAndSwap(true, false) {
		l.after()
	}
	return out, err
}

// The same with the realistic Notify: a password typed in Settings while the round takes the
// laptop's de over (Notify during Apply counts only for a secret).
func TestTakenOverValueAfterAPasswordTypedDuringTheRound(t *testing.T) {
	ctx := context.Background()
	_, devices := lossyDevices(t, "Desktop")
	laptop, desktop := devices[0], devices[1]
	syncAll(t, desktop, laptop, desktop)
	desktop.local.set(KeyLanguage, "fr") // offline, not sent
	desktop.Notify()
	laptop.local.set(KeyLanguage, "de")
	laptop.Notify()
	syncAll(t, laptop)
	var d *Engine
	opts := desktop.opts
	opts.Local = &hookLocal{fakeLocal: desktop.local, onApply: func() {
		desktop.local.mu.Lock()
		desktop.local.values[KeyPassword] = mustJSON("typed")
		desktop.local.mu.Unlock()
		d.Notify(KeyPassword)
	}}
	d = New(opts)
	d.Load(ctx)
	if err := d.Sync(ctx); err != nil || desktop.local.get(KeyLanguage) != `"de"` {
		t.Fatalf("want de taken over: %v %s", err, desktop.local.get(KeyLanguage))
	}
	// The round the password's Notify asked for does not go through (offline).
	laptop.local.set(KeyLanguage, "es")
	laptop.Notify()
	syncAll(t, laptop)
	desktop.local.set(KeyAutoUpdate, false)
	d.Notify()
	if err := os.WriteFile(desktop.opts.StatePath, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	again := device{New(desktop.opts), desktop.local, desktop.store}
	again.Load(ctx)
	syncAll(t, again, laptop, again)
	for _, dv := range []device{laptop, again} {
		if got := dv.local.get(KeyLanguage); got != `"es"` {
			t.Errorf("%s: language %s, want the laptop's later es", dv.opts.Name, got)
		}
		if got := dv.local.get(KeyPassword); got != `"typed"` {
			t.Errorf("%s: password %s, want the typed one", dv.opts.Name, got)
		}
	}
}

// Notify writes its note outside the lock, and Defaulted saves the state next to a round: two
// writes at once must both go through and leave a whole file.
func TestWritesAtOnceLeaveAWholeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync.json")
	var failed atomic.Int32
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				if writeFile(path, mustJSON(map[string]int{"writer": i})) != nil {
					failed.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	var got map[string]int
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &got) != nil || failed.Load() != 0 {
		t.Fatalf("%d writes failed, file %q %v", failed.Load(), data, err)
	}
}
