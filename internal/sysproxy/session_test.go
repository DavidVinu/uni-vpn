package sysproxy

import (
	"os"
	"path/filepath"
	"testing"
)

// Chromium's choice (base::nix::GetDesktopEnvironment, ProxyConfigServiceLinux), checked with
// the real Chromium: GNOME, Cinnamon, Pantheon, Budgie and MATE (by DESKTOP_SESSION) fetch
// the rule from GNOME's setting; Xfce, LXQt and an unknown desktop do not.
func TestChromeReadsSettings(t *testing.T) {
	for _, c := range []struct {
		env  map[string]string
		want bool
	}{
		{map[string]string{"XDG_CURRENT_DESKTOP": "GNOME"}, true},
		{map[string]string{"XDG_CURRENT_DESKTOP": "ubuntu:GNOME"}, true},
		{map[string]string{"XDG_CURRENT_DESKTOP": "Budgie:GNOME"}, true},
		{map[string]string{"XDG_CURRENT_DESKTOP": "X-Cinnamon"}, true},
		{map[string]string{"XDG_CURRENT_DESKTOP": "Pantheon"}, true},
		{map[string]string{"XDG_CURRENT_DESKTOP": "Unity"}, true},
		{map[string]string{"XDG_CURRENT_DESKTOP": "KDE"}, true},
		{map[string]string{"XDG_CURRENT_DESKTOP": "MATE", "DESKTOP_SESSION": "mate"}, true},
		{map[string]string{"XDG_CURRENT_DESKTOP": "XFCE", "DESKTOP_SESSION": "gnome"}, false},
		{map[string]string{"XDG_CURRENT_DESKTOP": "XFCE"}, false},
		{map[string]string{"XDG_CURRENT_DESKTOP": "LXQt", "DESKTOP_SESSION": "lxqt"}, false},
		{map[string]string{"XDG_CURRENT_DESKTOP": "MATE"}, false},
		{map[string]string{"DESKTOP_SESSION": "xubuntu"}, false},
		{map[string]string{"DESKTOP_SESSION": "xfce"}, false},
		{map[string]string{"XDG_CURRENT_DESKTOP": "sway"}, false},
		{map[string]string{}, false},
		{map[string]string{"GNOME_DESKTOP_SESSION_ID": "this-is-deprecated"}, true},
		{map[string]string{"KDE_FULL_SESSION": "true"}, true},
	} {
		if got := ChromeReadsSettings(func(k string) string { return c.env[k] }); got != c.want {
			t.Errorf("%v: %v", c.env, got)
		}
	}
}

// linux is a GNOME system with its home folder at home.
func linux(r *runner, home string) *System {
	return system("linux", r.run, map[string]string{"XDG_CURRENT_DESKTOP": "XFCE", "HOME": home})
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

const lines1081 = "# >>> uni-vpn >>>\n" +
	"# Chrome, Edge and Brave on Xfce, LXQt and other desktops read the proxy rule from here.\n" +
	"export auto_proxy=http://127.0.0.1:1081/proxy.pac\n" +
	"# <<< uni-vpn <<<\n"

// Chrome on Xfce gets the rule at the next login; uninstalling leaves the files as they were.
func TestLinuxInstallWritesTheSessionFilesAndUninstallTakesThemOut(t *testing.T) {
	home := t.TempDir()
	profile := "# ~/.profile\nexport PATH=\"$HOME/bin:$PATH\""
	if err := os.WriteFile(filepath.Join(home, ".profile"), []byte(profile), 0o644); err != nil {
		t.Fatal(err)
	}
	backup := tempBackup(t, "backup.json")
	s := linux(&runner{answers: gsettings("none", "")}, home)
	if result, err := s.Install(1081, backup); err != nil || result != "ok" {
		t.Fatal(result, err)
	}
	if got := read(t, filepath.Join(home, ".profile")); got != profile+"\n"+lines1081 {
		t.Fatalf("%q", got)
	}
	if got := read(t, filepath.Join(home, ".xprofile")); got != lines1081 {
		t.Fatalf("%q", got)
	}
	if st, _ := os.Stat(filepath.Join(home, ".profile")); st.Mode().Perm() != 0o644 {
		t.Fatal(st.Mode())
	}
	// Again (repair): the same lines once.
	if result, _ := s.Install(1081, backup); result != "ok" || read(t, filepath.Join(home, ".xprofile")) != lines1081 {
		t.Fatal(result, read(t, filepath.Join(home, ".xprofile")))
	}
	if result, err := s.Uninstall(backup); err != nil || result != "restored" {
		t.Fatal(result, err)
	}
	if got := read(t, filepath.Join(home, ".profile")); got != profile+"\n" {
		t.Fatalf("%q", got)
	}
	if exists(filepath.Join(home, ".xprofile")) {
		t.Fatal(".xprofile left")
	}
}

func TestLinuxUninstallWithoutBackupTakesTheSessionLinesOut(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".xprofile"), []byte(lines1081), 0o600); err != nil {
		t.Fatal(err)
	}
	if result, _ := linux(&runner{}, home).Uninstall(tempBackup(t, "none.json")); result != "unset" ||
		exists(filepath.Join(home, ".xprofile")) {
		t.Fatal(result)
	}
}

