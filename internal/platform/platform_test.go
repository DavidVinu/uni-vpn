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
