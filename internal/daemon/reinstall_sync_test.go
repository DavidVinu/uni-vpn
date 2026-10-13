//go:build !windows

package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/DavidVinu/uni-vpn/internal/config"
	"github.com/DavidVinu/uni-vpn/internal/devsync/relaytest"
	"github.com/DavidVinu/uni-vpn/internal/pyjson"
)

// A language picked while the relay was away, then the app removed, installed again and set up
// by hand: the new config.toml's default does not go out to the other devices.
func TestSyncUnsentLanguageThenInstalledAgain(t *testing.T) {
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
	// A language picked on the desktop while offline: not sent before Uni VPN was removed.
	down.Store(true)
	if err := desktop.SetLanguage("fr"); err != nil {
		t.Fatal(err)
	}
	desktop.sync.Sync(ctx) // offline
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

// Installed again and set up by hand, then a language picked before a round went through: the
// pick goes out, the group's older value does not undo it.
func TestSyncLanguagePickedAfterSetUpByHand(t *testing.T) {
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
	// Then, still before a round went through, the user picks French in Settings: a later
	// change than the laptop's.
	if err := desktop.SetLanguage("fr"); err != nil {
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
		if lang, auto := statusValue(t, d, "language"), statusValue(t, d, "auto_update"); lang != "fr" || auto != false {
			t.Errorf("%s: language %v, automatic updates %v; want the desktop's later fr, the laptop's off", name, lang, auto)
		}
	}
}