// Lines of another port (an earlier config) are replaced where they are; the user's lines
// around them stay.
func TestSessionLinesAreReplacedWhereTheyAre(t *testing.T) {
	home := t.TempDir()
	old := "# >>> uni-vpn >>>\nexport auto_proxy=http://127.0.0.1:2081/proxy.pac\n# <<< uni-vpn <<<\n"
	path := filepath.Join(home, ".profile")
	if err := os.WriteFile(path, []byte("a\n"+old+"b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if result, _ := linux(&runner{answers: gsettings("none", "")}, home).Install(1081, tempBackup(t, "b.json")); result != "ok" {
		t.Fatal(result)
	}
	if got := read(t, path); got != "a\n"+lines1081+"b\n" {
		t.Fatalf("%q", got)
	}
}

// Dotfiles kept in a repository: the link stays, its target gets the lines.
func TestSessionFileThatIsALinkStaysALink(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, "dotfiles", "xprofile")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("xset b off\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, ".xprofile")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	linux(&runner{answers: gsettings("none", "")}, home).Install(1081, tempBackup(t, "b.json"))
	if st, err := os.Lstat(link); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatal(st, err)
	}
	if got := read(t, target); got != "xset b off\n"+lines1081 {
		t.Fatalf("%q", got)
	}
}

// Lines whose end someone removed by hand are left alone: taking out "up to the end" could
// take the user's own lines with them.
func TestSessionLinesWithoutTheirEndAreLeftAlone(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".profile")
	text := "# >>> uni-vpn >>>\nexport auto_proxy=http://127.0.0.1:1081/proxy.pac\nexport EDITOR=vi\n"
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	s := linux(&runner{answers: gsettings("none", "")}, home)
	s.Install(1081, tempBackup(t, "b.json"))
	s.Uninstall(tempBackup(t, "b.json"))
	if got := read(t, path); got != text {
		t.Fatalf("%q", got)
	}
}

// Another account on the status port: from the next login Chrome on Xfce does not take the
// rule from it. Install (the port is ours again) puts the lines back.
func TestWithdrawTakesTheSessionLinesOut(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".xprofile"), []byte(lines1081), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &runner{answers: gsettings("auto", "http://127.0.0.1:1081/proxy.pac?v=7")}
	if !linux(r, home).Withdraw(1081) || exists(filepath.Join(home, ".xprofile")) {
		t.Fatal(r.calls)
	}
	// Only the lines: GNOME's setting was not ours.
	if err := os.WriteFile(filepath.Join(home, ".xprofile"), []byte(lines1081), 0o600); err != nil {
		t.Fatal(err)
	}
	r = &runner{answers: gsettings("auto", "http://corp/wpad.dat")}
	if !linux(r, home).Withdraw(1081) || r.called("gsettings", "set", Schema, "mode", "none") {
		t.Fatal(r.calls)
	}
	if linux(r, home).Withdraw(1081) {
		t.Fatal("withdrew twice")
	}
}

// Installed by an earlier version: the core adds the lines while the rule is ours, not when
// it is someone else's or switched off.
func TestLinuxCoverAddsTheSessionLinesWhileTheRuleIsOurs(t *testing.T) {
	home := t.TempDir()
	linux(&runner{answers: gsettings("none", "")}, home).Cover(1081, "")
	linux(&runner{answers: gsettings("auto", "http://corp/wpad.dat")}, home).Cover(1081, "")
	if exists(filepath.Join(home, ".profile")) || exists(filepath.Join(home, ".xprofile")) {
		t.Fatal("lines without our rule")
	}
	linux(&runner{answers: gsettings("auto", "http://127.0.0.1:1081/proxy.pac?v=7")}, home).Cover(1081, "")
	if read(t, filepath.Join(home, ".profile")) != lines1081 || read(t, filepath.Join(home, ".xprofile")) != lines1081 {
		t.Fatal(read(t, filepath.Join(home, ".profile")))
	}
}

// macOS and Windows have no session files.
func TestNoSessionFilesOnMacAndWindows(t *testing.T) {
	home := t.TempDir()
	for _, goos := range []string{"darwin", "windows"} {
		s := system(goos, (&runner{}).run, map[string]string{"HOME": home})
		s.WinGet = func() (string, error) { return "", nil }
		s.WinSet = func(string) error { return nil }
		s.Install(1081, tempBackup(t, goos+".json"))
		s.Cover(1081, "")
	}
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Fatal(entries)
	}
}
