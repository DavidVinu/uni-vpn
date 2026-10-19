//go:build linux || darwin

package daemon

import (
	"math"
	"testing"
	"time"
)

func TestSleepClockMovesWithTime(t *testing.T) {
	first, err := sleepClock()
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	time.Sleep(200 * time.Millisecond)
	now, err := sleepClock()
	if err != nil {
		t.Fatal(err)
	}
	if moved, want := now-first, time.Since(start).Seconds(); math.Abs(moved-want) > 0.05 {
		t.Fatal(moved, want)
	}
	// The phone's clock starts where the others do.
	mono := monoClock(true)
	a := mono()
	time.Sleep(20 * time.Millisecond)
	if b := mono(); a < 1e6 || a > 1e6+1 || b <= a {
		t.Fatal(a, b)
	}
}
