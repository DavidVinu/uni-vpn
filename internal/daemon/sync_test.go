//go:build !windows

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DavidVinu/uni-vpn/internal/config"
	"github.com/DavidVinu/uni-vpn/internal/devsync"
	"github.com/DavidVinu/uni-vpn/internal/devsync/relaytest"
	"github.com/DavidVinu/uni-vpn/internal/httpapi"
	"github.com/DavidVinu/uni-vpn/internal/messages"
	"github.com/DavidVinu/uni-vpn/internal/pyjson"
)

type memSyncStore struct {
	mu    sync.Mutex
	value string
}

func (s *memSyncStore) Load(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.value, nil
}

func (s *memSyncStore) Save(v string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.value = v
	return nil
}

func (s *memSyncStore) Delete() error { return s.Save("") }

func syncOptions(url, dir string, edit func(*Options)) func(*Options) {
	return func(o *Options) {
		o.SyncRelay, o.SyncStore, o.SyncStatePath = url, &memSyncStore{}, filepath.Join(dir, "sync.json")
		if edit != nil {
			edit(o)
		}
	}
}

func syncDevices(t *testing.T, d *Daemon) []any {
	t.Helper()
	s, _ := statusValue(t, d, "sync").(pyjson.Object).Get("devices")
	return s.([]any)
}

