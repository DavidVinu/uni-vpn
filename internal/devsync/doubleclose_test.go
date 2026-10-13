package devsync

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// holdRelay holds the first filler put into a slot until released, and counts the fillers put.
type holdRelay struct {
	next    http.Handler
	mu      sync.Mutex
	puts    int
	arrived chan struct{}
	release chan struct{}
	done    chan struct{} // one value per filler put that got its answer
}

func (h *holdRelay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	filler := false
	if r.Method == "PUT" && strings.HasPrefix(r.URL.Path, "/v1/pair/") {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		if filler = string(body) == "closed"; filler {
			h.mu.Lock()
			h.puts++
			first := h.puts == 1
			h.mu.Unlock()
			if first {
				close(h.arrived)
				<-h.release
			}
		}
	}
	h.next.ServeHTTP(w, r)
	if filler {
		h.done <- struct{}{}
	}
}

func (h *holdRelay) fillers() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.puts
}

// A request code runs out while the other device is still on its way with the answer, and the
// user takes the code off the screen while the engine closes it. Only one close runs (the
// cancel waits for it): two read each other's filler away, and an answer landing in between
// stays in slot b with the group's key.
func TestTwoClosesOfARequestCodeLeaveNoKey(t *testing.T) {
	ctx := context.Background()
	_, target := startRelay(t)
	u, _ := url.Parse(target)
	h := &holdRelay{next: httputil.NewSingleHostReverseProxy(u), arrived: make(chan struct{}), release: make(chan struct{}),
		done: make(chan struct{}, 8)}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	laptop := newDevice(t, srv.URL, "Laptop", setUpLaptop(), c)
	desktop := newDevice(t, srv.URL, "Desktop", newLocal(false, nil), c)
	desktop.opts.PollEvery = time.Minute
	desktop.opts.CodeLifetime = 300 * time.Millisecond
	p, err := desktop.Request(ctx)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := NormalizeCode(p.Code)
	// The laptop read the code just before it ran out and is busy with its group.
	if _, err := laptop.takeSlot(ctx, pairingFor(n), "a"); err != nil {
		t.Fatal(err)
	}
	<-h.arrived // the expiry's close is on its way
	if st := desktop.Status().Pairing.State; st != "waiting" {
		t.Fatalf("state %s", st)
	}
	canceled := make(chan struct{})
	go func() {
		desktop.CancelPairing() // the back arrow, "Enter a code instead", a new code
		close(canceled)
	}()
	select {
	case <-canceled:
		t.Fatal("the cancel returned before the expiry's close was through")
	case <-time.After(300 * time.Millisecond):
	}
	close(h.release)
	<-h.done
	<-canceled
	time.Sleep(200 * time.Millisecond)
	if got := h.fillers(); got != 1 {
		t.Fatalf("%d fillers, want 1: the code was closed twice", got)
	}
	k, _ := laptop.ensureGroup(ctx)
	if err := laptop.putSlot(ctx, pairingFor(n), "b", pairMessage{Kind: "root", Root: k.Root}); err == nil {
		t.Fatal("the late answer was accepted")
	}
}

// dropRelay holds the first filler; once exited is closed, the held one never reaches the
// relay (the process ended with the request still on its way).
type dropRelay struct {
	next    http.Handler
	mu      sync.Mutex
	puts    int
	arrived chan struct{}
	exited  chan struct{}
	drop    bool // set before exited closes: the process ended (else the filler goes on)
}

func (h *dropRelay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == "PUT" && strings.HasPrefix(r.URL.Path, "/v1/pair/") {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		if string(body) == "closed" {
			h.mu.Lock()
			h.puts++
			first := h.puts == 1
			h.mu.Unlock()
			if first {
				close(h.arrived)
				<-h.exited
				h.mu.Lock()
				drop := h.drop
				h.mu.Unlock()
				if drop {
					w.WriteHeader(502)
					return
				}
			}
		}
	}
	h.next.ServeHTTP(w, r)
}

// The service stops (logout, an update) while a request code's expiry closes it: Run's own
// cancel must not return before the code is closed, the process ends right after.
func TestStopDuringTheExpirysCloseLeavesNoKey(t *testing.T) {
	ctx := context.Background()
	_, target := startRelay(t)
	u, _ := url.Parse(target)
	h := &dropRelay{next: httputil.NewSingleHostReverseProxy(u), arrived: make(chan struct{}), exited: make(chan struct{})}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	laptop := newDevice(t, srv.URL, "Laptop", setUpLaptop(), c)
	desktop := newDevice(t, srv.URL, "Desktop", newLocal(false, nil), c)
	desktop.opts.PollEvery = time.Minute
	desktop.opts.CodeLifetime = 300 * time.Millisecond
	stop := make(chan struct{})
	ran := make(chan struct{})
	go func() { desktop.Run(stop); close(ran) }()
	p, err := desktop.Request(ctx)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := NormalizeCode(p.Code)
	// The laptop read the code just before it ran out and is busy with its group.
	if _, err := laptop.takeSlot(ctx, pairingFor(n), "a"); err != nil {
		t.Fatal(err)
	}
	<-h.arrived // the expiry's close is on its way
	close(stop)
	select {
	case <-ran:
		// Run returned: the daemon ends, the expiry's filler with it.
		h.mu.Lock()
		h.drop = true
		h.mu.Unlock()
	case <-time.After(2 * time.Second):
		// Run waits for the close: it goes on.
	}
	close(h.exited)
	<-ran
	k, _ := laptop.ensureGroup(ctx)
	if err := laptop.putSlot(ctx, pairingFor(n), "b", pairMessage{Kind: "root", Root: k.Root}); err == nil {
		t.Fatal("the late answer was accepted: the group's key waits in slot b for whoever saw the code")
	}
}
