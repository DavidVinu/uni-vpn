//go:build !windows

package setup

// moveAside: a running program can be deleted outside Windows.
func moveAside(string) {}
