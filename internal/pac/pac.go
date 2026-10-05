// Package pac holds the domain list and the PAC file (Proxy Auto-Config) for the browsers.
// It mirrors uni_vpn/pac.py; file format and PAC text must stay identical.
//
// The browsers fetch the rule from the daemon (/proxy.pac): listed hosts and their
// subdomains go via SOCKS5 127.0.0.1:<socks_port>, everything else direct.
package pac

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/DavidVinu/uni-vpn/internal/platform"
)

// DefaultDomains are Heidelberg's defaults. Callers must not modify the slice.
var DefaultDomains = []string{
	"sogo.uni-heidelberg.de",
	"elearning-med.uni-heidelberg.de",
	// elearning-med embeds matomo.js from there; the host is reachable only on the university
	// network, and otherwise the browser waits until the connection times out (Chrome: 136 s).
	"cip.dmed.uni-heidelberg.de",
	// heiCO login uses the UHDLOGINTERNAL realm, which answers 403 "Nur intern zugaenglich"
	// outside the university network.
	"heico.uni-heidelberg.de",
}

const label = `[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?`

// hostRE is HOST_RE without its length lookahead (RE2 has none); validHost checks the length.
var hostRE = regexp.MustCompile(`^` + label + `(?:\.` + label + `)+$`)

// Header is the first line written to domains.txt.
const Header = "# uni-vpn: domains that go through the university. One per line, also covers subdomains, # starts a comment.\n"

func validHost(s string) bool {
	return len(s) >= 1 && len(s) <= 253 && hostRE.MatchString(s)
}

// NormalizeHost trims whitespace, lowercases and drops trailing dots.
func NormalizeHost(value string) string {
	return strings.TrimRight(pyLower(strings.TrimFunc(value, pyIsSpace)), ".")
}

// ParseDomainList returns (domains, errors). Errors name the line so the status page can
// show them. Both slices are non-nil.
func ParseDomainList(text string) (domains []string, errs []string) {
	domains, errs = []string{}, []string{}
	seen := map[string]bool{}
	for i, raw := range splitLines(text) {
		before, _, _ := strings.Cut(raw, "#")
		line := NormalizeHost(before)
		if line == "" {
			continue
		}
		line = strings.TrimPrefix(line, "*.")
		if !validHost(line) {
			errs = append(errs, fmt.Sprintf("line %d: '%s' is not a hostname", i+1, strings.TrimFunc(raw, pyIsSpace)))
			continue
		}
		if !seen[line] {
			seen[line] = true
			domains = append(domains, line)
		}
	}
	return domains, errs
}

// Matches reports whether host is one of domains or a subdomain of one.
func Matches(host string, domains []string) bool {
	h := NormalizeHost(host)
	if h == "" {
		return false
	}
	for _, d := range domains {
		if h == d || strings.HasSuffix(h, "."+d) {
			return true
		}
	}
	return false
}

// BuildPAC returns the PAC script for domains via SOCKS5 127.0.0.1:port.
func BuildPAC(domains []string, port int) string {
	return strings.Join([]string{
		"function FindProxyForURL(url, host) {",
		"  var domains = " + jsonList(domains) + ";",
		"  host = host.toLowerCase();",
		`  if (host.charAt(host.length - 1) === ".") host = host.slice(0, -1);`,
		"  for (var i = 0; i < domains.length; i++) {",
		"    var d = domains[i];",
		`    if (host === d || (host.length > d.length && host.slice(-(d.length + 1)) === "." + d)) {`,
		`      return "SOCKS5 127.0.0.1:` + strconv.Itoa(port) + `";`,
		"    }",
		"  }",
		`  return "DIRECT";`,
		"}",
		"",
	}, "\n")
}

// DomainsPath is domains.txt in the config directory.
func DomainsPath() string {
	return filepath.Join(platform.ConfigDir(), "domains.txt")
}

// ReadDomains reads the domain list; invalid lines are skipped. If the file is missing,
// defaults apply (a copy). Python's read_domains(path) without defaults is
// ReadDomains(path, DefaultDomains); an empty defaults slice means no domains.
func ReadDomains(path string, defaults []string) ([]string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return append([]string{}, defaults...), nil
	}
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("%s: not valid UTF-8", path)
	}
	domains, _ := ParseDomainList(string(data))
	return domains, nil
}

// WriteDomains writes the header and one domain per line. A missing directory is created
// with mode 0700 (its missing parents with default permissions, like Python's mkdir).
func WriteDomains(path string, domains []string) error {
	if err := mkdirParents(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString(Header)
	for _, d := range domains {
		b.WriteString(d)
		b.WriteString("\n")
	}
	text := b.String()
	if runtime.GOOS == "windows" {
		// Python's write_text translates newlines in text mode.
		text = strings.ReplaceAll(text, "\n", "\r\n")
	}
	return os.WriteFile(path, []byte(text), 0o666)
}

func mkdirParents(dir string, mode os.FileMode) error {
	err := os.Mkdir(dir, mode)
	if err == nil {
		return nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		parent := filepath.Dir(dir)
		if parent == dir {
			return err
		}
		if err := mkdirParents(parent, 0o777); err != nil {
			return err
		}
		err = os.Mkdir(dir, mode)
		if err == nil {
			return nil
		}
	}
	if info, statErr := os.Stat(dir); statErr == nil && info.IsDir() {
		return nil
	}
	return err
}

// pyIsSpace matches Python's str.isspace().
func pyIsSpace(r rune) bool {
	return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
}

// pyLower is str.lower(): simple mappings plus the one unconditional special mapping.
func pyLower(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == '\u0130' {
			b.WriteString("i\u0307")
		} else {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// splitLines is str.splitlines(): the same line boundaries, no trailing empty line.
func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch r {
		case '\n', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
			lines = append(lines, s[start:i])
			start = i + size
		case '\r':
			lines = append(lines, s[start:i])
			if i+1 < len(s) && s[i+1] == '\n' {
				size++
			}
			start = i + size
		}
		i += size
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

// jsonList is json.dumps(list_of_str) with Python's defaults (ensure_ascii, ", ").
func jsonList(items []string) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, s := range items {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteByte('"')
		for _, r := range s {
			switch {
			case r == '"':
				b.WriteString(`\"`)
			case r == '\\':
				b.WriteString(`\\`)
			case r == '\n':
				b.WriteString(`\n`)
			case r == '\r':
				b.WriteString(`\r`)
			case r == '\t':
				b.WriteString(`\t`)
			case r == '\b':
				b.WriteString(`\b`)
			case r == '\f':
				b.WriteString(`\f`)
			case r >= ' ' && r <= '~':
				b.WriteRune(r)
			case r > 0xFFFF:
				r -= 0x10000
				fmt.Fprintf(&b, `\u%04x\u%04x`, 0xD800+(r>>10), 0xDC00+(r&0x3FF))
			default:
				fmt.Fprintf(&b, `\u%04x`, r)
			}
		}
		b.WriteByte('"')
	}
	b.WriteByte(']')
	return b.String()
}
