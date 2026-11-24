package daemon

import (
	"os/exec"
	"slices"
	"strings"
)

// standardMacAccount: this account of a Mac is not an administrator ("admin" is not among its
// groups). Unknown counts as an administrator.
func standardMacAccount() bool {
	out, err := exec.Command("/usr/bin/id", "-Gn").Output()
	if err != nil {
		return false
	}
	return !slices.Contains(strings.Fields(string(out)), "admin")
}
