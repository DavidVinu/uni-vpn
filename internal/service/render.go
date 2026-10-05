package service

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/DavidVinu/uni-vpn/internal/winsys"
)

// Pair is an ordered key/value: placeholder mappings and environment variables keep the
// order Python's dicts have, so the rendered texts are the same.
type Pair struct{ Key, Value string }

// SystemdTemplate is systemd/uni-vpn.service.in; a test keeps the copies in step.
const SystemdTemplate = `[Unit]
Description=Uni VPN (openconnect + ocproxy on demand)
PartOf=graphical-session.target
After=graphical-session.target
StartLimitIntervalSec=0

[Service]
Type=simple
ExecStart="@PYTHON@" "@UNI_VPN@" daemon
@EXTRA_ENV@
Restart=always
RestartSec=5
LimitCORE=0

[Install]
WantedBy=graphical-session.target
`

// LaunchdTemplate is launchd/de.davidvinu.uni-vpn.plist.in; a test keeps the copies in step.
const LaunchdTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>de.davidvinu.uni-vpn</string>
  <key>ProgramArguments</key>
  <array>
    <string>@PYTHON@</string>
    <string>@UNI_VPN@</string>
    <string>daemon</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>@PATH@</string>
@EXTRA_ENV@
  </dict>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>ProcessType</key>
  <string>Background</string>
  <key>StandardOutPath</key>
  <string>@LOG_DIR@/launchd.log</string>
  <key>StandardErrorPath</key>
  <string>@LOG_DIR@/launchd.log</string>
</dict>
</plist>
`

// The Go core is one binary: the interpreter goes, @UNI_VPN@ becomes the binary.
var binaryEdits = map[bool][2]string{
	false: {`ExecStart="@PYTHON@" "@UNI_VPN@" daemon`, `ExecStart="@UNI_VPN@" daemon`},
	true:  {"    <string>@PYTHON@</string>\n", ""},
}

var placeholder = regexp.MustCompile(`@[A-Z_]+@`)

// Render replaces @KEY@ placeholders in order and fails on any left over.
func Render(template string, mapping []Pair) (string, error) {
	text := template
	for _, kv := range mapping {
		text = strings.ReplaceAll(text, "@"+kv.Key+"@", kv.Value)
	}
	if leftover := placeholder.FindAllString(text, -1); leftover != nil {
		return "", fmt.Errorf("Placeholders not replaced: %s", strings.Join(leftover, ", "))
	}
	return text, nil
}

func checkValue(name, value string) error {
	// The values end up in systemd quotes or Environment= lines, where " and \ would be escape characters.
	if strings.ContainsAny(value, "\"\\\n") {
		return fmt.Errorf("%s must not contain quotes, backslashes or line breaks: %s", name, pyRepr(value))
	}
	return nil
}

func extraEnvBlock(macOS bool, extraEnv []Pair) string {
	lines := make([]string, len(extraEnv))
	for i, kv := range extraEnv {
		if macOS {
			lines[i] = "    <key>" + winsys.XMLEscape(kv.Key) + "</key>\n    <string>" + winsys.XMLEscape(kv.Value) + "</string>"
		} else {
			lines[i] = `Environment="` + kv.Key + "=" + kv.Value + `"`
		}
	}
	return strings.Join(lines, "\n")
}

func templateFor(macOS bool) string {
	if macOS {
		return LaunchdTemplate
	}
	return SystemdTemplate
}

func renderWith(macOS bool, template string, programs []Pair, logDir, brewPrefix string, extraEnv []Pair) (string, error) {
	pathEnv := "/usr/bin:/bin:/usr/sbin:/sbin"
	if brewPrefix != "" {
		pathEnv = brewPrefix + "/bin:" + brewPrefix + "/sbin:" + pathEnv
	}
	for _, kv := range extraEnv {
		if err := checkValue(kv.Key, kv.Value); err != nil {
			return "", err
		}
	}
	for _, kv := range programs {
		if err := checkValue(strings.ToLower(strings.ReplaceAll(kv.Key, "_", "-")), kv.Value); err != nil {
			return "", err
		}
	}
	mapping := append(programs, Pair{"LOG_DIR", logDir}, Pair{"PATH", pathEnv},
		Pair{"EXTRA_ENV", extraEnvBlock(macOS, extraEnv)})
	if len(extraEnv) == 0 {
		template = strings.Replace(template, "@EXTRA_ENV@\n", "", -1)
	}
	return Render(template, mapping)
}

// RenderPythonUnit is the systemd unit (or launchd plist when macOS) of the Python core,
// byte for byte as uni_vpn/service.py renders it.
func RenderPythonUnit(macOS bool, python, uniVPN, logDir, brewPrefix string, extraEnv []Pair) (string, error) {
	return renderWith(macOS, templateFor(macOS), []Pair{{"PYTHON", python}, {"UNI_VPN", uniVPN}},
		logDir, brewPrefix, extraEnv)
}

// RenderUnit is RenderPythonUnit for the Go core: the same text, but the program is the
// single binary, run as "<binary> daemon".
func RenderUnit(macOS bool, binary, logDir, brewPrefix string, extraEnv []Pair) (string, error) {
	edit := binaryEdits[macOS]
	template := strings.Replace(templateFor(macOS), edit[0], edit[1], 1)
	return renderWith(macOS, template, []Pair{{"UNI_VPN", binary}}, logDir, brewPrefix, extraEnv)
}

// PassthroughEnv returns the variables that determine config_dir()/state_dir(); otherwise
// the service does not get them.
func PassthroughEnv(getenv func(string) string) []Pair {
	var out []Pair
	for _, key := range PassthroughEnvKeys {
		if v := getenv(key); v != "" {
			out = append(out, Pair{key, v})
		}
	}
	return out
}

// pyRepr formats a string like Python's repr(), for messages that quote a value.
func pyRepr(s string) string {
	quote := byte('\'')
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		quote = '"'
	}
	var b strings.Builder
	b.WriteByte(quote)
	for _, r := range s {
		switch {
		case r == rune(quote) || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte(quote)
	return b.String()
}
