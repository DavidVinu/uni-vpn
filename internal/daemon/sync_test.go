package daemon

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DavidVinu/uni-vpn/internal/config"
	"github.com/DavidVinu/uni-vpn/internal/devsync/relaytest"
	"github.com/DavidVinu/uni-vpn/internal/httpapi"
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
