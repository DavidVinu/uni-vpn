package setup

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DavidVinu/uni-vpn/internal/i18n"
	"github.com/DavidVinu/uni-vpn/internal/sysproxy"
)

// The script macOS runs as root after the password: each word arrives as it was, also a service
// name with spaces and an apostrophe, and every command runs.
func TestAdminScriptKeepsEveryWord(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil || runtime.GOOS == "windows" {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	fake := "#!/bin/sh\nfor a in \"$@\"; do printf '[%s]' \"$a\" >> " + log + "; done\necho >> " + log + "\nexit 4\n"
	if err := os.WriteFile(filepath.Join(dir, "networksetup"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	cmds := [][]string{
		{"networksetup", "-setautoproxyurl", "Marie's iPhone USB", "http://127.0.0.1:1081/proxy.pac?v=5"},
		{"networksetup", "-setautoproxystate", `Wi-Fi "home" $HOME`, "off"},
	}
	cmd := exec.Command(sh, "-c", adminScript(cmds))
	cmd.Env = append(os.Environ(), "PATH="+dir+":/usr/bin:/bin")
	cmd.Run()
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	want := "[-setautoproxyurl][Marie's iPhone USB][http://127.0.0.1:1081/proxy.pac?v=5]\n" +
		"[-setautoproxystate][Wi-Fi \"home\" $HOME][off]\n"
	if string(data) != want {
		t.Fatalf("%q", data)
	}
}

func TestAskAdminHandsMacOSTheScriptAndTheReason(t *testing.T) {
	var got []string
	run := func(args []string, timeout time.Duration) (sysproxy.Result, error) {
		got = args
		if timeout < time.Minute {
			t.Error("the user needs time to type the password:", timeout)
		}
		return sysproxy.Result{}, nil
	}
	cmds := [][]string{{"networksetup", "-setautoproxyurl", "Wi-Fi", "http://127.0.0.1:1081/proxy.pac"}}
	if err := askAdmin(run, cmds, "why"); err != nil {
		t.Fatal(err)
	}
	if got[0] != "/usr/bin/osascript" || got[len(got)-2] != adminScript(cmds) || got[len(got)-1] != "why" ||
		!strings.Contains(strings.Join(got, " "), "with administrator privileges") {
		t.Fatal(got)
	}
	cancelled := func([]string, time.Duration) (sysproxy.Result, error) {
		return sysproxy.Result{Code: 1, Stderr: "execution error: User canceled. (-128)\n"}, nil
	}
	if err := askAdmin(cancelled, cmds, "why"); err == nil {
		t.Fatal("a cancelled dialog counts as done")
	}
	broken := func([]string, time.Duration) (sysproxy.Result, error) {
		return sysproxy.Result{}, errors.New("no osascript")
	}
	if err := askAdmin(broken, cmds, "why"); err == nil {
		t.Fatal("no osascript counts as done")
	}
}

func TestAdminPromptInTheMacsLanguage(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // no language setting
	t.Setenv("HOME", t.TempDir())
	out := "(\n    \"de-DE\",\n    en,\n    \"zh-Hant-TW\"\n)\n"
	if got := parseAppleLanguages(out); got != "de-DE,en,zh-Hant-TW" {
		t.Fatal(got)
	}
	if got := adminPrompt("de-DE,en"); got != i18n.Catalog("de")["setup.admin_network"] || got == "" {
		t.Fatal(got)
	}
	if got := adminPrompt(""); got != i18n.Catalog("en")["setup.admin_network"] {
		t.Fatal(got)
	}
}

// Only on a Mac, and only for what the user started.
func TestOnlyAMacAsksForAnAdministrator(t *testing.T) {
	if askingSystem("linux").Elevate != nil || askingSystem("windows").Elevate != nil || askingSystem("darwin").Elevate == nil {
		t.Fatal("Elevate where it does not belong")
	}
	if sysproxy.Default().Elevate != nil {
		t.Fatal("the core's own looks at the rule would ask")
	}
}

// Setup and Repair put the guard in place where it does not run or is older than this core's;
// a current one is left as it is, with no password asked.
func TestEnsureGuardReplacesAMissingOrOlderGuard(t *testing.T) {
	for _, c := range []struct {
		answers map[string]string
		install bool
	}{
		{map[string]string{"version": strconv.Itoa(sysproxy.GuardVersion), "state": "off"}, false},
		{map[string]string{"version": strconv.Itoa(sysproxy.GuardVersion - 1), "state": "off"}, true},
		{map[string]string{"state": "on 7"}, true},
		{map[string]string{}, true},
	} {
		ask := func(request string) (string, error) {
			if a, ok := c.answers[request]; ok {
				return a, nil
			}
			return "", errors.New("no guard")
		}
		var ran [][]string
		run := func(args []string, _ time.Duration) (sysproxy.Result, error) {
			ran = append(ran, args)
			return sysproxy.Result{}, nil
		}
		installed, err := ensureGuardWith(ask, run, "/Users/ann/Library/Application Support/uni-vpn/app/bin/uni-vpn-core")
		if err != nil || installed != c.install || (len(ran) == 1) != c.install {
			t.Error(c.answers, installed, err, ran)
		}
		if c.install && !strings.Contains(strings.Join(ran[0], " "), "'/Users/ann/Library/Application Support/uni-vpn/app/bin/uni-vpn-core' 'guard' 'install'") {
			t.Error(ran[0])
		}
	}
}
