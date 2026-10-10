package daemon

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"runtime/debug"
	"time"
)

// Updater is the self-updater (*updater.Updater); tests replace it.
type Updater interface {
	// Installed is the running program's commit, "" when unknown.
	Installed() string
	// Pending is the commit that is ready to install, "" when there is none.
	Pending() string
	// Check looks for a new version and prepares it; "" when this one is current.
	Check(ctx context.Context) (string, error)
	// Apply installs the pending version.
	Apply() (string, error)
	// Discard forgets the pending version.
	Discard()
}

// idleForUpdateLocked: nothing uses or wants the tunnel, so replacing the program and
// restarting goes unnoticed. mu must be held.
func (d *Daemon) idleForUpdateLocked() bool {
	return d.tun == nil && !d.hasDemand() && !d.loopRunning &&
		d.state != Connecting && d.state != Connected && d.state != Disconnecting
}

// check runs Updater.Check; an update problem must never stop the service.
func (d *Daemon) check(ctx context.Context) (commit string, err error, crashed bool) {
	defer func() {
		if r := recover(); r != nil {
			d.logf(slog.LevelError, "Update check failed\n%v\n%s", r, debug.Stack())
			commit, err, crashed = "", nil, true
		}
	}()
	commit, err = d.opts.Updater.Check(ctx)
	return commit, err, false
}

// autoUpdate is _auto_update: it ends when the daemon stops, or after installing an update,
// with RestartRequested set.
func (d *Daemon) autoUpdate() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-d.stop
		cancel()
	}()
	delay := d.updateFirstCheck
	for {
		select {
		case <-d.stop:
			return
		case <-d.updateNow:
		case <-d.after(delay):
		}
		// Woken by the timer while the switch was flipped: one check covers both.
		select {
		case <-d.updateNow:
		default:
		}
		delay = d.updateInterval + time.Duration(rand.Float64()*float64(d.updateJitter))
		d.mu.Lock()
		enabled := d.cfg.AutoUpdate
		d.mu.Unlock()
		if !enabled {
			continue
		}
		commit, err, crashed := d.check(ctx)
		if ctx.Err() != nil {
			// Stopped while checking.
			d.opts.Updater.Discard()
			return
		}
		if crashed {
			continue
		}
		if err != nil {
			d.logf(slog.LevelWarn, "Update: %s", err)
			continue
		}
		if commit == "" {
			continue
		}
		d.logf(slog.LevelInfo, "Update %s ready, installing once the tunnel is idle", short(commit))
		if !d.installWhenIdle(commit) {
			if ctx.Err() != nil {
				d.opts.Updater.Discard()
				return
			}
			continue
		}
		return
	}
}

// installWhenIdle waits until the tunnel is idle and installs the update. The check and
// the install happen under mu, so no connection can start in between; then the daemon stops
// and the caller starts the new program. False if the update was not installed.
func (d *Daemon) installWhenIdle(commit string) bool {
	for {
		d.mu.Lock()
		if !d.cfg.AutoUpdate {
			d.mu.Unlock()
			d.opts.Updater.Discard()
			return false
		}
		if d.idleForUpdateLocked() {
			break
		}
		tick := seconds(d.cfg.Tick)
		d.mu.Unlock()
		select {
		case <-d.stop:
			return false
		case <-time.After(tick):
		}
	}
	defer d.mu.Unlock()
	if _, err := d.opts.Updater.Apply(); err != nil {
		d.logf(slog.LevelWarn, "Update: %s", err)
		return false
	}
	d.logf(slog.LevelInfo, "Updated to %s, restarting", short(commit))
	d.RestartRequested = true
	d.Stop()
	return true
}

func short(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}
