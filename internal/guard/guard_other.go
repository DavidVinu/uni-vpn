//go:build !(darwin && !ios)

package guard

import (
	"fmt"
	"os"
)

// Run: the guard is for macOS only, where the proxy rule is the computer's.
func Run([]string) int {
	fmt.Fprintln(os.Stderr, "uni-vpn guard: only on macOS")
	return 2
}
