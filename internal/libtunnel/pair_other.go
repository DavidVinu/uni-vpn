//go:build !unix && !windows

package libtunnel

import (
	"errors"
	"os"
)

// No packet pipe here.
func socketPair() (*os.File, int, error) {
	return nil, -1, errors.New("no packet socketpair on this system")
}

func closeFD(int) {}