func TestSyncSetsUpAnotherDevice(t *testing.T) {
	_, url := relaytest.Start(t)

	laptopH := newHarness(t)
	laptopH.cfg.User = "ab123"
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := config.WriteInitial(path, "ab123", "heidelberg", []config.Setting{{Key: "authgroup", Value: "staff"}}); err != nil {
		t.Fatal(err)
	}
	laptop := laptopH.start(syncOptions(url, dir, func(o *Options) { o.ConfigPath = path }))
	if _, err := laptop.SetDomains("sogo.uni-heidelberg.de\nexample.org"); err != nil {
		t.Fatal(err)
	}

	desktopH := newHarness(t)
	desktopDir := t.TempDir()
	desktopPath := filepath.Join(desktopDir, "config.toml")
	desktop := desktopH.start(syncOptions(url, desktopDir, func(o *Options) {
		o.NeedsSetup, o.ConfigPath = true, desktopPath
	}))
	if _, err := desktop.Sync("offer", pyjson.O()); err == nil || !strings.Contains(err.Error(), "Set up") {
		t.Fatalf("offer before setup: %v", err)
	}

	offer, err := laptop.Sync("offer", pyjson.O())
	if err != nil {
		t.Fatal(err)
	}
	pairing, _ := offer.Get("pairing")
	code, _ := pairing.(pyjson.Object).Get("code")
	if _, err := desktop.Sync("enter", pyjson.O("code", code)); err != nil {
		t.Fatal(err)
	}

	if statusValue(t, desktop, "setup_needed") != false || statusValue(t, desktop, "user") != "ab123" {
		t.Fatal("desktop not set up")
	}
	written, err := config.Load(desktopPath)
	if err != nil || written.Authgroup != "staff" || written.University != "heidelberg" {
		t.Fatalf("%v %+v", err, written)
	}
	desktopH.mu.Lock()
	stored, storedTOTP := slices.Clone(desktopH.stored), slices.Clone(desktopH.storedTOTP)
	desktopH.mu.Unlock()
	if !slices.Equal(stored, []string{"pw-s3cret"}) || !slices.Equal(storedTOTP, []string{"base32:GEZDGNBVGY3TQOJQ"}) {
		t.Fatal(stored, storedTOTP)
	}
	if data, _ := os.ReadFile(desktopH.domainsPath); !strings.Contains(string(data), "example.org") {
		t.Fatalf("domains %q", data)
	}

	// The laptop sees both devices once the code was used.
	deadline := time.Now().Add(5 * time.Second)
	for len(syncDevices(t, laptop)) != 2 && time.Now().Before(deadline) {
		laptop.sync.Sync(context.Background())
		time.Sleep(50 * time.Millisecond)
	}
	if n := len(syncDevices(t, laptop)); n != 2 {
		t.Fatalf("%d devices", n)
	}

	// A change on the desktop reaches the laptop.
	if err := desktop.SetLanguage("de"); err != nil {
		t.Fatal(err)
	}
	for _, d := range []*Daemon{desktop, laptop} {
		if err := d.sync.Sync(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if statusValue(t, laptop, "language") != "de" {
		t.Fatal("language not synced")
	}

	if _, err := laptop.Sync("enter", pyjson.O("code", "AAAA-AAAA-AAAA")); err == nil {
		t.Fatal("unknown code accepted")
	}
	if _, err := laptop.Sync("nothing", pyjson.O()); err != httpapi.ErrNotFound {
		t.Fatal(err)
	}
}

func TestSyncHiddenWithoutRelay(t *testing.T) {
	t.Setenv("UNI_VPN_SYNC_RELAY", "")
	h := newHarness(t)
	d := h.start(nil)
	s := statusValue(t, d, "sync").(pyjson.Object)
	if available, _ := s.Get("available"); available != false {
		t.Fatal(s)
	}
}

// A second factor saved here is noted for syncing on its own: with the password's time, an older
// password typed here would win over a newer one from another device. The other way round too.
func TestSyncNotesOnlyTheSecretThatChanged(t *testing.T) {
	for want, set := range map[string]func(*Daemon) error{
		devsync.KeyTOTP:     func(d *Daemon) error { return d.SetTOTP("JBSWY3DPEHPK3PXP") },
		devsync.KeyPassword: func(d *Daemon) error { return d.SetPassword("new") },
	} {
		dir := t.TempDir()
		d := newHarness(t).start(syncOptions("http://127.0.0.1:1", dir, nil)) // not linked: the note stays
		if err := set(d); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(filepath.Join(dir, "sync.json.pending"))
		var p struct {
			Changes map[string]json.RawMessage `json:"changes"`
		}
		json.Unmarshal(data, &p)
		if got := slices.Sorted(maps.Keys(p.Changes)); !slices.Equal(got, []string{want}) {
			t.Errorf("noted %v, want only %s", got, want)
		}
	}
}

// syncedPair is a set-up laptop and a desktop it has set up with a code.
func syncedPair(t *testing.T, overrides []config.Setting) (laptop *Daemon, laptopH *harness, desktop *Daemon, desktopH *harness) {
	t.Helper()
	_, url := relaytest.Start(t)
	laptopH = newHarness(t)
	laptopH.cfg.User = "ab123"
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := config.WriteInitial(path, "ab123", "heidelberg", overrides); err != nil {
		t.Fatal(err)
	}
	laptop = laptopH.start(syncOptions(url, dir, func(o *Options) { o.ConfigPath = path }))
	desktopH = newHarness(t)
	desktopDir := t.TempDir()
	desktop = desktopH.start(syncOptions(url, desktopDir, func(o *Options) {
		o.NeedsSetup, o.ConfigPath = true, filepath.Join(desktopDir, "config.toml")
	}))
	offer, err := laptop.Sync("offer", pyjson.O())
	if err != nil {
		t.Fatal(err)
	}
	pairing, _ := offer.Get("pairing")
	code, _ := pairing.(pyjson.Object).Get("code")
	if _, err := desktop.Sync("enter", pyjson.O("code", code)); err != nil {
		t.Fatal(err)
	}
	if !waitFor(5*time.Second, func() bool { laptop.sync.Sync(context.Background()); return len(syncDevices(t, laptop)) == 2 }) {
		t.Fatal("the laptop does not see the desktop")
	}
	return laptop, laptopH, desktop, desktopH
}

// A password changed on one device is for the others' next connection: an idle one does not
// connect by itself (with Duo, every device would send a push).
func TestSyncedPasswordDoesNotConnect(t *testing.T) {
	ctx := context.Background()
	laptop, laptopH, desktop, desktopH := syncedPair(t, nil)
	waitState(t, laptop, Idle)
	waitState(t, desktop, Idle, 10*time.Second)
	desktopH.mu.Lock()
	desktopH.password = []byte("new-password")
	desktopH.mu.Unlock()
	if err := desktop.SetPassword("new-password"); err != nil {
		t.Fatal(err)
	}
	if err := desktop.sync.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if err := laptop.sync.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	laptopH.mu.Lock()
	stored := slices.Clone(laptopH.stored)
	laptopH.mu.Unlock()
	if !slices.Equal(stored, []string{"new-password"}) {
		t.Fatalf("laptop stored %v", stored)
	}
	if waitFor(time.Second, func() bool { s, _ := laptop.snapshot(); return s != Idle }) {
		t.Fatalf("the laptop connected by itself:\n%s", laptopH.logs.String())
	}
	// One that failed with the old password tries again with the new one.
	laptop.forceSet(AuthFailed, messages.PasswordRejected)
	before := len(laptopH.logs.String())
	desktopH.mu.Lock()
	desktopH.password = []byte("newer-password")
	desktopH.mu.Unlock()
	if err := desktop.SetPassword("newer-password"); err != nil {
		t.Fatal(err)
	}
	if err := desktop.sync.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if err := laptop.sync.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if !waitFor(5*time.Second, func() bool { return strings.Contains(laptopH.logs.String()[before:], "-> connecting") }) {
		t.Fatalf("the laptop did not try the new password:\n%s", laptopH.logs.String())
	}
}

// Two devices with the same overrides in another order in config.toml: each would send its
// order to the other as a change, for ever, every time taking the tunnel down.
func TestSyncedOverridesTakeTheGroupsOrder(t *testing.T) {
	ctx := context.Background()
	laptop, laptopH, desktop, desktopH := syncedPair(t, []config.Setting{{Key: "authgroup", Value: "staff"}})
	taken := func() int {
		return strings.Count(laptopH.logs.String()+desktopH.logs.String(), "Synced from other devices: account")
	}
	paired := taken()
	// On the laptop, Settings: another university, then back with one more override.
	pw, totp := "pw-s3cret", "base32:GEZDGNBVGY3TQOJQ"
	if err := laptop.CompleteSetup("ab123", pw, totp, "bremen", nil); err != nil {
		t.Fatal(err)
	}
	if err := laptop.CompleteSetup("ab123", pw, totp, "heidelberg",
		[]config.Setting{{Key: "usergroup", Value: "x"}, {Key: "authgroup", Value: "staff"}}); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		for _, d := range []*Daemon{laptop, desktop} {
			if err := d.sync.Sync(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	applied := taken() - paired
	laptopOrder, _ := config.Overrides(laptop.configPath)
	desktopOrder, _ := config.Overrides(desktop.configPath)
	if applied != 1 || !slices.Equal(laptopOrder, desktopOrder) {
		t.Fatalf("account taken over %d times; laptop %v, desktop %v", applied, laptopOrder, desktopOrder)
	}
}

// Settings, Account on a desktop whose sync.json a crash left unreadable (the keyring kept the
// sync keys): the user typed there goes out, and a website the laptop removed meanwhile does not
// come back from the desktop's list.
func TestSyncAccountTypedAfterSyncStateWasLost(t *testing.T) {
	ctx := context.Background()
	laptop, _, desktop, _ := syncedPair(t, nil)
	domains := statusValue(t, laptop, "domains").([]string)
	if len(domains) < 2 {
		t.Fatalf("websites %v", domains)
	}
	gone := domains[0]
	if _, err := laptop.SetDomains(strings.Join(domains[1:], "\n")); err != nil {
		t.Fatal(err)
	}
	if err := laptop.sync.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(desktop.opts.SyncStatePath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := desktop.CompleteSetup("cd456", "pw-s3cret", "base32:GEZDGNBVGY3TQOJQ", "heidelberg", nil); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		for _, d := range []*Daemon{desktop, laptop} {
			if err := d.sync.Sync(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	for name, d := range map[string]*Daemon{"laptop": laptop, "desktop": desktop} {
		user, listed := statusValue(t, d, "user"), statusValue(t, d, "domains").([]string)
		if user != "cd456" || slices.Contains(listed, gone) {
			t.Errorf("%s: user %v, websites %v; want the cd456 typed on the desktop, %s removed", name, user, listed, gone)
		}
	}
}

func TestSyncErrorsForTheApp(t *testing.T) {
	notUsed := &devsync.ApplyError{Keys: []string{"language"}, Err: errors.New("unknown language 'it'")}
	for _, c := range []struct {
		err  error
		want string
	}{
		{notUsed, "sync.not_used"},
		{fmt.Errorf("%w: %w", devsync.ErrOffline, errors.New("dial tcp")), "sync.offline"},
		{devsync.ErrCodeUnknown, "sync.code_unknown"},
		// The keyring's or the engine's own words go to the log (Remove), not to the page.
		{errors.New("sync data could not be decrypted"), "sync.failed"},
	} {
		var ve *httpapi.ValueError
		if !errors.As(syncError(c.err), &ve) || ve.Text == nil || ve.Text.Key != c.want {
			t.Errorf("%v: %#v", c.err, syncError(c.err))
		}
	}
	// "Use settings from another device": linked, but the account could not be used here.
	p := pairingJSON(&devsync.Pairing{Mode: "request", State: "done", Error: notUsed}).(pyjson.Object)
	if text, _ := p.Get("error"); text == nil || !strings.Contains(fmt.Sprint(text), "sync.not_used") {
		t.Fatalf("pairing %v", p)
	}
}

// syncErrorKey is the catalog key of an "error" in a sync answer or status, "" without one.
func syncErrorKey(t *testing.T, v any) string {
	t.Helper()
	if v == nil {
		return ""
	}
	key, _ := v.(pyjson.Object).Get("key")
	return key.(string)
}

// An account from another device that cannot be set up here (the keyring refused for a
// moment) leaves config.toml as it was. With "Not listed" the gateway's address is one of the
// overrides: without it neither device would start set up any more.
func TestFailedSyncedAccountKeepsTheGateway(t *testing.T) {
	ctx := context.Background()
	laptop, _, desktop, desktopH := syncedPair(t, nil)
	pw, totp := "pw-s3cret", "base32:GEZDGNBVGY3TQOJQ"
	gateway := []config.Setting{{Key: "host", Value: "vpn.example.edu"}, {Key: "mfa", Value: "totp_field"}}
	if err := laptop.CompleteSetup("ab123", pw, totp, "other", gateway); err != nil {
		t.Fatal(err)
	}
	for _, d := range []*Daemon{laptop, desktop} {
		if err := d.sync.Sync(ctx); err != nil {
			t.Fatal(err)
		}
	}
	// One more override on the laptop, and the desktop's keyring refuses once.
	if err := laptop.CompleteSetup("ab123", pw, totp, "other",
		append(gateway, config.Setting{Key: "authgroup", Value: "staff"})); err != nil {
		t.Fatal(err)
	}
	if err := laptop.sync.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	desktopH.mu.Lock()
	desktopH.totpSetter = func(string) error { return errors.New("keyring locked") }
	desktopH.mu.Unlock()
	var ae *devsync.ApplyError
	if err := desktop.sync.Sync(ctx); !errors.As(err, &ae) {
		t.Fatalf("got %v", err)
	}
	desktopH.mu.Lock()
	desktopH.totpSetter = nil
	desktopH.mu.Unlock()
	for range 2 {
		for _, d := range []*Daemon{desktop, laptop} {
			if err := d.sync.Sync(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, d := range []*Daemon{laptop, desktop} {
		overrides, _ := config.Overrides(d.configPath)
		if _, err := config.Load(d.configPath); err != nil || len(overrides) != 3 {
			t.Fatalf("%s: %v, overrides %v", d.configPath, err, overrides)
		}
	}
}

// Linked, but the second factor could not be stored here: the code worked, the answer says
// so with a note, and Settings keeps showing it until a sync goes through.
func TestSyncEnterLinksWithANote(t *testing.T) {
	relay, _ := relaytest.Start(t)
	var down atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			http.Error(w, "unavailable", 503)
			return
		}
		relay.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	url := srv.URL
	laptopH := newHarness(t)
	laptopH.cfg.User = "ab123"
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := config.WriteInitial(path, "ab123", "heidelberg", nil); err != nil {
		t.Fatal(err)
	}
	laptop := laptopH.start(syncOptions(url, dir, func(o *Options) { o.ConfigPath = path }))
	desktopH := newHarness(t)
	desktopH.totpSetter = func(string) error { return errors.New("keyring locked") }
	desktopDir := t.TempDir()
	desktop := desktopH.start(syncOptions(url, desktopDir, func(o *Options) {
		o.NeedsSetup, o.ConfigPath = true, filepath.Join(desktopDir, "config.toml")
	}))
	offer, err := laptop.Sync("offer", pyjson.O())
	if err != nil {
		t.Fatal(err)
	}
	pairing, _ := offer.Get("pairing")
	code, _ := pairing.(pyjson.Object).Get("code")
	answer, err := desktop.Sync("enter", pyjson.O("code", code))
	if err != nil {
		t.Fatal(err)
	}
	if note, _ := answer.Get("error_t"); syncErrorKey(t, note) != "sync.not_used" {
		t.Fatalf("answer %v", answer)
	}
	problem, _ := statusValue(t, desktop, "sync").(pyjson.Object).Get("error")
	if syncErrorKey(t, problem) != "sync.not_used" {
		t.Fatalf("status error %v", problem)
	}
	if !waitFor(5*time.Second, func() bool { laptop.sync.Sync(context.Background()); return len(syncDevices(t, laptop)) == 2 }) {
		t.Fatal("the laptop does not see the desktop")
	}
	// While the relay does not answer, then once it does again.
	down.Store(true)
	laptop.sync.Sync(context.Background())
	problem, _ = statusValue(t, laptop, "sync").(pyjson.Object).Get("error")
	if syncErrorKey(t, problem) != "sync.failed" {
		t.Fatalf("status error %v", problem)
	}
	down.Store(false)
	if err := laptop.sync.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if problem, _ = statusValue(t, laptop, "sync").(pyjson.Object).Get("error"); problem != nil {
		t.Fatalf("status error %v after a good sync", problem)
	}
}

// linkedWithoutAccount is a set-up laptop with the language de and automatic updates off, and a
// desktop linked to it whose keyring refused the second factor: linked, still to be set up.
// Both keyrings give back what was stored. groupReads counts the relay's group reads.
func linkedWithoutAccount(t *testing.T) (laptop *Daemon, laptopH *harness, desktop *Daemon, desktopH *harness, groupReads *atomic.Int64) {
	t.Helper()
	ctx := context.Background()
	relay, _ := relaytest.Start(t)
	groupReads = &atomic.Int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/group/") {
			groupReads.Add(1)
		}
		relay.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	keyring := func(h *harness) func(*Options) {
		return func(o *Options) {
			o.PasswordSetter = func(pw string) error {
				h.mu.Lock()
				defer h.mu.Unlock()
				h.stored, h.password = append(h.stored, pw), []byte(pw)
				return nil
			}
		}
	}
	laptopH = newHarness(t)
	laptopH.cfg.User = "ab123"
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := config.WriteInitial(path, "ab123", "heidelberg", nil); err != nil {
		t.Fatal(err)
	}
	laptop = laptopH.start(syncOptions(srv.URL, dir, func(o *Options) { o.ConfigPath = path; keyring(laptopH)(o) }))
	if err := laptop.SetLanguage("de"); err != nil {
		t.Fatal(err)
	}
	if err := laptop.SetAutoUpdate(false); err != nil {
		t.Fatal(err)
	}
	if err := laptop.sync.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	desktopH = newHarness(t)
	desktopH.totpSetter = func(string) error { return errors.New("keyring locked") }
	desktopDir := t.TempDir()
	desktop = desktopH.start(syncOptions(srv.URL, desktopDir, func(o *Options) {
		o.NeedsSetup, o.ConfigPath = true, filepath.Join(desktopDir, "config.toml")
		keyring(desktopH)(o)
	}))
	offer, err := laptop.Sync("offer", pyjson.O())
	if err != nil {
		t.Fatal(err)
	}
	pairing, _ := offer.Get("pairing")
	code, _ := pairing.(pyjson.Object).Get("code")
	if _, err := desktop.Sync("enter", pyjson.O("code", code)); err != nil {
		t.Fatal(err)
	}
	if statusValue(t, desktop, "setup_needed") != true {
		t.Fatal("want the desktop still to be set up")
	}
	return laptop, laptopH, desktop, desktopH, groupReads
}

// Waiting for setup by hand, the desktop does not sync over and over: each round took over the
// websites again and woke the next.
func TestLinkedWithoutTheAccountWaitsQuietly(t *testing.T) {
	_, _, _, desktopH, groupReads := linkedWithoutAccount(t)
	before := groupReads.Load()
	desktopH.mu.Lock()
	stored := len(desktopH.stored)
	desktopH.mu.Unlock()
	time.Sleep(time.Second)
	desktopH.mu.Lock()
	stored = len(desktopH.stored) - stored
	desktopH.mu.Unlock()
	if n := groupReads.Load() - before; n > 3 {
		t.Fatalf("%d group reads and %d passwords stored in a second", n, stored)
	}
}

// Then set up by hand: the laptop's language and automatic updates stay, the password typed
// on the desktop reaches the laptop.
func TestSetUpByHandAfterALinkKeepsTheGroupsSettings(t *testing.T) {
	ctx := context.Background()
	laptop, laptopH, desktop, desktopH, _ := linkedWithoutAccount(t)
	time.Sleep(200 * time.Millisecond) // the round the link woke is over (the user reads the note)
	desktop.sync.Sync(ctx)
	desktopH.mu.Lock()
	desktopH.totpSetter = nil
	desktopH.mu.Unlock()
	if err := desktop.CompleteSetup("ab123", "typed-by-hand", "base32:GEZDGNBVGY3TQOJQ", "heidelberg", nil); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		for _, d := range []*Daemon{desktop, laptop} {
			if err := d.sync.Sync(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	for name, d := range map[string]*Daemon{"laptop": laptop, "desktop": desktop} {
		if lang, auto := statusValue(t, d, "language"), statusValue(t, d, "auto_update"); lang != "de" || auto != false {
			t.Errorf("%s: language %v, automatic updates %v; want the laptop's de and off", name, lang, auto)
		}
	}
	for name, h := range map[string]*harness{"laptop": laptopH, "desktop": desktopH} {
		h.mu.Lock()
		stored := slices.Clone(h.stored)
		h.mu.Unlock()
		if len(stored) == 0 || stored[len(stored)-1] != "typed-by-hand" {
			t.Errorf("%s stored %v, want the password typed by hand last", name, stored)
		}
	}
}

// The desktop's app was removed and installed again: config.toml is gone, sync.json, the website
// list and the keyring stayed. Its first round does not go through before the user sets it up
// by hand: the new config.toml's language and automatic updates do not replace the laptop's.
func TestSyncSetUpByHandAfterTheAppWasInstalledAgain(t *testing.T) {
	ctx := context.Background()
	relay, _ := relaytest.Start(t)
	var down atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			http.Error(w, "unavailable", 503)
			return
		}
		relay.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	laptopH := newHarness(t)
	laptopH.cfg.User = "ab123"
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := config.WriteInitial(path, "ab123", "heidelberg", nil); err != nil {
		t.Fatal(err)
	}
	laptop := laptopH.start(syncOptions(srv.URL, dir, func(o *Options) { o.ConfigPath = path }))
	store := &memSyncStore{}
	desktopDir := t.TempDir()
	desktopPath := filepath.Join(desktopDir, "config.toml")
	desktopH := newHarness(t)
	installed := func(h *harness) *Daemon {
		return h.start(syncOptions(srv.URL, desktopDir, func(o *Options) {
			o.NeedsSetup, o.ConfigPath, o.SyncStore, o.DomainsPath = true, desktopPath, store, desktopH.domainsPath
		}))
	}
	desktop := installed(desktopH)
	offer, err := laptop.Sync("offer", pyjson.O())
	if err != nil {
		t.Fatal(err)
	}
	pairing, _ := offer.Get("pairing")
	code, _ := pairing.(pyjson.Object).Get("code")
	if _, err := desktop.Sync("enter", pyjson.O("code", code)); err != nil {
		t.Fatal(err)
	}
	if err := laptop.SetLanguage("de"); err != nil {
		t.Fatal(err)
	}
	if err := laptop.SetAutoUpdate(false); err != nil {
		t.Fatal(err)
	}
	for _, d := range []*Daemon{laptop, desktop, laptop} {
		if err := d.sync.Sync(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if lang := statusValue(t, desktop, "language"); lang != "de" {
		t.Fatalf("desktop language %v", lang)
	}
	desktopH.teardown()
	desktopH.d = nil
	if err := os.Remove(desktopPath); err != nil {
		t.Fatal(err)
	}
	down.Store(true)
	desktop = installed(newHarness(t))
	if err := desktop.CompleteSetup("ab123", "pw-s3cret", "base32:GEZDGNBVGY3TQOJQ", "heidelberg", nil); err != nil {
		t.Fatal(err)
	}
	down.Store(false)
	for range 2 {
		for _, d := range []*Daemon{desktop, laptop} {
			if err := d.sync.Sync(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	for name, d := range map[string]*Daemon{"laptop": laptop, "desktop": desktop} {
		if lang, auto := statusValue(t, d, "language"), statusValue(t, d, "auto_update"); lang != "de" || auto != false {
			t.Errorf("%s: language %v, automatic updates %v; want the laptop's de and off", name, lang, auto)
		}
	}
}

// newerLocal is a device of a newer version: it syncs a setting this version does not know.
type newerLocal struct {
	mu     sync.Mutex
	values map[string]json.RawMessage
}

func (l *newerLocal) Snapshot(_ context.Context, withSecrets bool) (map[string]json.RawMessage, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := map[string]json.RawMessage{}
	for key, v := range l.values {
		secret := key == devsync.KeyPassword || key == devsync.KeyTOTP
		if secret && !withSecrets || strings.HasPrefix(key, devsync.DomainPrefix) && string(v) != "true" {
			continue
		}
		out[key] = v
	}
	return out, nil
}

func (l *newerLocal) Apply(_ context.Context, all map[string]devsync.Entry, keys []string) map[string]error {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, key := range keys {
		l.values[key] = all[key].V
	}
	return nil
}

func (l *newerLocal) SetupDone() bool { return true }

// A tablet with a newer version syncs a setting this version does not know. The laptop syncs
// on without a note and without noting it as taken over (after an update it takes the
// tablet's value instead of sending its default), its own changes still go out, and it does
// not sync over and over.
func TestSyncSettingOfANewerVersion(t *testing.T) {
	ctx := context.Background()
	relay, _ := relaytest.Start(t)
	var groupReads atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/group/") {
			groupReads.Add(1)
		}
		relay.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	laptopH := newHarness(t)
	laptopH.cfg.User = "ab123"
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := config.WriteInitial(path, "ab123", "heidelberg", nil); err != nil {
		t.Fatal(err)
	}
	laptop := laptopH.start(syncOptions(srv.URL, dir, func(o *Options) { o.ConfigPath = path }))
	tabletLocal := &newerLocal{values: map[string]json.RawMessage{"theme": json.RawMessage(`"dark"`)}}
	tablet := devsync.New(devsync.Options{Relay: srv.URL, StatePath: filepath.Join(t.TempDir(), "sync.json"),
		Store: &memSyncStore{}, Local: tabletLocal, Name: "Tablet", Platform: "android"})
	offer, err := laptop.Sync("offer", pyjson.O())
	if err != nil {
		t.Fatal(err)
	}
	pairing, _ := offer.Get("pairing")
	code, _ := pairing.(pyjson.Object).Get("code")
	if err := tablet.Enter(ctx, code.(string)); err != nil {
		t.Fatal(err)
	}
	if !waitFor(5*time.Second, func() bool { // the laptop's round for the new device is under way
		p, _ := statusValue(t, laptop, "sync").(pyjson.Object).Get("pairing")
		shown, _ := p.(pyjson.Object)
		state, _ := shown.Get("state")
		return state == "done"
	}) {
		t.Fatal("the laptop does not see the code used")
	}
	if err := tablet.Sync(ctx); err != nil { // the round after joining sends its own setting
		t.Fatal(err)
	}
	if err := laptop.SetLanguage("fr"); err != nil {
		t.Fatal(err)
	}
	if err := laptop.sync.Sync(ctx); err != nil {
		t.Fatalf("got %v, want no note for a newer version's setting", err)
	}
	if problem, _ := statusValue(t, laptop, "sync").(pyjson.Object).Get("error"); problem != nil {
		t.Fatalf("status error %v", problem)
	}
	if state, _ := os.ReadFile(filepath.Join(dir, "sync.json")); !strings.Contains(string(state), `"language"`) ||
		strings.Contains(string(state), `"theme"`) {
		t.Fatalf("sync.json %s", state)
	}
	if err := tablet.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	tabletLocal.mu.Lock()
	language := string(tabletLocal.values[devsync.KeyLanguage])
	tabletLocal.mu.Unlock()
	if language != `"fr"` {
		t.Fatalf("tablet language %s, want the laptop's fr", language)
	}
	time.Sleep(200 * time.Millisecond) // the round SetLanguage woke is over
	before := groupReads.Load()
	time.Sleep(time.Second)
	if n := groupReads.Load() - before; n > 2 {
		t.Fatalf("%d group reads in a second", n)
	}
}
