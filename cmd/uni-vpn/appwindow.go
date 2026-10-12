package main

import (
	"log/slog"
	"sync"
)

// appWindows switches an app window of an earlier version, which cannot restart itself after
// an update, to the one the updates installed (desktop.RestartApp). Once per start of the
// service: an old copy the user opens later (on macOS the one in /Applications) keeps its
// window.
type appWindows struct {
	restart func(port int) bool
	log     *slog.Logger

	mu    sync.Mutex
	tried bool
}

func (a *appWindows) polled(port int, restartsItself bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if restartsItself || a.tried {
		return
	}
	a.tried = true
	go func() {
		if a.restart(port) {
			a.log.Info("App window switched to the updated version")
		}
	}()
}
