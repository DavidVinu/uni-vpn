//go:build !windows

package daemon

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DavidVinu/uni-vpn/internal/config"
	"github.com/DavidVinu/uni-vpn/internal/devsync/relaytest"
	"github.com/DavidVinu/uni-vpn/internal/pyjson"
)

// The round the offering device starts once the other device joined ends with the core: it
// writes nothing after the core stopped (on the iPhone a new core may run by then).
func TestTheRoundAfterAJoinEndsWithTheCore(t *testing.T) {
	relay, _ := relaytest.Start(t)
	var slow atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if slow.Load() {
			time.Sleep(300 * time.Millisecond) // a relay reached over a slow network
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
	_, phone := newSyncDevice(t, srv.URL)

	offer, err := laptop.Sync("offer", pyjson.O())
	if err != nil {
		t.Fatal(err)
	}
	p, _ := offer.Get("pairing")
	code, _ := p.(pyjson.Object).Get("code")
	if _, err := phone.Sync("enter", pyjson.O("code", code)); err != nil {
		t.Fatal(err)
	}
	slow.Store(true)
	state := func() any {
		s, _ := statusValue(t, laptop, "sync").(pyjson.Object).Get("pairing")
		if s == nil {
			return nil
		}
		st, _ := s.(pyjson.Object).Get("state")
		return st
	}
	if !waitFor(10*time.Second, func() bool { return state() == "done" }) {
		t.Fatal("the laptop did not see the join:", state())
	}
	laptop.Stop()
	select {
	case <-laptopH.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the laptop did not stop")
	}
	syncPath := filepath.Join(dir, "sync.json")
	before, _ := os.ReadFile(syncPath)
	st0, _ := os.Stat(syncPath)
	time.Sleep(3 * time.Second)
	after, _ := os.ReadFile(syncPath)
	st1, _ := os.Stat(syncPath)
	if !bytes.Equal(before, after) || (st0 == nil) != (st1 == nil) || (st0 != nil && !st0.ModTime().Equal(st1.ModTime())) {
		t.Error("sync.json was written after the core had stopped")
	}
}

// A code entered in the app links this device in the app's request; that join ends with the
// core too: once the core stopped, it writes nothing more (the iPhone's next core may run).
func TestJoinFromTheAppEndsWithTheCore(t *testing.T) {
	relay, _ := relaytest.Start(t)
	var hold atomic.Bool
	blocked, release := make(chan struct{}, 1), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hold.Load() && r.Method == "PUT" && strings.HasPrefix(r.URL.Path, "/v1/group/") {
			select {
			case blocked <- struct{}{}:
			default:
			}
			<-release // the joining device's write, over a slow network
		}
		relay.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })

	_, laptop := setUpSyncDevice(t, srv.URL)
	phoneH := newHarness(t)
	dir := t.TempDir()
	phone := phoneH.start(syncOptions(srv.URL, dir, func(o *Options) {
		o.NeedsSetup, o.ConfigPath = true, filepath.Join(dir, "config.toml")
	}))
	offer, err := laptop.Sync("offer", pyjson.O())
	if err != nil {
		t.Fatal(err)
	}
	p, _ := offer.Get("pairing")
	code, _ := p.(pyjson.Object).Get("code")
	hold.Store(true)
	entered := make(chan error, 1)
	go func() {
		_, err := phone.Sync("enter", pyjson.O("code", code))
		entered <- err
	}()
	select {
	case <-blocked:
	case <-time.After(10 * time.Second):
		t.Fatal("the phone did not write to the group")
	}
	phone.Stop()
	select {
	case <-phoneH.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the phone did not stop")
	}
	syncPath := filepath.Join(dir, "sync.json")
	before, _ := os.ReadFile(syncPath)
	once.Do(func() { close(release) })
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the join did not end")
	}
	time.Sleep(200 * time.Millisecond)
	if after, _ := os.ReadFile(syncPath); !bytes.Equal(before, after) {
		t.Errorf("sync.json was written after the core had stopped:\nbefore %s\nafter  %s", before, after)
	}
}
