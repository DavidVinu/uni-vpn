//go:build !windows

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// A port of tests/test_removal.py.

// installerCopy is an installer folder with the program inside, and a user copy pointing at
// it. goNative: the Go core alone, without the Python program.
func installerCopy(t *testing.T, goNative bool) (copyDir, marker string) {
	root := t.TempDir()
	installer := filepath.Join(root, "installer")
	copyDir = filepath.Join(root, "copy")
	marker = filepath.Join(installer, "app", "uni_vpn", "__init__.py")
	if goNative {
		marker = filepath.Join(installer, "app", "bin", "uni-vpn-core")
	} else {
		os.MkdirAll(filepath.Join(copyDir, "uni_vpn"), 0o755)
		os.WriteFile(filepath.Join(copyDir, "uni_vpn", "__init__.py"), nil, 0o644)
	}
	os.MkdirAll(filepath.Dir(marker), 0o755)
	os.WriteFile(marker, nil, 0o755)
	os.MkdirAll(copyDir, 0o755)
	os.WriteFile(filepath.Join(copyDir, PackageMarker), []byte(installer+"\n"), 0o644)
	return copyDir, marker
}

func TestPackageGoneOnlyForACopyFromAnInstaller(t *testing.T) {
	if PackageGone(t.TempDir()) {
		t.Fatal("a git checkout or get.sh copy is never gone")
	}
	for _, goNative := range []bool{false, true} {
		copyDir, marker := installerCopy(t, goNative)
		if PackageGone(copyDir) {
			t.Fatal(goNative)
		}
		os.Remove(marker)
		if !PackageGone(copyDir) {
			t.Fatal(goNative)
		}
	}
	// A Go-native copy does not look for the Python program in the package.
	copyDir, _ := installerCopy(t, true)
	os.Remove(filepath.Join(PackageDir(copyDir), "app", "uni_vpn", "__init__.py"))
	if PackageGone(copyDir) {
		t.Fatal("Go-native copy looked for the Python program")
	}
	// A copy of the Python program after the package was replaced by one of the Go core alone.
	copyDir, marker := installerCopy(t, false)
	os.Remove(marker)
	core := filepath.Join(PackageDir(copyDir), "app", "bin", "uni-vpn-core")
	os.MkdirAll(filepath.Dir(core), 0o755)
	os.WriteFile(core, nil, 0o755)
	if PackageGone(copyDir) {
		t.Fatal("a package of the Go core counted as removed")
	}
}

func TestRemovedAppUninstallsAfterTwoChecks(t *testing.T) {
	h := newHarness(t)
	copyDir, marker := installerCopy(t, true)
	var removed atomic.Int32
	h.start(func(o *Options) {
		o.PackageWatch = PackageWatch{Root: copyDir, Check: 50 * time.Millisecond,
			RemoveSelf: func() int { removed.Add(1); return 0 }}
	})
	time.Sleep(200 * time.Millisecond)
	if removed.Load() != 0 {
		t.Fatal("removed while the package is there")
	}
	os.Remove(marker)
	select {
	case <-h.done:
	case <-time.After(3 * time.Second):
		t.Fatal("daemon did not stop")
	}
	if removed.Load() != 1 || h.d.RestartRequested {
		t.Fatal(removed.Load())
	}
}

func TestANewVersionBeingInstalledIsNotARemoval(t *testing.T) {
	h := newHarness(t)
	copyDir, marker := installerCopy(t, false)
	var removed atomic.Int32
	h.start(func(o *Options) {
		o.PackageWatch = PackageWatch{Root: copyDir, Check: 300 * time.Millisecond,
			RemoveSelf: func() int { removed.Add(1); return 0 }}
	})
	os.Remove(marker)
	time.Sleep(100 * time.Millisecond) // shorter than one check interval: at most one check sees it
	os.WriteFile(marker, nil, 0o644)
	time.Sleep(800 * time.Millisecond)
	select {
	case <-h.done:
		t.Fatal("daemon stopped")
	default:
	}
	if removed.Load() != 0 {
		t.Fatal(removed.Load())
	}
}

// The first update check came at the same moment as the removal (both 10 minutes after the
// start): the update put the program back or restarted it halfway through the removal.
func TestNoUpdateForARemovedApp(t *testing.T) {
	h := newHarness(t)
	copyDir, marker := installerCopy(t, true)
	os.Remove(marker)
	fake := &fakeUpdater{commit: commitB}
	h.start(func(o *Options) {
		o.Updater = fake
		o.PackageWatch = PackageWatch{Root: copyDir, Check: time.Hour, RemoveSelf: func() int { return 0 }}
	}, timing(50*time.Millisecond, 50*time.Millisecond, 0))
	time.Sleep(300 * time.Millisecond)
	if checks, _, _ := fake.counts(); checks != 0 {
		t.Fatal("checked for an update of a removed app", checks)
	}
	os.WriteFile(marker, nil, 0o755) // a new version installed over it
	h.waitDone()
	if _, applied, _ := fake.counts(); applied != 1 {
		t.Fatal(applied)
	}
}

func TestAnUpdateWaitingDuringTheRemovalIsDropped(t *testing.T) {
	h := newHarness(t)
	fake := &fakeUpdater{commit: commitB}
	d := h.start(withUpdater(fake), timing(time.Hour, time.Hour, 0))
	if _, err := fake.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	d.removing = true
	d.mu.Unlock()
	if d.installWhenIdle(commitB) {
		t.Fatal("installed during the removal")
	}
	if _, applied, discarded := fake.counts(); applied != 0 || discarded != 1 {
		t.Fatal(applied, discarded)
	}
}

func TestRepairRunsSetupWithoutTheInstallerScripts(t *testing.T) {
	// The test binary's folder has no install.sh next to it.
	if fileExists(filepath.Join(RepoRoot(), "install.sh")) {
		t.Skip("the installed program on this machine has the installer scripts")
	}
	cmd := RepairCommand()
	exe, _ := os.Executable()
	// --repair: the app window the user clicked Repair in stays open.
	tail := cmd[len(cmd)-4:]
	if tail[0] != exe || tail[1] != "setup" || tail[2] != "--no-browser" || tail[3] != "--repair" {
		t.Fatal(cmd)
	}
}
