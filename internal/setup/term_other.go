//go:build !linux && !darwin && !windows

package setup

func isTerminal() bool                  { return false }
func noEcho() (restore func(), ok bool) { return func() {}, false }
func writable(string) bool              { return true }
