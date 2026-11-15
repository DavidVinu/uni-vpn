package devsync

import (
	"bytes"
	"context"
	"os"
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
