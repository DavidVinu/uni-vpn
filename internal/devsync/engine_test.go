package devsync

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
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
