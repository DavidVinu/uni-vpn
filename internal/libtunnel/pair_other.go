//go:build !unix

package libtunnel

import (
	"errors"
	"os"
)

// Windows runs the openconnect program with Wintun (internal/wintunnel).
func socketPair() (*os.File, int, error) {
	return nil, -1, errors.New("no packet socketpair on this system")
}

func closeFD(int) {}
