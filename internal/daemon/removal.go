package daemon

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/DavidVinu/uni-vpn/internal/platform"
)

// The app was moved to the Trash or its package removed: uninstall. It mirrors
// uni_vpn/removal.py and Daemon._watch_package. Dragging "Uni VPN" to the Trash (macOS) or
// removing the package in the App Center (Linux) runs nothing of ours, and each user's service
// would keep running from the user's own copy. So the service watches the installer's folder
// and, once it is gone, restores the proxy setting, deletes its files and ends itself.

// PackageMarker is the file in the user's copy of the program that names the installer
// folder it was copied from (packaging/uni-vpn-open).
const PackageMarker = ".package"

// DefaultRemoveSelf is what the watcher runs once the package is gone (setup.Remove); the
// command line sets it, so that this package does not depend on setup.
var DefaultRemoveSelf func() int

// PackageWatch configures the watcher; the zero value watches RepoRoot every 5 minutes and
// runs DefaultRemoveSelf.
type PackageWatch struct {
	Root       string
	Check      time.Duration
	RemoveSelf func() int
}

// PackageDir is the installer folder root was copied from, "" for a git checkout or a copy
// made by get.sh.
func PackageDir(root string) string {
	data, err := os.ReadFile(filepath.Join(root, PackageMarker))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func isFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
}

// PackageGone is true when root came from an installer whose files are no longer there. A
// copy of the Python program looks for the package's Python program; a Go-native copy (no
// uni_vpn/__init__.py of its own) for the package's Go core.
func PackageGone(root string) bool {
	pkg := PackageDir(root)
	if pkg == "" {
		return false
	}
	marker := filepath.Join(pkg, "app", "uni_vpn", "__init__.py")
	if !isFile(filepath.Join(root, "uni_vpn", "__init__.py")) {
		marker = filepath.Join(pkg, "app", "bin", "uni-vpn-core")
	}
	return !isFile(marker)
}

// watchPackage uninstalls once the package is gone on two checks in a row, so that installing
// a new version over it does not count.
func (d *Daemon) watchPackage(w PackageWatch) {
	if w.Root == "" {
		w.Root = RepoRoot()
	}
	if w.Check <= 0 {
		w.Check = 5 * time.Minute
	}
	if w.RemoveSelf == nil {
		w.RemoveSelf = DefaultRemoveSelf
	}
	if platform.IsWindows || w.RemoveSelf == nil || PackageDir(w.Root) == "" {
		return
	}
	missing := 0
	for {
		select {
		case <-d.stop:
			return
		case <-time.After(w.Check):
		}
		if PackageGone(w.Root) {
			missing++
		} else {
			missing = 0
		}
		if missing >= 2 {
			d.logf(slog.LevelInfo, "The app was removed, uninstalling")
			d.RequestDisconnect()
			w.RemoveSelf()
			d.Stop()
			return
		}
	}
}
