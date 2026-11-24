//go:build unix

package guard

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// The accounts with a Uni VPN service: each home folder owned by an account (not root) whose
// LaunchAgent is that account's, with the port from its own config.toml.
func TestServicesInTheHomeFolders(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to give the files other owners")
	}
	home := t.TempDir()
	user := func(name string, uid int, plistOwner int, config string) string {
		dir := filepath.Join(home, name)
		agents := filepath.Join(dir, "Library", "LaunchAgents")
		if err := os.MkdirAll(agents, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(dir, uid, 20); err != nil {
			t.Fatal(err)
		}
		if plistOwner >= 0 {
			plist := filepath.Join(agents, ServicePlist)
			if err := os.WriteFile(plist, []byte("<plist/>"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chown(plist, plistOwner, 20); err != nil {
				t.Fatal(err)
			}
		}
		if config != "" {
			cfg := filepath.Join(dir, ".config", "uni-vpn")
			if err := os.MkdirAll(cfg, 0o755); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(cfg, "config.toml")
			if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chown(path, uid, 20); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	user("ann", 501, 501, "http_port = 2081\n")
	user("ben", 502, 502, "")                   // no config: the default port
	user("cem", 503, 504, "http_port = 3081\n") // the service is someone else's
	user("dan", 505, -1, "http_port = 4081\n")  // no service
	user("Shared", 0, 0, "")                    // root's
	eve := user("eve", 506, 506, "")
	// A config.toml that is a link (to a file of root's) is not read.
	secret := filepath.Join(home, "secret.toml")
	if err := os.WriteFile(secret, []byte("http_port = 5081\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(eve, ".config", "uni-vpn"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(eve, ".config", "uni-vpn", "config.toml")); err != nil {
		t.Fatal(err)
	}
	// A home folder that is a link is not one.
	if err := os.Symlink(filepath.Join(home, "ann"), filepath.Join(home, "fay")); err != nil {
		t.Fatal(err)
	}
	want := map[uint32]int{501: 2081, 502: DefaultPort, 506: DefaultPort}
	if got := servicesIn(home); !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
}
