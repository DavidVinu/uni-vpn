package devsync

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A join that a shown request code started (the other device sent the group's key) ends with
// the core, like the round after a join on the offering side: nothing is written after Run
// returned (on the iPhone a new core may run by then).
func TestJoinFromARequestCodeEndsWithTheCore(t *testing.T) {
	ctx := context.Background()
	_, url := startRelay(t)
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	laptop := newDevice(t, url, "Laptop", setUpLaptop(), c)
	phone := newDevice(t, url, "Phone", newLocal(false, nil), c)
	applying, release := make(chan struct{}), make(chan struct{})
	opts := phone.opts
	opts.Local = &hookLocal{fakeLocal: phone.local, onApply: func() {
		close(applying)
		<-release // the join takes over the group's settings while the core stops
	}}
	e := New(opts)
	stop, ran := make(chan struct{}), make(chan struct{})
	go func() { e.Run(stop); close(ran) }()

	p, err := e.Request(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := laptop.Enter(ctx, p.Code); err != nil {
		t.Fatal(err)
	}
	select {
	case <-applying:
	case <-time.After(5 * time.Second):
		t.Fatal("the phone did not take the group's key")
	}
	close(stop)
	go func() {
		time.Sleep(300 * time.Millisecond)
		close(release)
	}()
	select {
	case <-ran:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
	before, _ := os.ReadFile(opts.StatePath)
	time.Sleep(time.Second)
	if after, _ := os.ReadFile(opts.StatePath); !bytes.Equal(before, after) {
		t.Errorf("sync.json was written after Run returned:\nbefore %s\nafter  %s", before, after)
	}
}

// A stop does not wait behind the round after a join while the relay hangs (no answer until
// the client's 30 s run out): Run's own round waits for that one, and the stop ends both.
func TestStopDoesNotWaitBehindTheRoundAfterAJoin(t *testing.T) {
	ctx := context.Background()
	_, target := startRelay(t)
	var hold atomic.Bool
	blocked, release := make(chan struct{}, 1), make(chan struct{})
	u, _ := url.Parse(target)
	proxy := httputil.NewSingleHostReverseProxy(u)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hold.Load() && r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/group/") {
			select {
			case blocked <- struct{}{}:
			default:
			}
			<-release
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	laptop := newDevice(t, srv.URL, "Laptop", setUpLaptop(), c)
	phone := newDevice(t, srv.URL, "Phone", newLocal(false, nil), c)
	stop, ran := make(chan struct{}), make(chan struct{})
	go func() { laptop.Run(stop); close(ran) }()

	p, err := laptop.Offer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := phone.Enter(ctx, p.Code); err != nil {
		t.Fatal(err)
	}
	hold.Store(true) // the laptop's round after the join hangs at its read
	select {
	case <-blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("the laptop did not start the round after the join")
	}
	laptop.Wake() // Run's own round now waits for that one
	time.Sleep(200 * time.Millisecond)
	close(stop)
	select {
	case <-ran:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return while the round after the join waited for the relay")
	}
}

// Codes asked for at once (a double click) replace each other: once the code on screen is
// taken off, none of them works any more.
func TestCodesShownAtOnceAreAllWithdrawn(t *testing.T) {
	ctx := context.Background()
	_, url := startRelay(t)
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	laptop := newDevice(t, url, "Laptop", setUpLaptop(), c)
	phone := newDevice(t, url, "Phone", newLocal(false, nil), c)
	phone.opts.PollEvery = time.Minute
	for round := 0; round < 10; round++ {
		var wg sync.WaitGroup
		codes := make([]string, 8)
		for i := range codes {
			wg.Add(1)
			go func() {
				defer wg.Done()
				p, err := phone.Request(ctx)
				if err != nil {
					t.Error(err)
					return
				}
				codes[i] = p.Code
			}()
		}
		wg.Wait()
		phone.CancelPairing()
		for _, code := range codes {
			n, _ := NormalizeCode(code)
			if _, err := laptop.takeSlot(ctx, pairingFor(n), "a"); err == nil {
				t.Fatalf("round %d: code %s still works after the code on screen was taken off", round, code)
			}
		}
	}
}
