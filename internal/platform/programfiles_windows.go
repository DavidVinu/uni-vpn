//go:build windows

package platform

import (
	"slices"

	"golang.org/x/sys/windows"
)

// systemProgramFiles asks Windows, not %ProgramFiles%: for the elevated task a user variable
// (HKCU\Environment) overrides the system one, like %windir% in the SilentCleanup task, and
// would make a folder any program can write to count as Program Files.
func systemProgramFiles() []string {
	var out []string
	for _, id := range []*windows.KNOWNFOLDERID{windows.FOLDERID_ProgramFiles, windows.FOLDERID_ProgramFilesX86,
		windows.FOLDERID_ProgramFilesX64} {
		if p, err := windows.KnownFolderPath(id, windows.KF_FLAG_DEFAULT); err == nil && p != "" && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}
