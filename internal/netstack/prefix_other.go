//go:build !darwin

package netstack

// Elsewhere the fd carries bare IP packets; the values serve the tests of the Apple framing.
const (
	afPrefix = false
	afInet   = 2
	afInet6  = 30
)
