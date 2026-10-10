package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/DavidVinu/uni-vpn/internal/daemon"
	"github.com/DavidVinu/uni-vpn/internal/platform"
	"github.com/DavidVinu/uni-vpn/internal/service"
	"github.com/DavidVinu/uni-vpn/internal/updater"
	"github.com/DavidVinu/uni-vpn/internal/winsys"
)

func versionLine() string {
	if daemon.Commit == "" {
		return "uni-vpn " + daemon.Version
	}
	return "uni-vpn " + daemon.Version + " " + daemon.Commit
}

// executable is the path the program was started from, taken at the start: an update
// renames or replaces the file, and the restart runs the new one from the same path.
func executable() string {
	exe, err := os.Executable()
	if err != nil {
		return os.Args[0]
	}
	if abs, err := filepath.Abs(exe); err == nil {
		exe = abs
	}
	return exe
}

// coreUpdater is the service's updater; it first removes what the last update moved aside.
func coreUpdater() *updater.Updater {
	u := updater.New(daemon.Commit)
	u.RemoveOld()
	return u
}

// selfUpdater is what "uni-vpn update" needs of *updater.Updater.
type selfUpdater interface {
	Disabled() string
	Check(ctx context.Context) (string, error)
	Apply() (string, error)
}

// update is setup.update of uni_vpn/setup.py for an installation without git: fetch the
// current version, install it and restart the service.
type update struct {
	u       selfUpdater
	root    string
	control func(action string) (int, error)
	out     io.Writer
	// needsAdmin: on Windows the program is in Program Files.
	needsAdmin bool
}

func cmdUpdate(dryRun bool) int {
	u := updater.New(daemon.Commit)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return update{u: u, root: u.Root, control: service.New().Control, out: os.Stdout,
		needsAdmin: platform.IsWindows && !winsys.IsAdmin()}.run(ctx, dryRun)
}

func (c update) run(ctx context.Context, dryRun bool) int {
	if dryRun {
		fmt.Fprintf(c.out, "-> would download the current version in %s and restart the service\n", c.root)
		return 0
	}
	if why := c.u.Disabled(); why != "" {
		fmt.Fprintf(c.out, "Update not possible: %s\n", why)
		return 1
	}
	if c.needsAdmin {
		fmt.Fprintln(c.out, "Update not possible: run it as administrator")
		return 1
	}
	commit, err := c.u.Check(ctx)
	if err == nil && commit != "" {
		_, err = c.u.Apply()
	}
	if err != nil {
		fmt.Fprintf(c.out, "Update failed: %s\n", err)
		return 1
	}
	if commit == "" {
		fmt.Fprintln(c.out, "-> This is the current version")
	} else {
		fmt.Fprintf(c.out, "-> Updated to %s\n", commit[:7])
	}
	rc, err := c.control("restart")
	if err != nil {
		fmt.Fprintln(c.out, err)
		rc = 1
	}
	if rc == 0 {
		fmt.Fprintln(c.out, "Service restarted.")
	} else {
		fmt.Fprintln(c.out, "Restarting the service failed, see: uni-vpn log")
	}
	return rc
}
