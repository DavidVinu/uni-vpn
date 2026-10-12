package platform

import (
	"path/filepath"
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
