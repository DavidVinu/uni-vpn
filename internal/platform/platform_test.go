package platform

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestIsBundled(t *testing.T) {
	pkg := PackageDir()
	if pkg == "" {
		if IsBundled(`C:\Program Files\uni-vpn\openconnect\openconnect.exe`) {
			t.Fatal("nothing is bundled without a package folder")
		}
		return
	}
	if !IsBundled(filepath.Join(pkg, "arm64", "bin", "openconnect")) {
		t.Fatal("a program inside the package folder is bundled")
	}
	for _, p := range []string{"/usr/bin/openconnect", pkg + "-other/bin/openconnect", pkg} {
		if IsBundled(p) {
			t.Fatalf("%s is not bundled", p)
		}
	}
}

func TestWindowsLooksAtItsOwnOpenConnectFirst(t *testing.T) {
	if !IsWindows {
		t.Skip("Windows only")
	}
	first := SearchDirs()[0]
	if !strings.HasSuffix(strings.ToLower(first), `\uni-vpn\openconnect`) {
		t.Fatalf("first search folder %q", first)
	}
}

func TestWindowsProgramFilesIgnoresUserVariables(t *testing.T) {
	if !IsWindows {
		t.Skip("Windows only")
	}
	// A user variable overrides the system one for the elevated task (HKCU\Environment).
	fake := t.TempDir()
	for _, name := range []string{"ProgramFiles", "ProgramFiles(x86)", "ProgramW6432"} {
		t.Setenv(name, fake)
	}
	if AdminOnly(filepath.Join(fake, App, "openconnect", "openconnect.exe")) {
		t.Fatal("a folder named by a variable counts as Program Files")
	}
	lower := strings.ToLower(fake)
	for _, p := range append(SearchDirs(), CiscoVPN(), AppInstallDir()) {
		if strings.HasPrefix(strings.ToLower(p), lower) {
			t.Fatalf("%s comes from the variable", p)
		}
	}
	if !AdminOnly(filepath.Join(programFiles()[0], App, "bin", "uni-vpn-core.exe")) {
		t.Fatal("the real Program Files no longer counts")
	}
}

// The updates put openconnect and ocproxy next to the core (packaging/build-core.sh); the Mac
// core runs those, and with the certificate file the package's need (IsBundled).
func TestTheUpdatesOpenConnectComesFirstOnMacOS(t *testing.T) {
	machine := map[string]string{"amd64": "x86_64", "arm64": "arm64"}[runtime.GOARCH]
	if IsWindows || machine == "" {
		t.Skip("macOS processors only")
	}
	defer func(mac bool, root func() string) { IsMacOS, coreRoot = mac, root }(IsMacOS, coreRoot)
	IsMacOS = true
	root := t.TempDir()
	coreRoot = func() string { return root }
	dir := filepath.Join(root, RuntimeDir, machine, "bin")
	for _, got := range BundledBinDirs() {
		if got == dir {
			t.Fatal("a folder that does not exist")
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	openconnect := filepath.Join(dir, "openconnect")
	if err := os.WriteFile(openconnect, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := BundledBinDirs(); len(got) == 0 || got[0] != dir {
		t.Fatalf("bundled folders %q", got)
	}
	if got := FindBinary("openconnect", ""); got != openconnect {
		t.Fatalf("found %q", got)
	}
	if got := FindBinary("ocproxy", ""); got == filepath.Join(dir, "ocproxy") {
		t.Fatal("a program that is not there")
	}
	if !IsBundled(openconnect) {
		t.Fatal("the updates' openconnect is bundled")
	}
	for _, p := range []string{filepath.Join(root, "bin", "uni-vpn-core"), filepath.Join(root, RuntimeDir+"-other", "openconnect")} {
		if IsBundled(p) {
			t.Fatalf("%s is not bundled", p)
		}
	}
	IsMacOS = false
	if got := BundledBinDirs(); len(got) != 0 {
		t.Fatalf("bundled folders off macOS: %q", got)
	}
}
