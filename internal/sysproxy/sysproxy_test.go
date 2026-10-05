package sysproxy

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

type answer struct {
	prefix string
	code   int
	out    string
}

// runner captures commands and returns prepared answers (the first matching prefix wins).
type runner struct {
	calls   [][]string
	answers []answer
}

func (r *runner) run(args []string, timeout time.Duration) (Result, error) {
	r.calls = append(r.calls, args)
	joined := strings.Join(args, " ")
	for _, a := range r.answers {
		if strings.HasPrefix(joined, a.prefix) {
			return Result{Code: a.code, Stdout: a.out}, nil
		}
	}
	return Result{}, nil
}

func (r *runner) called(args ...string) bool {
	for _, c := range r.calls {
		if reflect.DeepEqual(c, args) {
			return true
		}
	}
	return false
}

func missing(args []string, timeout time.Duration) (Result, error) {
	return Result{}, &os.PathError{Op: "exec", Path: args[0], Err: os.ErrNotExist}
}

func system(goos string, run Runner, env map[string]string) *System {
	return &System{
		GOOS:       goos,
		Run:        run,
		Getenv:     func(k string) string { return env[k] },
		FindBinary: func(string) string { return "" },
		WinGet:     func() (string, error) { return "", errors.New("no registry") },
		WinSet:     func(string) error { return errors.New("no registry") },
		Now:        time.Now,
	}
}

func gnome(run Runner) *System {
	return system("linux", run, map[string]string{"XDG_CURRENT_DESKTOP": "ubuntu:GNOME"})
}

func tempBackup(t *testing.T, name string) string { return filepath.Join(t.TempDir(), name) }

