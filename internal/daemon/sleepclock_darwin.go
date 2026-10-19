package daemon

import "golang.org/x/sys/unix"

// sleepClock is seconds on a clock that keeps counting while the machine sleeps (Go's monotonic
// clock here does not). macOS and iOS: there CLOCK_MONOTONIC counts the sleep, CLOCK_UPTIME_RAW
// would not.
func sleepClock() (float64, error) {
	var ts unix.Timespec
	err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts)
	return float64(ts.Sec) + float64(ts.Nsec)/1e9, err
}
