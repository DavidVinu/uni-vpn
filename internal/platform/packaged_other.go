//go:build !windows

package platform

func packaged() bool { return false }
