package devsync

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// asleep moves the engine's wall clock on as a sleep does: the wall clock goes on, Go's timers
// do not.
func asleep(e *Engine) *atomic.Int64 {
	var slept atomic.Int64
	e.wall = func() time.Time { return time.Now().Round(0).Add(time.Duration(slept.Load())) }
	return &slept
}

// A code shown while the device sleeps (a phone's screen off, a laptop's lid closed) is gone from
// the relay after ten minutes by the wall clock: the screen must not show it as waiting on.
func TestCodeShownDuringASleepExpires(t *testing.T) {
	ctx := context.Background()
	_, url := startRelay(t)
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	laptop := newDevice(t, url, "Laptop", setUpLaptop(), c)
	laptop.opts.CodeLifetime = 10 * time.Minute
	slept := asleep(laptop.Engine)
	for _, mode := range []string{"offer", "request"} {
		slept.Store(0)
		var err error
		if mode == "offer" {
			_, err = laptop.Offer(ctx)
		} else {
			_, err = laptop.Request(ctx)
		}
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
		if p := laptop.Status().Pairing; p == nil || p.State != "waiting" {
			t.Fatal(mode, p)
		}
		slept.Store(int64(11 * time.Minute))
		waitState(t, laptop.Engine, "expired")
	}
}

// ... but an answer that came before the relay let go of the code is still taken after the sleep.
func TestAnswerThatCameDuringASleepIsTaken(t *testing.T) {
	ctx := context.Background()
	_, url := startRelay(t)
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	laptop := newDevice(t, url, "Laptop", setUpLaptop(), c)
	phone := newDevice(t, url, "Phone", newLocal(false, nil), c)
	slept := asleep(phone.Engine)
	phone.opts.CodeLifetime = 10 * time.Minute
	phone.opts.PollEvery = 500 * time.Millisecond
	p, err := phone.Request(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := laptop.Enter(ctx, p.Code); err != nil {
		t.Fatal(err)
	}
	slept.Store(int64(11 * time.Minute))
	waitState(t, phone.Engine, "done")
	if phone.local.get(KeyPassword) != `"secret"` {
		t.Fatal(phone.local.get(KeyPassword))
	}
}
