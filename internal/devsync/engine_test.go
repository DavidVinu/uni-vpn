package devsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DavidVinu/uni-vpn/internal/devsync/relaytest"
)

func init() { scryptN = 1 << 10 } // fast pairing in tests

// startRelay is the in-memory relay, or relay/worker.js when UNI_VPN_TEST_RELAY names a running
// one (node relay/serve.mjs); groups is nil then.
func startRelay(t *testing.T) (groups func() int, url string) {
	if url := os.Getenv("UNI_VPN_TEST_RELAY"); url != "" {
		return nil, url
	}
	r, url := relaytest.Start(t)
	return r.Groups, url
}

type memStore struct {
	mu    sync.Mutex
	value string
}

func (s *memStore) Load(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.value, nil
}

func (s *memStore) Save(v string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.value = v
	return nil
}

func (s *memStore) Delete() error { return s.Save("") }

// fakeLocal is a device's settings as plain values.
type fakeLocal struct {
	mu      sync.Mutex
	values  map[string]json.RawMessage
	domains []string
	setup   bool
	reads   int // snapshots with secrets
	applied [][]string
	reject  map[string]bool // keys Apply refuses
}

func newLocal(setup bool, values map[string]any, domains ...string) *fakeLocal {
	l := &fakeLocal{values: map[string]json.RawMessage{}, setup: setup, domains: domains}
	for k, v := range values {
		l.values[k] = mustJSON(v)
	}
	return l
}

func (l *fakeLocal) Snapshot(_ context.Context, withSecrets bool) (map[string]json.RawMessage, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := map[string]json.RawMessage{}
	if !l.setup {
		return out, nil
	}
	if withSecrets {
		l.reads++
	}
	for k, v := range l.values {
		if isSecret(k) && !withSecrets {
			continue
		}
		out[k] = v
	}
	for _, d := range l.domains {
		out[DomainPrefix+d] = json.RawMessage("true")
	}
	return out, nil
}

func (l *fakeLocal) Apply(_ context.Context, all map[string]Entry, keys []string) map[string]error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.applied = append(l.applied, sorted(keys))
	failed := map[string]error{}
	for _, k := range keys {
		if l.reject[k] {
			failed[k] = errors.New("refused")
			continue
		}
		if strings.HasPrefix(k, DomainPrefix) {
			continue
		}
		l.values[k] = all[k].V
		if k == KeyAccount {
			l.setup = true
		}
	}
	l.domains = Domains(all)
	return failed
}

func (l *fakeLocal) SetupDone() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.setup
}

func (l *fakeLocal) get(k string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return string(l.values[k])
}

func (l *fakeLocal) set(k string, v any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.values[k] = mustJSON(v)
}

func (l *fakeLocal) setDomains(d ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.domains = d
}

func (l *fakeLocal) getDomains() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.domains)
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(time.Second)
	return c.t
}

type device struct {
	*Engine
	local *fakeLocal
	store *memStore
}

func newDevice(t *testing.T, url, name string, local *fakeLocal, c *clock) device {
	store := &memStore{}
	e := New(Options{Relay: url, StatePath: filepath.Join(t.TempDir(), "sync.json"), Store: store, Local: local,
		Name: name, Platform: "linux", Now: c.now, PollEvery: 20 * time.Millisecond, CodeLifetime: 5 * time.Second})
	return device{e, local, store}
}

var account = map[string]any{"university": "heidelberg", "user": "ab123", "overrides": map[string]any{}}

func setUpLaptop() *fakeLocal {
	return newLocal(true, map[string]any{KeyAccount: account, KeyPassword: "secret", KeyTOTP: "JBSWY3DP",
		KeyLanguage: "", KeyAutoUpdate: true}, "sogo.uni-heidelberg.de", "heico.uni-heidelberg.de")
}

