//go:build !windows

package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
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

func TestSyncErrorsForTheApp(t *testing.T) {
	notUsed := &devsync.ApplyError{Keys: []string{"language"}, Err: errors.New("unknown language 'it'")}
	for _, c := range []struct {
		err  error
		want string
	}{
		{notUsed, "sync.not_used"},
		{fmt.Errorf("%w: %w", devsync.ErrOffline, errors.New("dial tcp")), "sync.offline"},
		{devsync.ErrCodeUnknown, "sync.code_unknown"},
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
