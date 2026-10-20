package devsync

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"
)

// busyLocal is fakeLocal with the daemon's ErrBusy: an incoming account fails with ErrBusy (and
// password and second factor with it) when a change was noted since the round read. poke runs
// once inside the next Snapshot (the round's read), like a website saved at that moment.
type busyLocal struct {
	*fakeLocal
	e    *Engine
	pmu  sync.Mutex
	poke func()
}

func (l *busyLocal) Snapshot(ctx context.Context, withSecrets bool) (map[string]json.RawMessage, error) {
	out, err := l.fakeLocal.Snapshot(ctx, withSecrets)
	l.pmu.Lock()
	p := l.poke
	l.poke = nil
	l.pmu.Unlock()
	if p != nil {
		p()
	}
	return out, err
}

func (l *busyLocal) Apply(ctx context.Context, all map[string]Entry, keys []string) map[string]error {
	if !slices.Contains(keys, KeyAccount) || !l.e.ChangedSinceRead() {
		return l.fakeLocal.Apply(ctx, all, keys)
	}
	failed := map[string]error{KeyAccount: ErrBusy, KeyPassword: ErrBusy, KeyTOTP: ErrBusy}
	var rest []string
	for _, k := range keys {
		if _, f := failed[k]; !f {
			rest = append(rest, k)
		}
	}
	for k, v := range l.fakeLocal.Apply(ctx, all, rest) {
		failed[k] = v
	}
	return failed
}

// The desktop's sync.json is lost (its folder cleaned) after the laptop switched to another
// account, and its first round after that finds the account busy (a website saved while it
// read). The account it has counts as seen: a website saved before the next round went through
// does not send the old account to the group as a change made here.
func TestLostStateBusyRoundDoesNotBringBackTheOldAccount(t *testing.T) {
	ctx := context.Background()
	_, url := startRelay(t)
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	laptop := newDevice(t, url, "Laptop", setUpLaptop(), c)
	dl := &busyLocal{fakeLocal: setUpLaptop()}
	store := &memStore{}
	statePath := filepath.Join(t.TempDir(), "sync.json")
	de := New(Options{Relay: url, StatePath: statePath, Store: store, Local: dl,
		Name: "Desktop", Platform: "linux", Now: c.now, PollEvery: 20 * time.Millisecond, CodeLifetime: 5 * time.Second})
	t.Cleanup(de.bg.Wait)
	dl.e = de
	desktop := device{de, dl.fakeLocal, store}
	pair(t, ctx, laptop, desktop)
	for _, e := range []*Engine{desktop.Engine, laptop.Engine} {
		if err := e.Sync(ctx); err != nil {
			t.Fatal(err)
		}
	}
	other := map[string]any{"university": "other", "user": "zz999", "overrides": map[string]any{"host": "vpn.z.example"}}
	laptop.local.set(KeyAccount, other)
	laptop.local.set(KeyPassword, "secret-z")
	laptop.Notify(KeyAccount, KeyPassword, KeyTOTP)
	if err := laptop.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	want := laptop.local.get(KeyAccount)
	if err := os.Remove(statePath); err != nil {
		t.Fatal(err)
	}
	dl.pmu.Lock()
	dl.poke = func() {
		dl.setDomains(append(dl.getDomains(), "x.example")...)
		de.Notify()
	}
	dl.pmu.Unlock()
	de.Sync(ctx) // the busy round
	// Another website saved before the next round went through (or the network was gone).
	dl.setDomains(append(dl.getDomains(), "y.example")...)
	de.Notify()
	for _, e := range []*Engine{desktop.Engine, laptop.Engine, desktop.Engine, laptop.Engine} {
		e.Sync(ctx)
	}
	if l, d := laptop.local.get(KeyAccount), desktop.local.get(KeyAccount); l != want || d != want {
		t.Errorf("laptop %s, desktop %s; want the account the laptop switched to, %s", l, d, want)
	}
}
