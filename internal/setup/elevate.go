package setup

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/DavidVinu/uni-vpn/internal/config"
	"github.com/DavidVinu/uni-vpn/internal/i18n"
	"github.com/DavidVinu/uni-vpn/internal/sysproxy"
)

// askingSystem is the proxy rule's system for what the user started (setup, repair,
// uninstall): on a Mac, a standard account may not change the network settings, and macOS asks
// for an administrator's name and password instead.
func askingSystem(goos string) *sysproxy.System {
	s := sysproxy.Default()
	if goos == "darwin" {
		s.Elevate = func(cmds [][]string) error {
			return askAdmin(sysproxy.Exec, cmds, adminPrompt(macLanguages()))
		}
	}
	return s
}

// ensureGuard is Setup.EnsureGuard: on a Mac where the guard does not answer, binary (this
// core) puts itself in place as the guard, as root, once macOS has an administrator's name and
// password.
func ensureGuard(goos, binary string) func() (bool, error) {
	if goos != "darwin" {
		return nil
	}
	return func() (bool, error) {
		if _, err := sysproxy.AskGuard("state"); err == nil {
			return false, nil
		}
		return true, askAdmin(sysproxy.Exec, [][]string{{binary, "guard", "install"}}, adminPrompt(macLanguages()))
	}
}

// askAdmin runs the commands as root once macOS has an administrator's name and password, in
// its own dialog with prompt as the reason. Cancelled: an error.
func askAdmin(run sysproxy.Runner, cmds [][]string, prompt string) error {
	r, err := run([]string{"/usr/bin/osascript",
		"-e", "on run argv",
		"-e", "do shell script (item 1 of argv) with prompt (item 2 of argv) with administrator privileges",
		"-e", "end run",
		adminScript(cmds), prompt}, 10*time.Minute)
	if err != nil {
		return err
	}
	if r.Code != 0 {
		return fmt.Errorf("osascript: %s", strings.TrimSpace(r.Stderr))
	}
	return nil
}

// adminScript is the commands for /bin/sh, each word quoted (a service may be called
// "Marie's iPhone"), all of them run.
func adminScript(cmds [][]string) string {
	lines := make([]string, len(cmds))
	for i, c := range cmds {
		words := make([]string, len(c))
		for j, w := range c {
			words[j] = "'" + strings.ReplaceAll(w, "'", `'\''`) + "'"
		}
		lines[i] = strings.Join(words, " ")
	}
	return strings.Join(lines, "; ")
}

// adminPrompt is the dialog's reason in the app's language setting, else the Mac's (accepted:
// "de-DE,en"), as the page picks it.
func adminPrompt(accepted string) string {
	setting := ""
	if cfg, err := config.Load(config.DefaultPath()); err == nil {
		setting = cfg.Language
	}
	key := "setup.admin_network"
	if text, ok := i18n.Catalog(i18n.Negotiate(setting, accepted))[key]; ok {
		return text
	}
	return i18n.Catalog(i18n.Source)[key]
}

var appleLanguage = regexp.MustCompile(`"?([A-Za-z]{2,3}(?:-[A-Za-z0-9]+)*)"?,?\s*$`)

// macLanguages is the Mac's preferred languages ("defaults read -g AppleLanguages" prints a
// list, one per line), joined for i18n.Negotiate.
func macLanguages() string {
	out, err := exec.Command("/usr/bin/defaults", "read", "-g", "AppleLanguages").Output()
	if err != nil {
		return ""
	}
	return parseAppleLanguages(string(out))
}

func parseAppleLanguages(out string) string {
	var tags []string
	for _, line := range strings.Split(out, "\n") {
		if m := appleLanguage.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			tags = append(tags, m[1])
		}
	}
	return strings.Join(tags, ",")
}