func waitState(t *testing.T, e *Engine, want string) Pairing {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if p := e.Status().Pairing; p != nil && p.State != "waiting" {
			if p.State != want {
				t.Fatalf("pairing ended %s (%v), want %s", p.State, p.Error, want)
			}
			return *p
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pairing did not end")
	return Pairing{}
}

func names(e *Engine) []string {
	var out []string
	for _, d := range e.Status().Devices {
		out = append(out, d.Name)
	}
	return out
}

// Two set-up devices: laptop shows the code, the new desktop types it.
func pair(t *testing.T, ctx context.Context, from, to device) {
	t.Helper()
	p, err := from.Offer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := to.Enter(ctx, strings.ToLower(p.Code)); err != nil {
		t.Fatal(err)
	}
	waitState(t, from.Engine, "done")
	if err := from.Sync(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestOfferSetsUpNewDevice(t *testing.T) {
	ctx := context.Background()
	_, url := startRelay(t)
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	laptop := newDevice(t, url, "Laptop", setUpLaptop(), c)
	desktop := newDevice(t, url, "Desktop", newLocal(false, nil), c)

	p, err := laptop.Offer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.QR, "<svg") || len(p.Code) != 14 || p.State != "waiting" {
		t.Fatalf("pairing %+v", p)
	}
	if err := desktop.Enter(ctx, p.Code); err != nil {
		t.Fatal(err)
	}
	if !desktop.local.SetupDone() || desktop.local.get(KeyPassword) != `"secret"` || desktop.local.get(KeyTOTP) != `"JBSWY3DP"` {
		t.Fatalf("desktop not set up: %v", desktop.local.values)
	}
	if got := desktop.local.getDomains(); !slices.Equal(got, []string{"heico.uni-heidelberg.de", "sogo.uni-heidelberg.de"}) {
		t.Fatalf("domains %v", got)
	}
	waitState(t, laptop.Engine, "done")
	if err := laptop.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if got := names(laptop.Engine); !slices.Equal(got, []string{"Laptop", "Desktop"}) {
		t.Fatalf("laptop sees %v", got)
	}
	// A code works once.
	if err := desktop.Enter(ctx, p.Code); !errors.Is(err, ErrCodeUnknown) {
		t.Fatalf("second use: %v", err)
	}
}

func TestRequestFromNewDevice(t *testing.T) {
	ctx := context.Background()
	_, url := startRelay(t)
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	laptop := newDevice(t, url, "Laptop", setUpLaptop(), c)
	phone := newDevice(t, url, "Phone", newLocal(false, nil), c)

	p, err := phone.Request(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The laptop scans the phone's QR code.
	if err := laptop.Enter(ctx, QRPrefix+strings.ReplaceAll(p.Code, "-", "")); err != nil {
		t.Fatal(err)
	}
	waitState(t, phone.Engine, "done")
	if phone.local.get(KeyPassword) != `"secret"` {
		t.Fatal("phone did not get the password")
	}
	if err := laptop.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if got := names(laptop.Engine); !slices.Equal(got, []string{"Laptop", "Phone"}) {
		t.Fatalf("laptop sees %v", got)
	}
}

func TestRequestNeedsOneSetUpDevice(t *testing.T) {
	ctx := context.Background()
	_, url := startRelay(t)
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	a := newDevice(t, url, "A", newLocal(false, nil), c)
	b := newDevice(t, url, "B", newLocal(false, nil), c)
	p, err := a.Request(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Enter(ctx, p.Code); !errors.Is(err, ErrSetupFirst) {
		t.Fatalf("got %v", err)
	}
	if _, err := b.Offer(ctx); !errors.Is(err, ErrSetupFirst) {
		t.Fatalf("offer: %v", err)
	}
	if err := b.Enter(ctx, "AAAA-AAAA-AAAA"); !errors.Is(err, ErrCodeUnknown) {
		t.Fatalf("unknown code: %v", err)
	}
	if err := b.Enter(ctx, "hello"); !errors.Is(err, ErrCodeUnknown) {
		t.Fatalf("not a code: %v", err)
	}
}

func TestChangesTravelBothWays(t *testing.T) {
	ctx := context.Background()
	_, url := startRelay(t)
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	laptop := newDevice(t, url, "Laptop", setUpLaptop(), c)
	desktop := newDevice(t, url, "Desktop", newLocal(false, nil), c)
	pair(t, ctx, laptop, desktop)

	// Different websites changed on each side both survive.
	laptop.local.setDomains("sogo.uni-heidelberg.de", "heico.uni-heidelberg.de", "lsf.uni-heidelberg.de")
	desktop.local.setDomains("heico.uni-heidelberg.de")
	desktop.local.set(KeyLanguage, "de")
	for _, d := range []device{laptop, desktop, laptop} {
		if err := d.Sync(ctx); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"heico.uni-heidelberg.de", "lsf.uni-heidelberg.de"}
	if got := laptop.local.getDomains(); !slices.Equal(got, want) {
		t.Fatalf("laptop domains %v", got)
	}
	if got := desktop.local.getDomains(); !slices.Equal(got, want) {
		t.Fatalf("desktop domains %v", got)
	}
	if laptop.local.get(KeyLanguage) != `"de"` {
		t.Fatal("language did not reach the laptop")
	}

	// A new password goes out only when the daemon says secrets changed.
	desktop.local.set(KeyPassword, "new")
	desktop.Notify(true)
	if err := desktop.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if err := laptop.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if laptop.local.get(KeyPassword) != `"new"` {
		t.Fatal("password did not reach the laptop")
	}
	// Nothing new: no write, and the keyring is not read.
	reads := laptop.local.reads
	if err := laptop.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if laptop.local.reads != reads {
		t.Fatal("secrets read without a change")
	}
}

func TestRemoveMovesOthersToNewKey(t *testing.T) {
	ctx := context.Background()
	groups, url := startRelay(t)
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	laptop := newDevice(t, url, "Laptop", setUpLaptop(), c)
	desktop := newDevice(t, url, "Desktop", newLocal(false, nil), c)
	old := newDevice(t, url, "Old", newLocal(false, nil), c)
	pair(t, ctx, laptop, desktop)
	pair(t, ctx, laptop, old)
	if err := desktop.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	var oldID string
	for _, d := range laptop.Status().Devices {
		if d.Name == "Old" {
			oldID = d.ID
		}
	}
	if err := laptop.Remove(ctx, oldID); err != nil {
		t.Fatal(err)
	}
	if got := names(laptop.Engine); !slices.Equal(got, []string{"Laptop", "Desktop"}) {
		t.Fatalf("after removal %v", got)
	}
	// The desktop follows to the new key, the removed device stops.
	laptop.local.set(KeyLanguage, "fr")
	for _, d := range []device{laptop, desktop, old} {
		if err := d.Sync(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if desktop.local.get(KeyLanguage) != `"fr"` {
		t.Fatal("desktop lost the group")
	}
	if old.Status().On || old.store.value != "" {
		t.Fatal("removed device still syncs")
	}
	if old.local.get(KeyLanguage) == `"fr"` {
		t.Fatal("removed device got a change made after its removal")
	}
	if groups != nil && groups() != 2 {
		t.Fatalf("%d groups", groups())
	}
	// Turning syncing off on this device.
	if err := desktop.Remove(ctx, desktop.Status().Devices[0].ID); err != nil {
		t.Fatal(err)
	}
	if desktop.Status().On {
		t.Fatal("still on")
	}
	if err := laptop.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if got := names(laptop.Engine); !slices.Equal(got, []string{"Laptop"}) {
		t.Fatalf("after leaving %v", got)
	}
}

func TestConcurrentWritersRetry(t *testing.T) {
	ctx := context.Background()
	_, url := startRelay(t)
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	laptop := newDevice(t, url, "Laptop", setUpLaptop(), c)
	desktop := newDevice(t, url, "Desktop", newLocal(false, nil), c)
	pair(t, ctx, laptop, desktop)
	laptop.local.set(KeyAutoUpdate, false)
	desktop.local.set(KeyLanguage, "es")
	var wg sync.WaitGroup
	for _, d := range []device{laptop, desktop} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := d.Sync(ctx); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	for _, d := range []device{laptop, desktop} {
		if err := d.Sync(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []device{laptop, desktop} {
		if d.local.get(KeyAutoUpdate) != "false" || d.local.get(KeyLanguage) != `"es"` {
			t.Fatalf("%s: %v", d.opts.Name, d.local.values)
		}
	}
}

func TestUnavailableWithoutRelay(t *testing.T) {
	e := New(Options{Store: &memStore{}, Local: newLocal(true, nil)})
	if e.Available() || e.Status().Available {
		t.Fatal("available without a relay")
	}
	if _, err := e.Offer(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if err := e.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCodes(t *testing.T) {
	for range 100 {
		code := newCode()
		got, ok := NormalizeCode(" " + strings.ToLower(FormatCode(code)) + " ")
		if !ok || got != code {
			t.Fatalf("%s -> %s %v", code, got, ok)
		}
	}
	if got, ok := NormalizeCode("UNIVPN:olio-0000-1111"); !ok || got != "011000001111" {
		t.Fatalf("got %s %v", got, ok)
	}
	for _, bad := range []string{"", "ABCD-EFGH-JKL", "ABCD-EFGH-JKLMN", "ABCD-EFGH-JKLU", "ÄBCD-EFGH-JKLM"} {
		if _, ok := NormalizeCode(bad); ok {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func TestCrypto(t *testing.T) {
	key := random(32)
	blob := seal(key, "group x", []byte("hello"))
	if blob[0] != Format || len(blob) != 1+24+5+16 {
		t.Fatalf("blob %x", blob)
	}
	if got, err := open(key, "group x", blob); err != nil || string(got) != "hello" {
		t.Fatal(got, err)
	}
	if _, err := open(key, "group y", blob); err == nil {
		t.Fatal("opened with another context")
	}
	k := newKeys(random(32), "", nil)
	sealed, err := box(k.private().PublicKey().Bytes(), []byte("root"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := unbox(k.private(), sealed); err != nil || string(got) != "root" {
		t.Fatal(got, err)
	}
	other := newKeys(random(32), "", nil)
	if _, err := unbox(other.private(), sealed); err == nil {
		t.Fatal("another device opened the box")
	}
}

func TestLaterChangeWinsOverOfflineEdit(t *testing.T) {
	ctx := context.Background()
	_, url := startRelay(t)
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	laptop := newDevice(t, url, "Laptop", setUpLaptop(), c)
	desktop := newDevice(t, url, "Desktop", newLocal(false, nil), c)
	pair(t, ctx, laptop, desktop)

	// The desktop changes the language while offline, the laptop later, and syncs first.
	desktop.local.set(KeyLanguage, "de")
	desktop.Notify(false)
	laptop.local.set(KeyLanguage, "fr")
	laptop.Notify(false)
	for _, d := range []device{laptop, desktop, laptop} {
		if err := d.Sync(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []device{laptop, desktop} {
		if got := d.local.get(KeyLanguage); got != `"fr"` {
			t.Fatalf("%s has %s, want the later fr", d.opts.Name, got)
		}
	}
}

func TestPendingSecretSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	_, url := startRelay(t)
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	laptop := newDevice(t, url, "Laptop", setUpLaptop(), c)
	desktop := newDevice(t, url, "Desktop", newLocal(false, nil), c)
	pair(t, ctx, laptop, desktop)

	laptop.local.set(KeyPassword, "changed")
	laptop.Notify(true)
	// The daemon restarts before the change went out.
	restarted := New(laptop.opts)
	if err := restarted.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if err := desktop.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if got := desktop.local.get(KeyPassword); got != `"changed"` {
		t.Fatalf("desktop has %s", got)
	}
}

func TestRefusedSettingDoesNotStopSyncing(t *testing.T) {
	ctx := context.Background()
	_, url := startRelay(t)
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	laptop := newDevice(t, url, "Laptop", setUpLaptop(), c)
	desktop := newDevice(t, url, "Desktop", newLocal(false, nil), c)
	pair(t, ctx, laptop, desktop)

	desktop.local.mu.Lock()
	desktop.local.reject = map[string]bool{KeyLanguage: true}
	desktop.local.mu.Unlock()
	laptop.local.set(KeyLanguage, "xx")
	laptop.local.set(KeyAutoUpdate, false)
	if err := laptop.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	var ae *ApplyError
	if err := desktop.Sync(ctx); !errors.As(err, &ae) || !slices.Equal(ae.Keys, []string{KeyLanguage}) {
		t.Fatalf("got %v", err)
	}
	if desktop.local.get(KeyAutoUpdate) != "false" {
		t.Fatal("the other setting did not arrive")
	}
	// The desktop does not send its own language back as a change.
	if err := desktop.Sync(ctx); !errors.As(err, &ae) {
		t.Fatalf("got %v", err)
	}
	if err := laptop.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if laptop.local.get(KeyLanguage) != `"xx"` {
		t.Fatal("refused value was overwritten on the laptop")
	}
}

func TestCancelWithdrawsTheCode(t *testing.T) {
	ctx := context.Background()
	_, url := startRelay(t)
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	laptop := newDevice(t, url, "Laptop", setUpLaptop(), c)
	desktop := newDevice(t, url, "Desktop", newLocal(false, nil), c)
	p, err := laptop.Offer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	laptop.CancelPairing()
	if err := desktop.Enter(ctx, p.Code); !errors.Is(err, ErrCodeUnknown) {
		t.Fatalf("cancelled code: %v", err)
	}
	// A new code replaces the old one.
	first, _ := laptop.Offer(ctx)
	second, _ := laptop.Offer(ctx)
	if err := desktop.Enter(ctx, first.Code); !errors.Is(err, ErrCodeUnknown) {
		t.Fatalf("replaced code: %v", err)
	}
	if err := desktop.Enter(ctx, second.Code); err != nil {
		t.Fatal(err)
	}
	// The laptop finishes the pairing and its sync (both write sync.json) before the test's
	// folder goes, as in pair.
	waitState(t, laptop.Engine, "done")
	if err := laptop.Sync(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestExpiredCode(t *testing.T) {
	ctx := context.Background()
	_, url := startRelay(t)
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	laptop := newDevice(t, url, "Laptop", setUpLaptop(), c)
	laptop.opts.CodeLifetime = 100 * time.Millisecond
	if _, err := laptop.Offer(ctx); err != nil {
		t.Fatal(err)
	}
	waitState(t, laptop.Engine, "expired")
}

func TestRemovedDeviceCannotRewriteTheOldGroup(t *testing.T) {
	ctx := context.Background()
	_, url := startRelay(t)
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	laptop := newDevice(t, url, "Laptop", setUpLaptop(), c)
	stolen := newDevice(t, url, "Stolen", newLocal(false, nil), c)
	pair(t, ctx, laptop, stolen)
	k, _ := stolen.loadKeys(ctx)
	old := k.group()
	_, version, err := stolen.fetch(ctx, old)
	if err != nil {
		t.Fatal(err)
	}
	var id string
	for _, d := range laptop.Status().Devices {
		if !d.This {
			id = d.ID
		}
	}
	if err := laptop.Remove(ctx, id); err != nil {
		t.Fatal(err)
	}
	_, now, _ := stolen.fetch(ctx, old)
	if _, err := stolen.Engine.store(ctx, old, now, Doc{Format: Format, Entries: map[string]Entry{}}, false); !errors.Is(err, errConflict) {
		t.Fatalf("write after removal: %v (version %d -> %d)", err, version, now)
	}
}

// otherID is the id of the first device in e's list that is not e itself, or of the one named.
func otherID(e *Engine, name string) string {
	for _, d := range e.Status().Devices {
		if !d.This && (name == "" || d.Name == name) {
			return d.ID
		}
	}
	return ""
}

func TestOfferAfterRemovalElsewhere(t *testing.T) {
	ctx := context.Background()
	_, url := startRelay(t)
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	laptop := newDevice(t, url, "Laptop", setUpLaptop(), c)
	desktop := newDevice(t, url, "Desktop", newLocal(false, nil), c)
	old := newDevice(t, url, "Old", newLocal(false, nil), c)
	pair(t, ctx, laptop, desktop)
	pair(t, ctx, laptop, old)
	if err := desktop.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if err := desktop.Remove(ctx, otherID(desktop.Engine, "Old")); err != nil {
		t.Fatal(err)
	}
	// The laptop has not synced since: its code must carry the new key, not the old one.
	phone := newDevice(t, url, "Phone", newLocal(false, nil), c)
	pair(t, ctx, laptop, phone)
	if !phone.local.SetupDone() || !phone.Status().On {
		t.Fatal("the phone joined a group that had moved on")
	}
}

func TestCodeFromBeforeARemovalIsRefused(t *testing.T) {
	ctx := context.Background()
	_, url := startRelay(t)
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	laptop := newDevice(t, url, "Laptop", setUpLaptop(), c)
	desktop := newDevice(t, url, "Desktop", newLocal(false, nil), c)
	pair(t, ctx, laptop, desktop)
	p, err := laptop.Offer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer laptop.CancelPairing()
	// While the code is on the laptop's screen, the desktop removes the laptop.
	if err := desktop.Remove(ctx, otherID(desktop.Engine, "Laptop")); err != nil {
		t.Fatal(err)
	}
	phone := newDevice(t, url, "Phone", newLocal(false, nil), c)
	if err := phone.Enter(ctx, p.Code); !errors.Is(err, ErrCodeUnknown) {
		t.Fatalf("got %v, the phone is set up: %v", err, phone.local.SetupDone())
	}
	if phone.Status().On {
		t.Fatal("still syncing")
	}
}

func TestRefusedSettingDoesNotBlockAddingOrRemoving(t *testing.T) {
	ctx := context.Background()
	_, url := startRelay(t)
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	laptop := newDevice(t, url, "Laptop", setUpLaptop(), c)
	desktop := newDevice(t, url, "Desktop", newLocal(false, nil), c)
	pair(t, ctx, laptop, desktop)
	// A newer version's language this one cannot take over, for good.
	desktop.local.mu.Lock()
	desktop.local.reject = map[string]bool{KeyLanguage: true}
	desktop.local.mu.Unlock()
	laptop.local.set(KeyLanguage, "xx")
	if err := laptop.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	phone := newDevice(t, url, "Phone", newLocal(false, nil), c)
	p, err := desktop.Offer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := phone.Enter(ctx, p.Code); err != nil {
		t.Fatal(err)
	}
	waitState(t, desktop.Engine, "done")
	var ae *ApplyError
	if err := desktop.Sync(ctx); !errors.As(err, &ae) {
		t.Fatalf("got %v", err)
	}
	if err := desktop.Remove(ctx, otherID(desktop.Engine, "Phone")); err != nil {
		t.Fatal(err)
	}
	if got := names(desktop.Engine); !slices.Equal(got, []string{"Desktop", "Laptop"}) {
		t.Fatalf("after removal %v", got)
	}
}

func TestPairingFitsInTheTunnelExtension(t *testing.T) {
	defer func(n int) { scryptN = n }(scryptN)
	scryptN = 1 << 15
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	pairingFor("0123456789AB")
	runtime.ReadMemStats(&after)
	if used := after.TotalAlloc - before.TotalAlloc; used > 8<<20 {
		t.Fatalf("a code takes %d MiB, the iPhone's tunnel extension has about 50 MB in all", used>>20)
	}
}

func TestStateFileHasNoPasswordHash(t *testing.T) {
	ctx := context.Background()
	_, url := startRelay(t)
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	laptop := newDevice(t, url, "Laptop", setUpLaptop(), c)
	desktop := newDevice(t, url, "Desktop", newLocal(false, nil), c)
	pair(t, ctx, laptop, desktop)
	for _, d := range []device{laptop, desktop} {
		data, err := os.ReadFile(d.opts.StatePath)
		if err != nil {
			t.Fatal(err)
		}
		// SHA-256 of a password can be guessed back; sync.json must not let anyone try.
		if strings.Contains(string(data), hash(mustJSON("secret"))) || !strings.Contains(string(data), `"password"`) {
			t.Fatalf("%s: %s", d.opts.Name, data)
		}
	}
}

// An older version wrote plain hashes into sync.json: after the update, what did not change
// here stays the group's, and a change made here before the update still goes out.
func TestUpdateFromPlainStateHashes(t *testing.T) {
	ctx := context.Background()
	_, url := startRelay(t)
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	laptop := newDevice(t, url, "Laptop", setUpLaptop(), c)
	desktop := newDevice(t, url, "Desktop", newLocal(false, nil), c)
	pair(t, ctx, laptop, desktop)
	k, _ := desktop.loadKeys(ctx)
	remote, version, err := desktop.fetch(ctx, k.group())
	if err != nil {
		t.Fatal(err)
	}
	old := state{Version: version, Hashes: map[string]savedHash{}}
	for key, ent := range remote.Entries {
		old.Hashes[key] = savedHash{H: hash(ent.V), T: ent.T, D: ent.D}
	}
	if err := writeFile(desktop.opts.StatePath, mustJSON(old)); err != nil {
		t.Fatal(err)
	}
	laptop.local.set(KeyLanguage, "fr")
	laptop.Notify(false)
	if err := laptop.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	// Then, before the update and offline, the desktop turned automatic updates off.
	desktop.local.set(KeyAutoUpdate, false)
	if err := writeFile(desktop.opts.StatePath+".pending", mustJSON(map[string]any{"at": c.now().UnixMilli()})); err != nil {
		t.Fatal(err)
	}
	updated := New(desktop.opts)
	if err := updated.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if err := laptop.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	for _, d := range []device{laptop, desktop} {
		if d.local.get(KeyLanguage) != `"fr"` || d.local.get(KeyAutoUpdate) != "false" || d.local.get(KeyPassword) != `"secret"` {
			t.Fatalf("%s: %v", d.opts.Name, d.local.values)
		}
	}
	if st := updated.loadState(); !st.Keyed {
		t.Fatal("sync.json not carried over")
	}
}

// keyringStore misses the first reads the way the keyring does when it does not answer:
// keyringSync.Load reads any failed lookup as no entry.
type keyringStore struct {
	memStore
	misses int
}

func (s *keyringStore) Load(ctx context.Context) (string, error) {
	s.mu.Lock()
	if s.misses > 0 {
		s.misses--
		s.mu.Unlock()
		return "", nil
	}
	s.mu.Unlock()
	return s.memStore.Load(ctx)
}

func TestKeyringThatAnswersLate(t *testing.T) {
	ctx := context.Background()
	_, url := startRelay(t)
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	laptop := newDevice(t, url, "Laptop", setUpLaptop(), c)
	desktop := newDevice(t, url, "Desktop", newLocal(false, nil), c)
	pair(t, ctx, laptop, desktop)
	laptop.local.set(KeyLanguage, "fr")
	if err := laptop.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	restart := func() (*Engine, *keyringStore) {
		store := &keyringStore{misses: 1}
		store.value = desktop.store.value
		opts := desktop.opts
		opts.Store = store
		e := New(opts)
		e.Load(ctx) // the keyring does not answer at start
		return e, store
	}
	// Opening Settings finds the keys.
	e, _ := restart()
	if err := e.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if !e.Status().On || desktop.local.get(KeyLanguage) != `"fr"` {
		t.Fatalf("on %v, language %s", e.Status().On, desktop.local.get(KeyLanguage))
	}
	// "Add a device" keeps the group.
	e, store := restart()
	if _, err := e.Offer(ctx); err != nil {
		t.Fatal(err)
	}
	e.CancelPairing()
	if store.value != desktop.store.value {
		t.Fatal("the keys were replaced by a new group")
	}
}

func TestDeviceWithoutSetupRemovesNoWebsites(t *testing.T) {
	ctx := context.Background()
	_, url := startRelay(t)
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	laptop := newDevice(t, url, "Laptop", setUpLaptop(), c)
	desktop := newDevice(t, url, "Desktop", newLocal(false, nil), c)
	pair(t, ctx, laptop, desktop)
	// The desktop's config.toml is gone (uninstalled and installed again) and the group's
	// account cannot be set up there yet.
	desktop.local.mu.Lock()
	desktop.local.setup = false
	desktop.local.reject = map[string]bool{KeyAccount: true, KeyLanguage: true, KeyAutoUpdate: true}
	desktop.local.mu.Unlock()
	var ae *ApplyError
	if err := desktop.Sync(ctx); err != nil && !errors.As(err, &ae) {
		t.Fatal(err)
	}
	if err := laptop.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if got := laptop.local.getDomains(); len(got) != 2 {
		t.Fatalf("laptop websites %v", got)
	}
}

func TestOfflineEditKeepsItsTime(t *testing.T) {
	ctx := context.Background()
	_, url := startRelay(t)
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	laptop := newDevice(t, url, "Laptop", setUpLaptop(), c)
	desktop := newDevice(t, url, "Desktop", newLocal(false, nil), c)
	pair(t, ctx, laptop, desktop)
	// The desktop, offline, changes the language; the laptop changes it later and syncs; the
	// desktop, still offline, then adds a website.
	desktop.local.set(KeyLanguage, "de")
	desktop.Notify(false)
	laptop.local.set(KeyLanguage, "fr")
	laptop.Notify(false)
	if err := laptop.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	desktop.local.setDomains("sogo.uni-heidelberg.de", "heico.uni-heidelberg.de", "lsf.uni-heidelberg.de")
	desktop.Notify(false)
	for _, d := range []device{desktop, laptop} {
		if err := d.Sync(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []device{laptop, desktop} {
		if d.local.get(KeyLanguage) != `"fr"` || len(d.local.getDomains()) != 3 {
			t.Fatalf("%s: language %s, websites %v", d.opts.Name, d.local.get(KeyLanguage), d.local.getDomains())
		}
	}
}

func TestRemoveOffline(t *testing.T) {
	ctx := context.Background()
	_, url := startRelay(t)
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	laptop := newDevice(t, url, "Laptop", setUpLaptop(), c)
	desktop := newDevice(t, url, "Desktop", newLocal(false, nil), c)
	pair(t, ctx, laptop, desktop)
	opts := laptop.opts
	opts.Relay = "http://127.0.0.1:1" // nothing listens there
	if err := New(opts).Remove(ctx, otherID(laptop.Engine, "")); !errors.Is(err, ErrOffline) {
		t.Fatalf("got %v", err)
	}
}

// hookLocal runs onApply inside the first Apply, before anything is taken over.
type hookLocal struct {
	*fakeLocal
	once    sync.Once
	onApply func()
}

func (l *hookLocal) Apply(ctx context.Context, all map[string]Entry, keys []string) map[string]error {
	l.once.Do(l.onApply)
	return l.fakeLocal.Apply(ctx, all, keys)
}

// TestTakenOverValueIsNotSentBackAfterAConflict: another device writes between this one's read
// and its write; the second attempt must not send back what the first took over as a change
// of its own, over the newer one.
func TestTakenOverValueIsNotSentBackAfterAConflict(t *testing.T) {
	ctx := context.Background()
	_, url := startRelay(t)
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	laptop := newDevice(t, url, "Laptop", setUpLaptop(), c)
	desktop := newDevice(t, url, "Desktop", newLocal(false, nil), c)
	pair(t, ctx, laptop, desktop)
	// The desktop sets the language to de and syncs, then to fr, not sent yet.
	desktop.local.set(KeyLanguage, "de")
	desktop.Notify(false)
	if err := desktop.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	desktop.local.set(KeyLanguage, "fr")
	desktop.Notify(false)
	// Then automatic updates go off on the laptop; while it takes over de, fr arrives.
	laptop.local.set(KeyAutoUpdate, false)
	laptop.Notify(false)
	opts := laptop.opts
	opts.Local = &hookLocal{fakeLocal: laptop.local, onApply: func() {
		if err := desktop.Sync(ctx); err != nil {
			t.Error(err)
		}
	}}
	e := New(opts)
	for _, run := range []func(context.Context) error{e.Sync, desktop.Sync, e.Sync} {
		if err := run(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for _, l := range []*fakeLocal{laptop.local, desktop.local} {
		if l.get(KeyLanguage) != `"fr"` || l.get(KeyAutoUpdate) != "false" {
			t.Fatalf("language %s, automatic updates %s; want the later fr and off", l.get(KeyLanguage), l.get(KeyAutoUpdate))
		}
	}
}

// A password typed on this device while it takes over a setting from another one goes out.
func TestPasswordTypedWhileTakingOverGoesOut(t *testing.T) {
	ctx := context.Background()
	_, url := startRelay(t)
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	laptop := newDevice(t, url, "Laptop", setUpLaptop(), c)
	desktop := newDevice(t, url, "Desktop", newLocal(false, nil), c)
	pair(t, ctx, laptop, desktop)
	desktop.local.set(KeyLanguage, "de")
	desktop.Notify(false)
	if err := desktop.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	var e *Engine
	opts := laptop.opts
	opts.Local = &hookLocal{fakeLocal: laptop.local, onApply: func() {
		laptop.local.set(KeyPassword, "typed")
		e.Notify(true)
	}}
	e = New(opts)
	for _, run := range []func(context.Context) error{e.Sync, e.Sync, desktop.Sync} {
		if err := run(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got := desktop.local.get(KeyPassword); got != `"typed"` {
		t.Fatalf("desktop has %s", got)
	}
}

// A website removed on the laptop and later added on the desktop, which had not synced since:
// the later addition stays.
func TestLaterAddOfAWebsiteRemovedElsewhere(t *testing.T) {
	ctx := context.Background()
	_, url := startRelay(t)
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	laptop := newDevice(t, url, "Laptop", setUpLaptop(), c)
	desktop := newDevice(t, url, "Desktop", newLocal(false, nil), c)
	pair(t, ctx, laptop, desktop)
	for _, list := range [][]string{
		{"sogo.uni-heidelberg.de", "heico.uni-heidelberg.de", "lsf.uni-heidelberg.de"},
		{"sogo.uni-heidelberg.de", "heico.uni-heidelberg.de"},
	} {
		laptop.local.setDomains(list...)
		laptop.Notify(false)
		if err := laptop.Sync(ctx); err != nil {
			t.Fatal(err)
		}
	}
	desktop.local.setDomains("sogo.uni-heidelberg.de", "heico.uni-heidelberg.de", "lsf.uni-heidelberg.de")
	desktop.Notify(false)
	for _, d := range []device{desktop, laptop} {
		if err := d.Sync(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []device{laptop, desktop} {
		if got := d.local.getDomains(); len(got) != 3 {
			t.Fatalf("%s: websites %v", d.opts.Name, got)
		}
	}
}

// flakyRelay fails one group read on demand, like a moment without network.
type flakyRelay struct {
	next http.Handler
	fail atomic.Bool
}

func (f *flakyRelay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/group/") && f.fail.CompareAndSwap(true, false) {
		http.Error(w, "unavailable", 503)
		return
	}
	f.next.ServeHTTP(w, r)
}

// The first code's join stops halfway; a new code then sets the device up, not only says
// linked.
func TestNewCodeFinishesAJoinThatStopped(t *testing.T) {
	ctx := context.Background()
	_, target := startRelay(t)
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	relay := &flakyRelay{next: httputil.NewSingleHostReverseProxy(u)}
	srv := httptest.NewServer(relay)
	t.Cleanup(srv.Close)
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	laptop := newDevice(t, srv.URL, "Laptop", setUpLaptop(), c)
	desktop := newDevice(t, srv.URL, "Desktop", newLocal(false, nil), c)
	p, err := laptop.Offer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	relay.fail.Store(true)
	if err := desktop.Enter(ctx, p.Code); !errors.Is(err, ErrOffline) {
		t.Fatalf("first code: %v", err)
	}
	laptop.CancelPairing()
	pair(t, ctx, laptop, desktop)
	if !desktop.local.SetupDone() || desktop.local.get(KeyPassword) != `"secret"` {
		t.Fatalf("linked, but the desktop is not set up: %v", desktop.local.values)
	}
}

// Every website ever listed stays in the group as a key, so a removal moves a large document.
func TestRemoveWithManyWebsites(t *testing.T) {
	ctx := context.Background()
	_, url := startRelay(t)
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	laptop := newDevice(t, url, "Laptop", setUpLaptop(), c)
	desktop := newDevice(t, url, "Desktop", newLocal(false, nil), c)
	pair(t, ctx, laptop, desktop)
	var hosts []string
	for i := range 800 {
		hosts = append(hosts, fmt.Sprintf("journal%d.publisher-example.com", i))
	}
	laptop.local.setDomains(hosts...)
	laptop.Notify(false)
	if err := laptop.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if err := laptop.Remove(ctx, otherID(laptop.Engine, "Desktop")); err != nil {
		t.Fatal(err)
	}
	if got := names(laptop.Engine); !slices.Equal(got, []string{"Laptop"}) {
		t.Fatalf("after removal %v", got)
	}
}

// lossyRelay loses group writes: dropped on the way (no network for a moment, a relay error),
// or written with the answer lost on the way back. drop and lose take the next write; with
// odds set, every read and write fails or loses its answer at random.
type lossyRelay struct {
	next       http.Handler
	drop, lose atomic.Bool
	mu         sync.Mutex
	rng        *rand.Rand
	odds       float64
}

func (f *lossyRelay) chance() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rng != nil && f.rng.Float64() < f.odds
}

func (f *lossyRelay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/v1/group/") {
		switch {
		case r.Method == "GET" && f.chance(), r.Method == "PUT" && (f.drop.CompareAndSwap(true, false) || f.chance()):
			http.Error(w, "unavailable", 503)
			return
		case r.Method == "PUT" && (f.lose.CompareAndSwap(true, false) || f.chance()):
			f.next.ServeHTTP(httptest.NewRecorder(), r)
			http.Error(w, "gateway timeout", 504)
			return
		}
	}
	f.next.ServeHTTP(w, r)
}

// lossyDevices is the set-up laptop and new devices linked to it, all through a lossyRelay.
func lossyDevices(t *testing.T, names ...string) (*lossyRelay, []device) {
	t.Helper()
	_, target := startRelay(t)
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	relay := &lossyRelay{next: httputil.NewSingleHostReverseProxy(u)}
	srv := httptest.NewServer(relay)
	t.Cleanup(srv.Close)
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	devices := []device{newDevice(t, srv.URL, "Laptop", setUpLaptop(), c)}
	for _, name := range names {
		d := newDevice(t, srv.URL, name, newLocal(false, nil), c)
		pair(t, context.Background(), devices[0], d)
		devices = append(devices, d)
	}
	return relay, devices
}

func syncAll(t *testing.T, devices ...device) {
	t.Helper()
	for _, d := range devices {
		if err := d.Sync(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

func without(list []string, host string) []string {
	return slices.DeleteFunc(slices.Clone(list), func(h string) bool { return h == host })
}

// A round takes over a website and cannot write (no network for a moment). Then the website
// is removed on the laptop and something else changed on the desktop: the removal stays.
func TestFailedWriteDoesNotBringBackARemovedWebsite(t *testing.T) {
	relay, devices := lossyDevices(t, "Desktop")
	laptop, desktop := devices[0], devices[1]
	const lsf = "lsf.uni-heidelberg.de"
	laptop.local.setDomains(append(laptop.local.getDomains(), lsf)...)
	laptop.Notify(false)
	syncAll(t, laptop)
	desktop.local.set(KeyLanguage, "de") // the desktop's round has something to write
	desktop.Notify(false)
	relay.drop.Store(true)
	if err := desktop.Sync(context.Background()); err == nil || !slices.Contains(desktop.local.getDomains(), lsf) {
		t.Fatalf("want the website taken over and the write failed: %v %v", err, desktop.local.getDomains())
	}
	laptop.local.setDomains(without(laptop.local.getDomains(), lsf)...)
	laptop.Notify(false)
	syncAll(t, laptop)
	desktop.local.set(KeyAutoUpdate, false)
	desktop.Notify(false)
	syncAll(t, desktop, laptop, desktop)
	for _, d := range devices {
		if slices.Contains(d.local.getDomains(), lsf) {
			t.Fatalf("%s: the removed website is back: %v", d.opts.Name, d.local.getDomains())
		}
	}
}

// The same with a value: the laptop's later language stays.
func TestFailedWriteDoesNotUndoALaterLanguage(t *testing.T) {
	relay, devices := lossyDevices(t, "Desktop")
	laptop, desktop := devices[0], devices[1]
	laptop.local.set(KeyLanguage, "fr")
	laptop.Notify(false)
	syncAll(t, laptop)
	desktop.local.setDomains(append(desktop.local.getDomains(), "lsf.uni-heidelberg.de")...)
	desktop.Notify(false)
	relay.drop.Store(true)
	if err := desktop.Sync(context.Background()); err == nil || desktop.local.get(KeyLanguage) != `"fr"` {
		t.Fatalf("want fr taken over and the write failed: %v %s", err, desktop.local.get(KeyLanguage))
	}
	laptop.local.set(KeyLanguage, "es")
	laptop.Notify(false)
	syncAll(t, laptop)
	desktop.local.set(KeyAutoUpdate, false)
	desktop.Notify(false)
	syncAll(t, desktop, laptop, desktop)
	for _, d := range devices {
		if got := d.local.get(KeyLanguage); got != `"es"` {
			t.Fatalf("%s: language %s, want the later es", d.opts.Name, got)
		}
	}
}

// The relay took a new website but its answer got lost; the website is removed again before
// the next round. The removal reaches the group.
func TestRemovalAfterALostAnswerGoesOut(t *testing.T) {
	relay, devices := lossyDevices(t, "Desktop")
	laptop, desktop := devices[0], devices[1]
	const lsf = "lsf.uni-heidelberg.de"
	desktop.local.setDomains(append(desktop.local.getDomains(), lsf)...)
	desktop.Notify(false)
	relay.lose.Store(true)
	if err := desktop.Sync(context.Background()); err == nil {
		t.Fatal("want the answer lost")
	}
	desktop.local.setDomains(without(desktop.local.getDomains(), lsf)...)
	desktop.Notify(false)
	syncAll(t, desktop, laptop, desktop)
	for _, d := range devices {
		if slices.Contains(d.local.getDomains(), lsf) {
			t.Fatalf("%s: the removed website is back: %v", d.opts.Name, d.local.getDomains())
		}
	}
}

// Three devices edit and sync at random while reads, writes and answers get lost. Once the
// network is back they agree on the latest language and on every website added and not
// removed since. The seeds found undone changes and lost removals before.
func TestRandomEditsOverALossyNetwork(t *testing.T) {
	for _, seed := range []uint64{1, 2, 3, 4} {
		t.Run(fmt.Sprint(seed), func(t *testing.T) { randomEdits(t, seed) })
	}
}

func randomEdits(t *testing.T, seed uint64) {
	relay, devices := lossyDevices(t, "Desktop", "Phone")
	syncAll(t, devices...)
	rng := rand.New(rand.NewPCG(seed, 0))
	relay.mu.Lock()
	relay.rng, relay.odds = rand.New(rand.NewPCG(seed, 1)), 0.15
	relay.mu.Unlock()
	language, removed := "", map[string]bool{}
	var added []string
	for step := range 120 {
		d := devices[rng.IntN(len(devices))]
		switch rng.IntN(4) {
		case 0:
			language = fmt.Sprintf("l%d", step)
			d.local.set(KeyLanguage, language)
			d.Notify(false)
		case 1:
			host := fmt.Sprintf("site%d.example.org", step)
			d.local.setDomains(append(d.local.getDomains(), host)...)
			d.Notify(false)
			added = append(added, host)
		case 2:
			// A website this device has, once: whoever removes it, it is gone for good.
			var listed []string
			for _, host := range d.local.getDomains() {
				if strings.HasPrefix(host, "site") && !removed[host] {
					listed = append(listed, host)
				}
			}
			if len(listed) > 0 {
				host := listed[rng.IntN(len(listed))]
				d.local.setDomains(without(d.local.getDomains(), host)...)
				d.Notify(false)
				removed[host] = true
			}
		default:
			d.Sync(context.Background()) // fails now and then
		}
	}
	relay.mu.Lock()
	relay.rng = nil
	relay.mu.Unlock()
	for range 3 {
		syncAll(t, devices...)
	}
	var want []string
	for _, host := range added {
		if !removed[host] {
			want = append(want, host)
		}
	}
	slices.Sort(want)
	for _, d := range devices {
		got := slices.DeleteFunc(d.local.getDomains(), func(h string) bool { return !strings.HasPrefix(h, "site") })
		slices.Sort(got)
		if language != "" && d.local.get(KeyLanguage) != `"`+language+`"` || !slices.Equal(got, want) {
			t.Fatalf("%s: language %s, want %s; websites %v, want %v", d.opts.Name, d.local.get(KeyLanguage), language, got, want)
		}
	}
}
