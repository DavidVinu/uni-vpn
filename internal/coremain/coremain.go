// Package coremain is "uni-vpn daemon": the lock, the log, config.toml and the daemon until
// it is stopped. The command line (cmd/uni-vpn) and the iPhone app's library
// (cmd/uni-vpn-ios) both run it.
package coremain

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/DavidVinu/uni-vpn/internal/config"
	"github.com/DavidVinu/uni-vpn/internal/daemon"
	"github.com/DavidVinu/uni-vpn/internal/logsetup"
	"github.com/DavidVinu/uni-vpn/internal/platform"
	"github.com/DavidVinu/uni-vpn/internal/winsys"
)

// ErrLocked: another daemon holds the lock.
var ErrLocked = errors.New("uni-vpn daemon is already running")

// ErrRestart: the daemon asked to be started again (after an update); lock and log are
// released.
var ErrRestart = errors.New("uni-vpn daemon asked for a restart")

// LoggedError is a failure that is already in the log file.
type LoggedError struct{ Err error }

func (e *LoggedError) Error() string { return e.Err.Error() }
func (e *LoggedError) Unwrap() error { return e.Err }

// Options are what differs between the command line and the iPhone app.
type Options struct {
	ConfigArg string // --config, "" for the default path
	APIToken  string // every API request must carry it, "" for none
	// LockWait is how long to wait for another daemon to release the lock; 0 gives up at once.
	LockWait time.Duration
	// Extend fills in daemon options of the caller (updater, app window) right before the
	// daemon is made.
	Extend func(log *slog.Logger, o *daemon.Options)
	// Started runs once the daemon serves its ports.
	Started func()
	// Release, when set, receives a function that stops the daemon and frees the lock and
	// the ports at once, while the daemon may still be ending (a tunnel slow to say goodbye).
	// It may be called any time, also after Run returned.
	Release func(now func())
}

// lockPoll is how often a waiting core tries the lock again.
const lockPoll = 100 * time.Millisecond

func lock(wait time.Duration) (*os.File, error) {
	path := platform.LockFile()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		f, err := acquireLock(path)
		if err != nil || f != nil {
			return f, err
		}
		if !time.Now().Before(deadline) {
			return nil, ErrLocked
		}
		time.Sleep(lockPoll)
	}
}

// Run runs the daemon until ctx is done or the daemon ends by itself.
func Run(ctx context.Context, o Options) error {
	lockFile, err := lock(o.LockWait)
	if err != nil {
		return err
	}
	defer lockFile.Close()
	log, tail, err := logsetup.Setup(platform.LogFile(), slog.LevelInfo)
	if err != nil {
		return err
	}
	closeLog := func() {
		if h, ok := log.Handler().(interface{ Close() error }); ok {
			h.Close()
		}
	}
	cfgPath := o.ConfigArg
	if cfgPath == "" {
		cfgPath = config.DefaultPath()
	}
	configError := ""
	cfg, err := config.Load(o.ConfigArg)
	if err != nil {
		var ce *config.ConfigError
		if !errors.As(err, &ce) {
			log.Error(err.Error())
			closeLog()
			return &LoggedError{err}
		}
		fallback := config.Default()
		ports := config.PortsFromBroken(o.ConfigArg)
		if p, ok := ports["socks_port"]; ok {
			fallback.SocksPort = p
		}
		if p, ok := ports["http_port"]; ok {
			fallback.HTTPPort = p
		}
		cfg = &fallback
		configError = err.Error()
		log.Error(configError)
	}
	log.Info(fmt.Sprintf("uni-vpn %s starting (SOCKS %d, status %d)", daemon.Version, cfg.SocksPort, cfg.HTTPPort))
	if platform.IsWindows {
		winsys.KillChildrenWithUs()
		winsys.AllowCtrlCForChildren()
		if !winsys.IsAdmin() {
			log.Warn("Not running elevated: openconnect cannot create the Wintun adapter")
		}
	}
	_, statErr := os.Stat(cfgPath)
	opts := daemon.Options{ConfigError: configError, LogTail: tail.Lines, ConfigPath: cfgPath,
		NeedsSetup: errors.Is(statErr, fs.ErrNotExist), APIToken: o.APIToken}
	if o.Extend != nil {
		o.Extend(log, &opts)
	}
	d := daemon.New(cfg, log, opts)
	stopped := make(chan struct{})
	defer close(stopped)
	stopOnCancel := context.AfterFunc(ctx, d.Stop)
	defer stopOnCancel()
	if o.Release != nil {
		o.Release(func() {
			d.Stop()
			d.Unbind()
			lockFile.Close()
		})
	}
	if o.Started != nil {
		go func() {
			select {
			case <-d.Started():
				o.Started()
			case <-stopped:
			}
		}()
	}
	d.Run()
	log.Info("uni-vpn stopped")
	lockFile.Close()
	closeLog() // a restarted program opens the log file again
	if d.RestartRequested {
		return ErrRestart
	}
	return nil
}
