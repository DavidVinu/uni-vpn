package devsync

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

// randomEdits with every device on its own clock (2 h ahead, right, half an hour behind):
// once the network is back they agree on one language, and a website removed on a device that
// had it stays removed, whichever clock is ahead. Without a removal coming after the addition
// it saw, most seeds brought removed websites back.
func TestRandomEditsOnClocksThatDisagree(t *testing.T) {
	for seed := uint64(1); seed <= 8; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) { skewedEdits(t, seed) })
	}
}

func skewedEdits(t *testing.T, seed uint64) {
	_, target := startRelay(t)
	u, _ := url.Parse(target)
	relay := &lossyRelay{next: httputil.NewSingleHostReverseProxy(u)}
	srv := httptest.NewServer(relay)
	t.Cleanup(srv.Close)
	base := time.Unix(1_800_000_000, 0)
	offsets := []time.Duration{2 * time.Hour, 0, -30 * time.Minute}
	devices := []device{newDevice(t, srv.URL, "Laptop", setUpLaptop(), &clock{t: base.Add(offsets[0])})}
	for i, name := range []string{"Desktop", "Phone"} {
		d := newDevice(t, srv.URL, name, newLocal(false, nil), &clock{t: base.Add(offsets[i+1])})
		pair(t, context.Background(), devices[0], d)
		devices = append(devices, d)
	}
	syncAll(t, devices...)
	rng := rand.New(rand.NewPCG(seed, 0))
	relay.mu.Lock()
	relay.rng, relay.odds = rand.New(rand.NewPCG(seed, 1)), 0.15
	relay.mu.Unlock()
	removed := map[string]bool{}
	var added []string
	for step := range 120 {
		d := devices[rng.IntN(len(devices))]
		switch rng.IntN(4) {
		case 0:
			d.local.set(KeyLanguage, fmt.Sprintf("l%d", step))
			d.Notify()
		case 1:
			host := fmt.Sprintf("site%d.example.org", step)
			d.local.setDomains(append(d.local.getDomains(), host)...)
			d.Notify()
			added = append(added, host)
		case 2:
			var listed []string
			for _, host := range d.local.getDomains() {
				if strings.HasPrefix(host, "site") && !removed[host] {
					listed = append(listed, host)
				}
			}
			if len(listed) > 0 {
				host := listed[rng.IntN(len(listed))]
				d.local.setDomains(without(d.local.getDomains(), host)...)
				d.Notify()
				removed[host] = true
			}
		default:
			d.Sync(context.Background())
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
	lang := devices[0].local.get(KeyLanguage)
	for _, d := range devices {
		got := slices.DeleteFunc(d.local.getDomains(), func(h string) bool { return !strings.HasPrefix(h, "site") })
		slices.Sort(got)
		if d.local.get(KeyLanguage) != lang || !slices.Equal(got, want) {
			t.Fatalf("%s: language %s (laptop %s); websites %v, want %v", d.opts.Name, d.local.get(KeyLanguage), lang, got, want)
		}
	}
}
