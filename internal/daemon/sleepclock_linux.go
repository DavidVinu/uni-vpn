package daemon

import "golang.org/x/sys/unix"

// sleepClock is seconds on a clock that keeps counting while the machine sleeps (Go's monotonic
// clock here does not). Linux and Android.
func sleepClock() (float64, error) {
	var ts unix.Timespec
	err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &ts)
	return float64(ts.Sec) + float64(ts.Nsec)/1e9, err
}
