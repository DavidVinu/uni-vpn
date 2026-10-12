//go:build !windows

package platform

import "os"

// systemProgramFiles reads the variables; elsewhere only tests of the Windows paths ask.
func systemProgramFiles() []string {
	var out []string
	seen := map[string]bool{}
	for _, name := range []string{"ProgramFiles", "ProgramFiles(x86)", "ProgramW6432"} {
		if v := os.Getenv(name); v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
