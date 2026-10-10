package pac

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/DavidVinu/uni-vpn/internal/pyjson"
)

func TestDefaults(t *testing.T) {
	want := []string{"sogo.uni-heidelberg.de", "elearning-med.uni-heidelberg.de", "cip.dmed.uni-heidelberg.de",
		"heico.uni-heidelberg.de"}
	if !reflect.DeepEqual(DefaultDomains, want) {
		t.Fatal(DefaultDomains)
	}
}

func TestDefaultsRouteHeicoLogin(t *testing.T) {
	if !Matches("heico.uni-heidelberg.de", DefaultDomains) {
		t.Fatal()
	}
}

func TestParseNormalizesAndReportsErrorsWithLineNumbers(t *testing.T) {
	text := "Sogo.Uni-Heidelberg.DE\n# comment\n\n*.example.org  # wildcard\nsogo.uni-heidelberg.de\nnot valid\nhttp://x.y\n"
	domains, errs := ParseDomainList(text)
	if !reflect.DeepEqual(domains, []string{"sogo.uni-heidelberg.de", "example.org"}) {
		t.Fatal(domains)
	}
	if len(errs) != 2 || errs[1].String() != `Line 7: "http://x.y" is not a website` {
		t.Fatal(errs)
	}
	if errs[0].String() != `Line 6: "not valid" is not a website` || errs[0].JSON()[1].Value.(pyjson.Object)[0].Value != 6 {
		t.Fatal(errs[0])
	}
}

func TestParseRejectsSingleLabelAndTooLong(t *testing.T) {
	domains, errs := ParseDomainList("localhost\n" + strings.Repeat("a", 64) + ".de\n")
	if len(domains) != 0 || domains == nil || len(errs) != 2 {
		t.Fatal(domains, errs)
	}
}

// Edge cases checked against the Python implementation.
func TestParsePythonCompatibility(t *testing.T) {
	long := strings.Repeat(strings.Repeat("a", 63)+".", 4)[:254]
	text := "a.b\r\nc.d\re.f\x0bg.h\x1ci.j\u2028\u0130.de\n" + long + "\n " + long[:253] + " \n"
	domains, errs := ParseDomainList(text)
	if !reflect.DeepEqual(domains, []string{"a.b", "c.d", "e.f", "g.h", "i.j", long[:253]}) {
		t.Fatal(domains)
	}
	if len(errs) != 2 || errs[0].String() != "Line 6: \"\u0130.de\" is not a website" ||
		!strings.HasPrefix(errs[1].String(), "Line 7: ") {
		t.Fatal(errs)
	}
	if Matches("\u0130ntra.example", []string{"intra.example"}) {
		t.Fatal("Python lowercases \u0130 to i + combining dot")
	}
}

func TestMatchesHostAndSubdomainsOnly(t *testing.T) {
	d := []string{"sogo.uni-heidelberg.de", "example.org"}
	for host, want := range map[string]bool{
		"sogo.uni-heidelberg.de":    true,
		"SOGO.uni-heidelberg.de.":   true,
		"mail.example.org":          true,
		"notsogo.uni-heidelberg.de": false,
		"uni-heidelberg.de":         false,
		"":                          false,
	} {
		if Matches(host, d) != want {
			t.Errorf("Matches(%q) != %v", host, want)
		}
	}
}

func TestPACListsDomainsAndPort(t *testing.T) {
	text := BuildPAC([]string{"sogo.uni-heidelberg.de", "example.org"}, 1080)
	for _, part := range []string{"function FindProxyForURL(url, host)", `["sogo.uni-heidelberg.de", "example.org"]`,
		`"SOCKS5 127.0.0.1:1080"`, `"DIRECT"`} {
		if !strings.Contains(text, part) {
			t.Errorf("missing %s", part)
		}
	}
	if !strings.HasSuffix(text, "}\n") {
		t.Error("no trailing newline")
	}
}

func TestJSONListLikePython(t *testing.T) {
	got := jsonList([]string{"a\"b\\c\x7f\u00e9\U0001F600\x01"})
	want := `["a\"b\\c\u007f\u00e9\ud83d\ude00\u0001"]`
	if got != want {
		t.Fatal(got)
	}
	if jsonList(nil) != "[]" {
		t.Fatal(jsonList(nil))
	}
}

func TestPACEvaluatesLikeTheMatcher(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node missing")
	}
	script := BuildPAC([]string{"sogo.uni-heidelberg.de", "example.org"}, 1080) + `
const cases = ["sogo.uni-heidelberg.de", "SOGO.uni-heidelberg.de.", "mail.example.org", "notsogo.uni-heidelberg.de", "uni-heidelberg.de", "ifconfig.me"];
console.log(JSON.stringify(cases.map(h => FindProxyForURL("https://" + h + "/", h))));
`
	out, err := exec.Command(node, "-e", script).Output()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	want := []string{"SOCKS5 127.0.0.1:1080", "SOCKS5 127.0.0.1:1080", "SOCKS5 127.0.0.1:1080", "DIRECT", "DIRECT", "DIRECT"}
	if !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
}

func tempPath(t *testing.T) string {
	return filepath.Join(t.TempDir(), "domains.txt")
}

func read(t *testing.T, path string, defaults []string) []string {
	t.Helper()
	got, err := ReadDomains(path, defaults)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestMissingFileGivesDefaults(t *testing.T) {
	if got := read(t, tempPath(t), DefaultDomains); !reflect.DeepEqual(got, DefaultDomains) {
		t.Fatal(got)
	}
}

func TestMissingFileGivesTheUniversityDefaults(t *testing.T) {
	path := tempPath(t)
	if got := read(t, path, []string{"intranet.example.edu"}); !reflect.DeepEqual(got, []string{"intranet.example.edu"}) {
		t.Fatal(got)
	}
	if got := read(t, path, []string{}); got == nil || len(got) != 0 {
		t.Fatal(got)
	}
}

func TestRoundtripKeepsOrderAndCommentHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "dir", "domains.txt")
	if err := WriteDomains(path, []string{"example.org", "sogo.uni-heidelberg.de"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "#") {
		t.Fatal(string(data))
	}
	if got := read(t, path, DefaultDomains); !reflect.DeepEqual(got, []string{"example.org", "sogo.uni-heidelberg.de"}) {
		t.Fatal(got)
	}
	if info, err := os.Stat(filepath.Dir(path)); err != nil || (os.PathSeparator == '/' && info.Mode().Perm()&0o077 != 0) {
		t.Fatal(info.Mode(), err)
	}
}

func TestInvalidLinesAreSkippedWhenReading(t *testing.T) {
	path := tempPath(t)
	os.WriteFile(path, []byte("sogo.uni-heidelberg.de\nbroken\n"), 0o600)
	if got := read(t, path, DefaultDomains); !reflect.DeepEqual(got, []string{"sogo.uni-heidelberg.de"}) {
		t.Fatal(got)
	}
}

func TestEmptyFileMeansNoDomainsNotDefaults(t *testing.T) {
	path := tempPath(t)
	os.WriteFile(path, []byte("# nothing\n"), 0o600)
	if got := read(t, path, DefaultDomains); got == nil || len(got) != 0 {
		t.Fatal(got)
	}
}

func TestInvalidUTF8IsAnError(t *testing.T) {
	path := tempPath(t)
	os.WriteFile(path, []byte("\xff\n"), 0o600)
	if _, err := ReadDomains(path, DefaultDomains); err == nil {
		t.Fatal("no error")
	}
}
