package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestPythonFallbackOnlyWithThePythonCore(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil || os.Getenv("OS") == "Windows_NT" {
		t.Skip("needs python3 on PATH")
	}
	root := t.TempDir()
	if got := pythonCommand(root, []string{"status"}); got != nil {
		t.Fatalf("no Python core here, got %v", got)
	}
	os.MkdirAll(filepath.Join(root, "uni_vpn"), 0o755)
	os.WriteFile(filepath.Join(root, "uni_vpn", "cli.py"), nil, 0o644)
	got := pythonCommand(root, []string{"status", "--json"})
	if len(got) != 5 || got[1] != "-I" || got[2] != filepath.Join(root, "bin", "uni-vpn") || got[3] != "status" {
		t.Fatalf("%v", got)
	}
}
