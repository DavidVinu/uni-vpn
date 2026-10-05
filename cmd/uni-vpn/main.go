// Command uni-vpn is the Go core of uni-vpn. It serves the same local API as the Python
// service and reads the same config.toml and keyring entries.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "uni-vpn core: not ready yet")
	os.Exit(2)
}
