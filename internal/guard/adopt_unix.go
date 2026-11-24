//go:build unix

package guard

import (
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// servicesIn finds the accounts with a Uni VPN service of their own below home (/Users): a home
// folder (not a link) of an account other than root, whose Library/LaunchAgents/ServicePlist is
// that account's. Each one's port is the http_port of its own config.toml (not a link, the
// account's, at most 64 KB), DefaultPort without one.
func servicesIn(home string) map[uint32]int {
	found := map[uint32]int{}
	dirs, err := os.ReadDir(home)
	if err != nil {
		return found
	}
	for _, d := range dirs {
		dir := filepath.Join(home, d.Name())
		owner, ok := ownerOf(dir, true)
		if !ok || owner == 0 {
			continue
		}
		if o, ok := ownerOf(filepath.Join(dir, "Library", "LaunchAgents", ServicePlist), false); !ok || o != owner {
			continue
		}
		found[owner] = DefaultPort
		if data, ok := readOwned(filepath.Join(dir, ".config", "uni-vpn", "config.toml"), owner); ok {
			found[owner] = PortOf(data)
		}
	}
	return found
}

// ownerOf is the account that owns path, a folder (dir) or a file and never a link.
func ownerOf(path string, dir bool) (uint32, bool) {
	st, err := os.Lstat(path)
	if err != nil || st.IsDir() != dir || (!dir && !st.Mode().IsRegular()) {
		return 0, false
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return sys.Uid, true
}

// readOwned reads path when it is a file of owner's, not a link, of at most 64 KB.
func readOwned(path string, owner uint32) ([]byte, bool) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > 64<<10 {
		return nil, false
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); !ok || sys.Uid != owner {
		return nil, false
	}
	data, err := io.ReadAll(io.LimitReader(f, 64<<10))
	return data, err == nil
}
