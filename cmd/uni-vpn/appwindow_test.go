package main

import (
	"log/slog"
	"testing"
	"time"
)

func TestAppWindowOfAnEarlierVersionIsSwitchedOnce(t *testing.T) {
	now := time.Unix(0, 0)
	calls := make(chan int, 10)
	a := &appWindows{restart: func(port int) bool { calls <- port; return true },
		log: slog.New(slog.DiscardHandler), now: func() time.Time { return now }}
	count := func() int {
		time.Sleep(20 * time.Millisecond)
		return len(calls)
	}
	a.polled(1081, true)
	if count() != 0 {
		t.Fatal("a window that restarts itself was switched")
	}
	a.polled(1081, false)
	a.polled(1081, false)
	if count() != 1 || <-calls != 1081 {
		t.Fatal("not switched exactly once")
	}
	// The new window asked: an old one started later is switched again, though not at once.
	a.polled(1081, true)
	a.polled(1081, false)
	if count() != 0 {
		t.Fatal("switched again within a minute")
	}
	now = now.Add(2 * time.Minute)
	a.polled(1081, true)
	a.polled(1081, false)
	if count() != 1 {
		t.Fatal("an old window started later stays")
	}
}
