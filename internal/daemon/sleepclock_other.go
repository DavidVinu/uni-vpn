//go:build !linux && !darwin

package daemon

import "errors"

// sleepClock: only phones use it (monoClock).
func sleepClock() (float64, error) { return 0, errors.ErrUnsupported }
