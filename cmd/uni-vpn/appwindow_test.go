package main

import (
	"log/slog"
	"testing"
	"time"
)

func TestAppWindowOfAnEarlierVersionIsSwitchedOnce(t *testing.T) {
	calls := make(chan int, 10)
	a := &appWindows{restart: func(port int) bool { calls <- port; return true }, log: slog.New(slog.DiscardHandler)}
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
	// An old copy opened later keeps its window.
	a.polled(1081, true)
	a.polled(1081, false)
	if count() != 0 {
		t.Fatal("switched again")
	}
}
