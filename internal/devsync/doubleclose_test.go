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

// A request code runs out while the other device is still on its way with the answer, and the
// user takes the code off the screen while the engine closes it: the two closes together still
// leave slot b filled, so the late answer is refused.
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
	desktop.CancelPairing() // the back arrow, "Enter a code instead", a new code
	close(h.release)
	// Both closes are through: the cancel's filler, the expiry's, and the expiry's second one.
	for range 3 {
		select {
		case <-h.done:
		case <-time.After(5 * time.Second):
		}
	}
	k, _ := laptop.ensureGroup(ctx)
	if err := laptop.putSlot(ctx, pairingFor(n), "b", pairMessage{Kind: "root", Root: k.Root}); err == nil {
		t.Fatal("the late answer was accepted after both closes")
	}
}
