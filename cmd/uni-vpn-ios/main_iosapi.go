//go:build iosapi && !ios

package main

// The tag iosapi builds core.go on Linux for its tests, without cgo exports.
func main() {}
