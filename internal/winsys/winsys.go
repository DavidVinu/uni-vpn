// Package winsys holds Windows specifics: proxy registry key, job object, console control,
// scheduled task, user PATH. It mirrors the non-credential parts of uni_vpn/windows.py.
// The pure helpers (task XML, quoting, environment) build on every OS so tests run anywhere;
// the API calls exist only on Windows and return ErrUnsupported elsewhere.
package winsys

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const (
	InternetSettings = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`
	TaskName         = "uni-vpn"
	CreateNoWindow   = 0x08000000
	CreateNewConsole = 0x00000010
)

// ErrUnsupported is returned by the Windows API wrappers on other systems.
var ErrUnsupported = errors.New("winsys: only available on Windows")

// ChildEnvKeep lists the variables passed to the elevated openconnect (and its vpnc script):
// only these names, plus the system ones with trusted values. User variables override system
// ones on Windows, so for example ComSpec, PATH or a library search path could otherwise run
// user code elevated.
var ChildEnvKeep = []string{"TEMP", "TMP", "SystemDrive", "ProgramData", "ProgramFiles", "ProgramFiles(x86)",
	"ProgramW6432", "NUMBER_OF_PROCESSORS", "PROCESSOR_ARCHITECTURE", "OS", "USERNAME",
	"COMPUTERNAME"}

// ntJoin joins like Python's ntpath.join for two plain parts, independent of the host OS.
func ntJoin(parts ...string) string {
	out := parts[0]
	for _, p := range parts[1:] {
		if out != "" && !strings.HasSuffix(out, `\`) && !strings.HasSuffix(out, "/") {
			out += `\`
		}
		out += p
	}
	return out
}

// TrustedEnv builds the child environment from current ("KEY=value" entries, as os.Environ
// returns them) and the system folders from SystemDirs.
func TrustedEnv(current []string, system, windows string) []string {
	lower := map[string]string{}
	for _, kv := range current {
		// Windows keeps per-drive entries like "=C:=C:\x"; the name never starts at "=".
		i := strings.Index(kv[min(1, len(kv)):], "=")
		if i < 0 {
			continue
		}
		i += min(1, len(kv))
		lower[strings.ToLower(kv[:i])] = kv[i+1:]
	}
	var env []string
	for _, key := range ChildEnvKeep {
		if v, ok := lower[strings.ToLower(key)]; ok {
			env = append(env, key+"="+v)
		}
	}
	return append(env,
		"SystemRoot="+windows,
		"windir="+windows,
		"ComSpec="+ntJoin(system, "cmd.exe"),
		"PATH="+strings.Join([]string{system, windows, ntJoin(system, "Wbem"),
			ntJoin(system, "WindowsPowerShell", "v1.0")}, ";"),
		"PATHEXT=.COM;.EXE;.BAT;.CMD;.VBS;.JS;.WSF",
	)
}

// CurrentUser is DOMAIN\user as Task Scheduler expects it.
func CurrentUser() string {
	domain, user := os.Getenv("USERDOMAIN"), os.Getenv("USERNAME")
	if domain != "" {
		return domain + `\` + user
	}
	return user
}

var xmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// XMLEscape matches xml.sax.saxutils.escape: only &, < and >.
func XMLEscape(s string) string { return xmlEscaper.Replace(s) }

// RenderTask is the Task Scheduler XML of the Python core: start at logon of this user,
// elevated (Wintun needs it), restart after a crash, no time limit, also on battery.
func RenderTask(python, script, user, workdir string) string {
	return taskXML(python, "-I "+XMLEscape(QuoteArg(script))+" daemon", user, workdir)
}

// RenderTaskBinary is RenderTask for the Go core: the single binary runs "<binary> daemon".
func RenderTaskBinary(binary, user, workdir string) string {
	return taskXML(binary, "daemon", user, workdir)
}

// taskXML takes arguments already escaped.
func taskXML(command, arguments, user, workdir string) string {
	return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.4" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>uni-vpn: university VPN on demand for selected websites</Description>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
      <UserId>` + XMLEscape(user) + `</UserId>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>` + XMLEscape(user) + `</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>HighestAvailable</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>true</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <IdleSettings>
      <StopOnIdleEnd>false</StopOnIdleEnd>
      <RestartOnIdle>false</RestartOnIdle>
    </IdleSettings>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>true</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <WakeToRun>false</WakeToRun>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Priority>7</Priority>
    <RestartOnFailure>
      <Interval>PT1M</Interval>
      <Count>999</Count>
    </RestartOnFailure>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>` + XMLEscape(command) + `</Command>
      <Arguments>` + arguments + `</Arguments>
      <WorkingDirectory>` + XMLEscape(workdir) + `</WorkingDirectory>
    </Exec>
  </Actions>
</Task>
`
}

// QuoteArg quotes one argument like Python's subprocess.list2cmdline.
func QuoteArg(arg string) string {
	var b strings.Builder
	bs := 0
	needQuote := arg == "" || strings.ContainsAny(arg, " \t")
	if needQuote {
		b.WriteByte('"')
	}
	for _, c := range arg {
		switch c {
		case '\\':
			bs++
		case '"':
			b.WriteString(strings.Repeat(`\`, bs*2))
			bs = 0
			b.WriteString(`\"`)
		default:
			b.WriteString(strings.Repeat(`\`, bs))
			bs = 0
			b.WriteRune(c)
		}
	}
	b.WriteString(strings.Repeat(`\`, bs))
	if needQuote {
		// Backslashes before the closing quote are doubled.
		b.WriteString(strings.Repeat(`\`, bs))
		b.WriteByte('"')
	}
	return b.String()
}

// Pythonw returns pythonw.exe next to python.exe: no console window for the service.
func Pythonw(python string) string {
	dir, name := filepath.Split(python)
	if strings.ToLower(name) == "python.exe" {
		candidate := filepath.Join(dir, "pythonw.exe")
		if st, err := os.Stat(candidate); err == nil && st.Mode().IsRegular() {
			return candidate
		}
	}
	return python
}

func StartMenuDir() string {
	base := os.Getenv("APPDATA")
	if base == "" {
		if h, err := os.UserHomeDir(); err == nil {
			base = h
		} else {
			base = "~"
		}
	}
	return filepath.Join(base, "Microsoft", "Windows", "Start Menu", "Programs")
}

// normcase matches ntpath.normcase after rstrip("\\"), as the user PATH comparison does.
func normcase(entry string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimRight(entry, `\`), "/", `\`))
}

func pathEntries(value string) []string {
	var out []string
	for _, e := range strings.Split(value, ";") {
		if e != "" {
			out = append(out, e)
		}
	}
	return out
}

func joinPath(entries []string) string { return strings.Join(entries, ";") }