func readBackup(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func gsettings(mode, url string) []answer {
	return []answer{
		{"gsettings get org.gnome.system.proxy mode", 0, "'" + mode + "'\n"},
		{"gsettings get org.gnome.system.proxy autoconfig-url", 0, "'" + url + "'\n"},
	}
}

func TestPacURLWithAndWithoutVersion(t *testing.T) {
	if got := PacURL(1081); got != "http://127.0.0.1:1081/proxy.pac" {
		t.Fatal(got)
	}
	if got := PacURLVersion(1081, 42); got != "http://127.0.0.1:1081/proxy.pac?v=42" {
		t.Fatal(got)
	}
	if !IsOurs("http://127.0.0.1:1081/proxy.pac?v=7", 1081) || IsOurs("http://127.0.0.1:1082/proxy.pac", 1081) ||
		IsOurs("", 1081) {
		t.Fatal("IsOurs")
	}
}

// --- Linux (GNOME) ---

func TestLinuxCurrentReadsGsettings(t *testing.T) {
	r := &runner{answers: gsettings("auto", "http://x/p.pac")}
	got := gnome(r.run).Current()
	want := &Snapshot{GNOME: &GNOME{Mode: "auto", URL: "http://x/p.pac"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
}

func TestLinuxCurrentWithoutGsettingsIsNil(t *testing.T) {
	if got := gnome(missing).Current(); got != nil {
		t.Fatalf("%+v", got)
	}
}

func TestLinuxApplySetsAutoModeAndURL(t *testing.T) {
	r := &runner{}
	if err := gnome(r.run).Apply("http://127.0.0.1:1081/proxy.pac"); err != nil {
		t.Fatal(err)
	}
	if !r.called("gsettings", "set", "org.gnome.system.proxy", "autoconfig-url", "http://127.0.0.1:1081/proxy.pac") ||
		!r.called("gsettings", "set", "org.gnome.system.proxy", "mode", "auto") {
		t.Fatal(r.calls)
	}
}

func TestLinuxRestorePutsPreviousValuesBack(t *testing.T) {
	r := &runner{}
	if err := gnome(r.run).Restore(&Snapshot{GNOME: &GNOME{Mode: "none"}}); err != nil {
		t.Fatal(err)
	}
	if !r.called("gsettings", "set", "org.gnome.system.proxy", "mode", "none") ||
		!r.called("gsettings", "set", "org.gnome.system.proxy", "autoconfig-url", "") {
		t.Fatal(r.calls)
	}
}

func TestLinuxRefreshBumpsURLVersionSoBrowsersRefetch(t *testing.T) {
	r := &runner{answers: gsettings("auto", "http://127.0.0.1:1081/proxy.pac?v=1")}
	s := gnome(r.run)
	s.Now = func() time.Time { return time.Unix(1700000000, 900_000_000) }
	s.Refresh(1081)
	if !r.called("gsettings", "set", "org.gnome.system.proxy", "autoconfig-url", "http://127.0.0.1:1081/proxy.pac?v=1700000000") {
		t.Fatal(r.calls)
	}
}

func TestLinuxRefreshLeavesAForeignSettingAlone(t *testing.T) {
	for _, c := range [][2]string{{"auto", "http://corp/wpad.dat"}, {"manual", "http://127.0.0.1:1081/proxy.pac"}, {"none", ""}} {
		r := &runner{answers: gsettings(c[0], c[1])}
		gnome(r.run).Refresh(1081)
		for _, call := range r.calls {
			if len(call) > 1 && call[0] == "gsettings" && call[1] == "set" {
				t.Fatal(c[0], r.calls)
			}
		}
	}
}

func TestLinuxFailedGsettingsSetIsReported(t *testing.T) {
	backup := tempBackup(t, "proxy-backup.json")
	r := &runner{answers: append(gsettings("none", ""), answer{"gsettings set", 1, ""})}
	s := gnome(r.run)
	if got, err := s.Install(1081, backup); got != "failed" || err != nil {
		t.Fatal(got, err)
	}
	if got, err := s.Uninstall(backup); got != "failed" || err != nil {
		t.Fatal(got, err)
	}
	if !exists(backup) {
		t.Fatal("backup must stay for another try")
	}
}

func TestLinuxKDEBackupIsRestoredOutsideTheKDESession(t *testing.T) {
	// Uninstall over SSH: XDG_CURRENT_DESKTOP is not KDE, kwriteconfig must still run.
	backup := tempBackup(t, "proxy-backup.json")
	os.WriteFile(backup, []byte(`{"kde": {"type": "0", "url": ""}}`), 0o600)
	r := &runner{}
	s := system("linux", r.run, map[string]string{"XDG_CURRENT_DESKTOP": ""})
	s.FindBinary = func(name string) string { return "/usr/bin/" + name }
	if got, err := s.Uninstall(backup); got != "restored" || err != nil {
		t.Fatal(got, err)
	}
	if !r.called("/usr/bin/kwriteconfig6", "--file", "kioslaverc", "--group", "Proxy Settings", "--key", "ProxyType", "0") {
		t.Fatal(r.calls)
	}
	if exists(backup) {
		t.Fatal("backup left")
	}
}

func TestLinuxKDEBackupWithoutKwriteconfigIsKept(t *testing.T) {
	backup := tempBackup(t, "proxy-backup.json")
	os.WriteFile(backup, []byte(`{"kde": {"type": "0", "url": ""}}`), 0o600)
	r := &runner{}
	if got, _ := gnome(r.run).Uninstall(backup); got != "failed" {
		t.Fatal(got)
	}
	if !exists(backup) {
		t.Fatal("backup removed")
	}
}

func TestLinuxInstallBacksUpThenAppliesAndUninstallRestores(t *testing.T) {
	backup := tempBackup(t, "proxy-backup.json")
	r := &runner{answers: gsettings("none", "")}
	if got, err := gnome(r.run).Install(1081, backup); got != "ok" || err != nil {
		t.Fatal(got, err)
	}
	// Byte-identical to Python's json.dumps.
	if got := readBackup(t, backup); got != `{"mode": "none", "url": ""}` {
		t.Fatal(got)
	}
	if !r.called("gsettings", "set", "org.gnome.system.proxy", "mode", "auto") {
		t.Fatal(r.calls)
	}
	r2 := &runner{}
	if got, err := gnome(r2.run).Uninstall(backup); got != "restored" || err != nil {
		t.Fatal(got, err)
	}
	if !r2.called("gsettings", "set", "org.gnome.system.proxy", "mode", "none") {
		t.Fatal(r2.calls)
	}
	if exists(backup) {
		t.Fatal("backup left")
	}
}

func TestLinuxInstallKeepsExistingBackupWhenRerun(t *testing.T) {
	// On the second install.sh our own entry is active; the original state stays backed up.
	backup := tempBackup(t, "proxy-backup.json")
	os.WriteFile(backup, []byte(`{"mode": "none", "url": ""}`), 0o600)
	r := &runner{answers: gsettings("auto", "http://127.0.0.1:1081/proxy.pac?v=1")}
	gnome(r.run).Install(1081, backup)
	if got := readBackup(t, backup); got != `{"mode": "none", "url": ""}` {
		t.Fatal(got)
	}
}

func TestLinuxInstallReportsForeignProxy(t *testing.T) {
	backup := tempBackup(t, "proxy-backup.json")
	r := &runner{answers: gsettings("manual", "")}
	if got, _ := gnome(r.run).Install(1081, backup); got != "replaced" {
		t.Fatal(got)
	}
	if got := readBackup(t, backup); got != `{"mode": "manual", "url": ""}` {
		t.Fatal(got)
	}
}

func TestLinuxInstallWithoutGsettings(t *testing.T) {
	if got, _ := gnome(missing).Install(1081, tempBackup(t, "b.json")); got != "unavailable" {
		t.Fatal(got)
	}
}

func TestLinuxUninstallWithoutBackupIsNoop(t *testing.T) {
	r := &runner{}
	if got, _ := gnome(r.run).Uninstall(tempBackup(t, "missing.json")); got != "unset" {
		t.Fatal(got)
	}
	if len(r.calls) != 0 {
		t.Fatal(r.calls)
	}
}

func TestLinuxStateForDoctor(t *testing.T) {
	r := &runner{answers: gsettings("auto", "http://127.0.0.1:1081/proxy.pac?v=3")}
	if got := gnome(r.run).State(1081); got != "ok" {
		t.Fatal(got)
	}
	r = &runner{answers: []answer{{"gsettings get org.gnome.system.proxy mode", 0, "'none'\n"}}}
	if got := gnome(r.run).State(1081); got != "unset" {
		t.Fatal(got)
	}
	r = &runner{answers: []answer{{"gsettings get org.gnome.system.proxy mode", 0, "'manual'\n"}}}
	if got := gnome(r.run).State(1081); got != "foreign" {
		t.Fatal(got)
	}
}

// --- macOS ---

func macAnswers() []answer {
	return []answer{
		{"networksetup -listallnetworkservices", 0, "An asterisk (*) denotes that a network service is disabled.\nWi-Fi\n*Thunderbolt Bridge\n"},
		{"networksetup -getautoproxyurl Wi-Fi", 0, "URL: (null)\nEnabled: No\n"},
	}
}

func mac(r *runner) *System { return system("darwin", r.run, nil) }

func TestMacCurrentListsEnabledServices(t *testing.T) {
	got := mac(&runner{answers: macAnswers()}).Current()
	want := &Snapshot{Mac: &Mac{Services: []Service{{Name: "Wi-Fi"}}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got.Mac)
	}
	data, _ := got.MarshalJSON()
	if string(data) != `{"services": {"Wi-Fi": {"url": "", "enabled": false}}}` {
		t.Fatal(string(data))
	}
}

func TestMacApplySetsURLOnEveryService(t *testing.T) {
	r := &runner{answers: macAnswers()}
	if err := mac(r).Apply("http://127.0.0.1:1081/proxy.pac"); err != nil {
		t.Fatal(err)
	}
	if !r.called("networksetup", "-setautoproxyurl", "Wi-Fi", "http://127.0.0.1:1081/proxy.pac") ||
		r.called("networksetup", "-setautoproxyurl", "Thunderbolt Bridge", "http://127.0.0.1:1081/proxy.pac") {
		t.Fatal(r.calls)
	}
}

func TestMacFailedNetworksetupIsReported(t *testing.T) {
	r := &runner{answers: append(macAnswers(), answer{"networksetup -setautoproxyurl", 1, ""})}
	if got, _ := mac(r).Install(1081, tempBackup(t, "b.json")); got != "failed" {
		t.Fatal(got)
	}
}

func TestMacRestoreDisablesWhenPreviouslyOff(t *testing.T) {
	r := &runner{answers: macAnswers()}
	mac(r).Restore(&Snapshot{Mac: &Mac{Services: []Service{{Name: "Wi-Fi"}}}})
	if !r.called("networksetup", "-setautoproxystate", "Wi-Fi", "off") {
		t.Fatal(r.calls)
	}
}

func TestMacRestorePutsOldURLBack(t *testing.T) {
	r := &runner{answers: macAnswers()}
	mac(r).Restore(&Snapshot{Mac: &Mac{Services: []Service{{Name: "Wi-Fi", URL: "http://old/p.pac", Enabled: true}}}})
	if !r.called("networksetup", "-setautoproxyurl", "Wi-Fi", "http://old/p.pac") {
		t.Fatal(r.calls)
	}
}

func TestMacStateOKWhenOurURLIsEnabled(t *testing.T) {
	answers := append([]answer{{"networksetup -getautoproxyurl Wi-Fi", 0, "URL: http://127.0.0.1:1081/proxy.pac\nEnabled: Yes\n"}}, macAnswers()...)
	if got := mac(&runner{answers: answers}).State(1081); got != "ok" {
		t.Fatal(got)
	}
	if got := mac(&runner{answers: macAnswers()}).State(1081); got != "unset" {
		t.Fatal(got)
	}
}

func TestMacRefreshIsNoop(t *testing.T) {
	r := &runner{answers: macAnswers()}
	mac(r).Refresh(1081)
	if len(r.calls) != 0 {
		t.Fatal(r.calls)
	}
}

// --- KDE ---

func kdeAnswers() []answer {
	return []answer{
		{"gsettings get", 1, ""},
		{"/usr/bin/kreadconfig6 --file kioslaverc --group Proxy Settings --key ProxyType", 0, "0\n"},
		{"/usr/bin/kreadconfig6 --file kioslaverc --group Proxy Settings --key Proxy Config Script", 0, "\n"},
	}
}

func kde(r *runner) *System {
	s := system("linux", r.run, map[string]string{"XDG_CURRENT_DESKTOP": "KDE"})
	s.FindBinary = func(name string) string { return "/usr/bin/" + name }
	return s
}

func TestKDEInstallWritesKioslavercAndUninstallRestores(t *testing.T) {
	backup := tempBackup(t, "backup.json")
	r := &runner{answers: kdeAnswers()}
	if got, err := kde(r).Install(1081, backup); got != "ok" || err != nil {
		t.Fatal(got, err)
	}
	if got := readBackup(t, backup); got != `{"kde": {"type": "0", "url": ""}}` {
		t.Fatal(got)
	}
	write := []string{"/usr/bin/kwriteconfig6", "--file", "kioslaverc", "--group", "Proxy Settings", "--key"}
	if !r.called(append(write, "ProxyType", "2")...) ||
		!r.called(append(write[:len(write):len(write)], "Proxy Config Script", "http://127.0.0.1:1081/proxy.pac")...) {
		t.Fatal(r.calls)
	}
	r2 := &runner{answers: kdeAnswers()}
	if got, _ := kde(r2).Uninstall(backup); got != "restored" {
		t.Fatal(got)
	}
	if !r2.called(append(write, "ProxyType", "0")...) {
		t.Fatal(r2.calls)
	}
}

func TestKDEStateFollowsKDE(t *testing.T) {
	answers := append([]answer{
		{"/usr/bin/kreadconfig6 --file kioslaverc --group Proxy Settings --key ProxyType", 0, "2\n"},
		{"/usr/bin/kreadconfig6 --file kioslaverc --group Proxy Settings --key Proxy Config Script", 0, "http://127.0.0.1:1081/proxy.pac?v=3\n"},
	}, kdeAnswers()...)
	if got := kde(&runner{answers: answers}).State(1081); got != "ok" {
		t.Fatal(got)
	}
	if got := kde(&runner{answers: kdeAnswers()}).State(1081); got != "unset" {
		t.Fatal(got)
	}
}

func TestKDEWithoutToolsAndGsettingsIsUnavailable(t *testing.T) {
	s := kde(&runner{answers: []answer{{"gsettings get", 1, ""}}})
	s.FindBinary = func(string) string { return "" }
	if got := s.Current(); got != nil {
		t.Fatalf("%+v", got)
	}
}

// --- Windows ---

type registry struct {
	url      string
	notified []string
}

func windowsSystem(reg *registry) *System {
	s := system("windows", (&runner{}).run, nil)
	s.WinGet = func() (string, error) { return reg.url, nil }
	s.WinSet = func(url string) error {
		reg.url = url
		reg.notified = append(reg.notified, url)
		return nil
	}
	return s
}

func TestWindowsInstallSetsAutoconfigURLAndUninstallRemovesIt(t *testing.T) {
	reg := &registry{}
	s := windowsSystem(reg)
	backup := tempBackup(t, "backup.json")
	if got, _ := s.Install(1081, backup); got != "ok" {
		t.Fatal(got)
	}
	if reg.url != "http://127.0.0.1:1081/proxy.pac" || s.State(1081) != "ok" {
		t.Fatal(reg.url)
	}
	if got := readBackup(t, backup); got != `{"windows": {"url": ""}}` {
		t.Fatal(got)
	}
	if got, _ := s.Uninstall(backup); got != "restored" {
		t.Fatal(got)
	}
	if reg.url != "" || s.State(1081) != "unset" {
		t.Fatal(reg.url)
	}
}

func TestWindowsForeignPACIsReplacedAndRestored(t *testing.T) {
	reg := &registry{url: "http://corp/wpad.dat"}
	s := windowsSystem(reg)
	backup := tempBackup(t, "backup.json")
	if got := s.State(1081); got != "foreign" {
		t.Fatal(got)
	}
	if got, _ := s.Install(1081, backup); got != "replaced" {
		t.Fatal(got)
	}
	if got, _ := s.Uninstall(backup); got != "restored" {
		t.Fatal(got)
	}
	if reg.url != "http://corp/wpad.dat" {
		t.Fatal(reg.url)
	}
}

func TestWindowsRefreshBumpsVersionOnlyWhenOurs(t *testing.T) {
	reg := &registry{}
	s := windowsSystem(reg)
	s.Refresh(1081)
	if len(reg.notified) != 0 {
		t.Fatal(reg.notified)
	}
	reg.url = "http://127.0.0.1:1081/proxy.pac"
	s.Refresh(1081)
	if !strings.HasPrefix(reg.url, "http://127.0.0.1:1081/proxy.pac?v=") {
		t.Fatal(reg.url)
	}
}

// --- backup format shared with Python ---

func TestBackupJSONMatchesPython(t *testing.T) {
	for _, c := range []struct {
		snap Snapshot
		want string
	}{
		{Snapshot{GNOME: &GNOME{Mode: "auto", URL: "http://x/p.pac"}, KDE: &KDE{Type: "2", URL: "http://y/ü.pac"}},
			`{"mode": "auto", "url": "http://x/p.pac", "kde": {"type": "2", "url": "http://y/\u00fc.pac"}}`},
		{Snapshot{Mac: &Mac{Services: []Service{{Name: "Wi-Fi", URL: "http://a<b>&\"c\"", Enabled: true}, {Name: "USB 10/100 LAN"}}}},
			`{"services": {"Wi-Fi": {"url": "http://a<b>&\"c\"", "enabled": true}, "USB 10/100 LAN": {"url": "", "enabled": false}}}`},
		{Snapshot{Mac: &Mac{Services: []Service{}}}, `{"services": {}}`},
		{Snapshot{Windows: &Windows{URL: "\t\x7f😀"}}, `{"windows": {"url": "\t\u007f\ud83d\ude00"}}`},
	} {
		data, err := c.snap.MarshalJSON()
		if err != nil || string(data) != c.want {
			t.Fatalf("%s\n%s", data, c.want)
		}
		var back Snapshot
		if err := back.UnmarshalJSON(data); err != nil || !reflect.DeepEqual(back, c.snap) {
			t.Fatalf("roundtrip %s: %+v", data, back)
		}
	}
}

func TestBackupReadsPythonLeniency(t *testing.T) {
	var s Snapshot
	// Service order is kept; null and odd values fall back like Python's `or`.
	if err := s.UnmarshalJSON([]byte(`{"services":{"B":{"url":"u","enabled":1},"A":{}},"mode":null,"kde":{"type":2}}`)); err != nil {
		t.Fatal(err)
	}
	want := Snapshot{GNOME: &GNOME{}, KDE: &KDE{Type: "2"},
		Mac: &Mac{Services: []Service{{Name: "B", URL: "u", Enabled: true}, {Name: "A"}}}}
	if !reflect.DeepEqual(s, want) {
		t.Fatalf("%+v", s)
	}
	for _, bad := range []string{"", "[1]", "null", "{"} {
		if err := s.UnmarshalJSON([]byte(bad)); err == nil {
			t.Fatal(bad)
		}
	}
}

func TestUninstallOfUnreadableBackupIsUnset(t *testing.T) {
	backup := tempBackup(t, "b.json")
	os.WriteFile(backup, []byte("not json"), 0o600)
	r := &runner{}
	if got, _ := gnome(r.run).Uninstall(backup); got != "unset" || len(r.calls) != 0 {
		t.Fatal(got, r.calls)
	}
}

func TestInstallCreatesPrivateBackupDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes")
	}
	backup := filepath.Join(t.TempDir(), "a", "uni-vpn", "proxy-backup.json")
	r := &runner{answers: gsettings("none", "")}
	if got, err := gnome(r.run).Install(1081, backup); got != "ok" || err != nil {
		t.Fatal(got, err)
	}
	st, err := os.Stat(filepath.Dir(backup))
	if err != nil || st.Mode().Perm()&0o077 != 0 {
		t.Fatal(st.Mode(), err)
	}
}
