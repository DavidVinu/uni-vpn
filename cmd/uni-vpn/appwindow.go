package main

import (
	"log/slog"
	"sync"
	"time"
)

// appWindows switches an app window of an earlier version, which cannot restart itself after
// an update, to the one the updates installed (desktop.RestartApp). It tries once, and again
// only after an app window that restarts itself has asked: a switch that brings no new window
// is not repeated.
type appWindows struct {
	restart func(port int) bool
	log     *slog.Logger
	now     func() time.Time

	mu    sync.Mutex
	tried bool
	last  time.Time
}

func (a *appWindows) polled(port int, restartsItself bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if restartsItself {
		a.tried = false
		return
	}
	if a.tried || (!a.last.IsZero() && a.now().Sub(a.last) < time.Minute) {
		return
	}
	a.tried, a.last = true, a.now()
	go func() {
		if a.restart(port) {
			a.log.Info("App window switched to the updated version")
		}
	}()
}
