package sysproxy

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/DavidVinu/uni-vpn/internal/atomicfile"
)

// Chrome, Edge and Brave on Linux read GNOME's proxy setting only on GNOME-like desktops
// (GNOME, Cinnamon, MATE, Budgie, Pantheon, Unity, Deepin, UKUI) and KDE's only on KDE. On
// Xfce, LXQt and the others they take the rule from the environment variable auto_proxy alone
// (Chromium's ProxyConfigServiceLinux). A session gets it at login from these files:
// ~/.profile (LightDM, GDM, and SDDM for bash) and ~/.xprofile (the X11 sessions of all three,
// also for zsh). Both get our lines, which take effect from the next login; uninstalling, and
// another account on the status port (Withdraw), take them out again.
var sessionFiles = [...]string{".profile", ".xprofile"}

const (
	sessionBegin = "# >>> uni-vpn >>>"
	sessionEnd   = "# <<< uni-vpn <<<"
)

var errSessionLines = errors.New("uni-vpn's lines have no end")

// sessionLines are our lines in the session files.
func sessionLines(httpPort int) string {
	return sessionBegin + "\n" +
		"# Chrome, Edge and Brave on Xfce, LXQt and other desktops read the proxy rule from here.\n" +
		"export auto_proxy=" + PacURL(httpPort) + "\n" +
		sessionEnd + "\n"
}

// ChromeReadsSettings reports whether Chrome, Edge and Brave in the session that getenv
// describes read GNOME's or KDE's proxy setting (Chromium's GetDesktopEnvironment). Where they
// do not, they read the rule from auto_proxy, which a session gets at login.
func ChromeReadsSettings(getenv func(string) string) bool {
	for _, name := range strings.Split(getenv("XDG_CURRENT_DESKTOP"), ":") {
		switch strings.TrimSpace(name) {
		case "Unity", "Deepin", "GNOME", "X-Cinnamon", "KDE", "Pantheon", "UKUI":
			return true
		case "XFCE", "LXQt":
			return false
		}
	}
	switch session := getenv("DESKTOP_SESSION"); {
	case session == "deepin", session == "gnome", session == "mate", session == "ukui",
		session == "kde", session == "kde4", session == "kde-plasma":
		return true
	case strings.Contains(session, "xfce"), session == "xubuntu":
		return false
	}
	return getenv("GNOME_DESKTOP_SESSION_ID") != "" || getenv("KDE_FULL_SESSION") != ""
}

// NamesDesktop reports whether getenv names the desktop, as in a session, which a service may
// not have.
func NamesDesktop(getenv func(string) string) bool {
	for _, name := range [...]string{"XDG_CURRENT_DESKTOP", "DESKTOP_SESSION", "GNOME_DESKTOP_SESSION_ID", "KDE_FULL_SESSION"} {
		if getenv(name) != "" {
			return true
		}
	}
	return false
}

// linux: the setting is GNOME's or KDE's (not macOS or Windows).
func (s *System) linux() bool { return !s.macOS() && !s.windows() }

// setSession puts our lines into the session files, in place of earlier ones. Errors are
// ignored: GNOME and KDE do not need the lines.
func (s *System) setSession(httpPort int) {
	for _, name := range sessionFiles {
		_, _ = s.editSession(name, sessionLines(httpPort))
	}
}

// clearSession takes our lines out of the session files. Reports whether any were there.
func (s *System) clearSession() bool {
	cleared := false
	for _, name := range sessionFiles {
		if changed, _ := s.editSession(name, ""); changed {
			cleared = true
		}
	}
	return cleared
}

// editSession replaces our lines in the session file name with lines ("" takes them out),
// where they were, else at the end. A file left empty is removed. A symbolic link (dotfiles
// kept elsewhere) stays: its target is changed. Reports whether the file changed.
func (s *System) editSession(name, lines string) (bool, error) {
	home := s.Getenv("HOME")
	if home == "" {
		return false, nil
	}
	path := filepath.Join(home, name)
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if err == nil {
		if real, err := filepath.EvalSymlinks(path); err == nil {
			path = real
		}
	}
	text := string(data)
	var kept strings.Builder
	at := -1 // where our lines were, in kept
	for rest := text; ; {
		begin := lineIndex(rest, sessionBegin)
		if begin < 0 {
			kept.WriteString(rest)
			break
		}
		end := lineIndex(rest[begin:], sessionEnd)
		if end < 0 {
			return false, errSessionLines // edited by hand: left as it is
		}
		end += begin + len(sessionEnd)
		if end < len(rest) && rest[end] == '\n' {
			end++
		}
		kept.WriteString(rest[:begin])
		if at < 0 {
			at = kept.Len()
		}
		rest = rest[end:]
	}
	out := kept.String()
	if lines != "" {
		if at < 0 {
			at = len(out)
			if out != "" && !strings.HasSuffix(out, "\n") {
				out += "\n"
				at++
			}
		}
		out = out[:at] + lines + out[at:]
	}
	if out == text {
		return false, nil
	}
	if strings.TrimSpace(out) == "" {
		return true, os.Remove(path)
	}
	return true, atomicfile.Write(path, []byte(out))
}

// lineIndex is where line starts in text as a whole line, -1 when it does not.
func lineIndex(text, line string) int {
	for from := 0; ; {
		i := strings.Index(text[from:], line)
		if i < 0 {
			return -1
		}
		i += from
		after := i + len(line)
		if (i == 0 || text[i-1] == '\n') && (after == len(text) || text[after] == '\n') {
			return i
		}
		from = i + 1
	}
}

// RuleAtStartName is the file in the state folder that says Chrome, Edge and Brave in this
// account's sessions take the rule from the session files, which they read when they start: a
// changed rule reaches them once they start again. Setup, which runs in the session, knows the
// desktop; the service may not.
const RuleAtStartName = "rule-at-browser-start"
