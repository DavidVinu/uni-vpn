package detect

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DavidVinu/uni-vpn/internal/i18n"
)

// fixture returns a reply recorded on 2026-10-05 with the init request and no credentials.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "tests", "fixtures", "detect", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func parse(t *testing.T, name string) Detection {
	return ParseReply(fixture(t, name), "vpn.example.edu", "")
}

func fieldNames(d Detection) []string {
	var out []string
	for _, f := range d.Fields {
		out = append(out, f.Name)
	}
	return out
}

func TestHeidelbergIsAPasswordFormAlthoughItNamesSSO(t *testing.T) {
	d := parse(t, "heidelberg.xml")
	if !d.Cisco || d.SAML {
		t.Fatalf("cisco=%v saml=%v", d.Cisco, d.SAML)
	}
	if got := fieldNames(d); !reflect.DeepEqual(got, []string{"username", "password"}) {
		t.Errorf("fields %v", got)
	}
	if len(d.Groups) != 0 {
		t.Errorf("groups %v", d.Groups)
	}
	if d.Suggestion().MFA != "none" {
		t.Error("the OTP form comes only after the password")
	}
	if !strings.Contains(d.Message, "Benutzernamen") {
		t.Errorf("message %q", d.Message)
	}
}

func TestBremenGroupsAndSecondPassword(t *testing.T) {
	d := parse(t, "bremen.xml")
	if !reflect.DeepEqual(d.Groups, []string{"Tunnel-All-Traffic", "Tunnel-Uni-Bremen"}) {
		t.Errorf("groups %v", d.Groups)
	}
	if d.Group != "Tunnel-All-Traffic" || !d.SecondPassword {
		t.Errorf("group %q second %v", d.Group, d.SecondPassword)
	}
	want := Suggestion{Host: "vpn.example.edu", Usergroup: "", Authgroup: "Tunnel-All-Traffic", MFA: "totp_field"}
	if d.Suggestion() != want {
		t.Errorf("suggestion %+v", d.Suggestion())
	}
}

func TestETHZRealmGroups(t *testing.T) {
	d := parse(t, "ethz.xml")
	if !reflect.DeepEqual(d.Groups, []string{"staff-net", "student-net"}) {
		t.Errorf("groups %v", d.Groups)
	}
	if s := d.Suggestion(); s.Authgroup != "staff-net" || s.MFA != "totp_field" {
		t.Errorf("suggestion %+v", s)
	}
}

func TestMarburgAppendsTheCodeWhichTheFormDoesNotShow(t *testing.T) {
	d := parse(t, "marburg.xml")
	if d.Group != "unimr-vpn-staff-Passwort+2FA" || len(d.Groups) != 4 || d.SecondPassword {
		t.Errorf("group %q groups %v second %v", d.Group, d.Groups, d.SecondPassword)
	}
	if d.Suggestion().MFA != "none" {
		t.Errorf("mfa %q", d.Suggestion().MFA)
	}
}

func TestMuensterAndKasselWithoutGroups(t *testing.T) {
	if !parse(t, "muenster.xml").SecondPassword {
		t.Error("muenster has a second password")
	}
	d := parse(t, "kassel.xml")
	if len(d.Groups) != 0 || d.Group != "" || d.Message != "" || d.Suggestion().Authgroup != "" {
		t.Errorf("%+v", d)
	}
}

func TestStanfordDefaultGroupIsSAMLButGroupStanfordIsNot(t *testing.T) {
	def := parse(t, "stanford.xml")
	if !def.SAML || def.Group != "CardinalKey" || def.Suggestion().MFA != "saml" {
		t.Errorf("%+v", def)
	}
	found := false
	for _, g := range def.Groups {
		found = found || g == "Stanford"
	}
	if !found {
		t.Errorf("groups %v", def.Groups)
	}
	stanford := parse(t, "stanford-group-stanford.xml")
	if stanford.SAML || stanford.Group != "Stanford" {
		t.Errorf("%+v", stanford)
	}
}

func TestFUBerlinIsSAML(t *testing.T) {
	d := parse(t, "fu-berlin.xml")
	if !d.SAML || len(d.Fields) != 0 {
		t.Errorf("%+v", d)
	}
}

func TestOtherRepliesAreNotCisco(t *testing.T) {
	for _, data := range []string{
		"<html><body>Welcome</body></html>", "not xml", "<?xml version='1.0'?><other/>",
		`<!DOCTYPE x [<!ENTITY a "aaaa">]><config-auth>&a;</config-auth>`,
		"<config-auth>" + strings.Repeat("x", MaxReply+1) + "</config-auth>",
		// expat refuses these too; encoding/xml alone would not.
		"<config-auth/><config-auth/>", "junk<config-auth/>", "<config-auth/>junk",
		"<!doctype x><config-auth/>", "<config-auth>&nbsp;</config-auth>",
	} {
		d := ParseReply([]byte(data), "www.example.edu", "")
		head := data
		if len(head) > 40 {
			head = head[:40]
		}
		if d.Cisco || !d.Reachable || d.Error.Key != "detect.not_cisco" {
			t.Errorf("%q: %+v", head, d)
		}
	}
}

func TestEdgeRepliesAreCisco(t *testing.T) {
	for _, data := range []string{
		"\xef\xbb\xbf<?xml version=\"1.0\"?>\n<!-- c --><config-auth/>\n",
		"<?xml version=\"1.0\" encoding=\"ISO-8859-1\"?><config-auth><auth><message>Gr\xfc\xdfe</message></auth></config-auth>",
	} {
		d := ParseReply([]byte(data), "www.example.edu", "")
		if !d.Cisco {
			t.Errorf("%q: %+v", data, d)
		}
	}
	d := ParseReply([]byte("<?xml version=\"1.0\" encoding=\"ISO-8859-1\"?><config-auth><auth><message> Gr\xfc\xdfe </message></auth></config-auth>"), "h.example.edu", "")
	if d.Message != "Grüße" {
		t.Errorf("message %q", d.Message)
	}
	if d := ParseReply([]byte("<config-auth/>"), "h.example.edu", ""); d.Error.Key != "detect.no_form" {
		t.Errorf("error %v", d.Error)
	}
}

func TestAsDictIsJSONReady(t *testing.T) {
	raw, err := json.Marshal(parse(t, "bremen.xml"))
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	if data["suggestion"].(map[string]any)["mfa"] != "totp_field" {
		t.Errorf("suggestion %v", data["suggestion"])
	}
	want := map[string]any{"name": "secondary_password", "type": "password", "label": "Password:"}
	if got := data["fields"].([]any)[2]; !reflect.DeepEqual(got, want) {
		t.Errorf("fields[2] %v", got)
	}
	empty, _ := json.Marshal(Detection{Host: "h.example.edu"})
	const wantEmpty = `{"host":"h.example.edu","usergroup":"","reachable":false,"cisco":false,"saml":false,` +
		`"groups":[],"group":"","fields":[],"second_password":false,"message":"","error":"","error_t":null,` +
		`"suggestion":{"host":"h.example.edu","usergroup":"","authgroup":"","mfa":"none"}}`
	if string(empty) != wantEmpty {
		t.Errorf("got %s", empty)
	}
}

func TestInitRequestIsWhatOpenconnectSends(t *testing.T) {
	root, err := parseXML(InitRequest("vpn.example.edu", "staff", "A&B <x>"))
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := root.get("type"); v != "init" {
		t.Errorf("type %q", v)
	}
	if v := root.findText("group-access"); v != "https://vpn.example.edu/staff" {
		t.Errorf("group-access %q", v)
	}
	if v := root.findText("group-select"); v != "A&B <x>" {
		t.Errorf("group-select %q", v)
	}
	if v := root.findText("capabilities", "auth-method"); v != "single-sign-on-v2" {
		t.Errorf("auth-method %q", v)
	}
	root, err = parseXML(InitRequest("vpn.example.edu", "", ""))
	if err != nil || root.find("group-select") != nil {
		t.Errorf("err %v", err)
	}
}

// gateway is a TLS test server standing in for an AnyConnect gateway.
type gateway struct {
	*httptest.Server
	mu       sync.Mutex
	requests []recorded
}

type recorded struct {
	method, host, path string
	header             http.Header
	body               []byte
}

func newGateway(t *testing.T, handler http.HandlerFunc) *gateway {
	g := &gateway{}
	g.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		g.mu.Lock()
		g.requests = append(g.requests, recorded{r.Method, r.Host, r.URL.Path, r.Header.Clone(), body})
		g.mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(g.Close)
	return g
}

func (g *gateway) host() string { return strings.TrimPrefix(g.URL, "https://") }

func (g *gateway) seen() []recorded {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]recorded(nil), g.requests...)
}

func (g *gateway) opts() Options {
	pool := x509.NewCertPool()
	pool.AddCert(g.Certificate())
	return Options{RootCAs: pool}
}

func TestProbePostsWithTheAnyConnectHeaders(t *testing.T) {
	bremen := fixture(t, "bremen.xml")
	g := newGateway(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(bremen) })
	d, err := Probe(context.Background(), " "+g.host()+" ", "", "Tunnel-Uni-Bremen", g.opts())
	if err != nil {
		t.Fatal(err)
	}
	reqs := g.seen()
	if len(reqs) != 1 {
		t.Fatalf("%d requests", len(reqs))
	}
	r := reqs[0]
	if r.method != "POST" || r.path != "/" || r.host != g.host() {
		t.Errorf("%s %s %s", r.method, r.host, r.path)
	}
	for key, want := range map[string]string{
		"User-Agent": "AnyConnect Linux_64 5.1.18.314", "X-Aggregate-Auth": "1", "X-Transcend-Version": "1",
		"X-Support-Http-Auth": "true", "Accept": "*/*", "Content-Type": "application/x-www-form-urlencoded",
		"Accept-Encoding": "identity", "Connection": "close",
	} {
		if got := r.header.Get(key); got != want {
			t.Errorf("%s: %q, want %q", key, got, want)
		}
	}
	if !strings.Contains(string(r.body), "<group-select>Tunnel-Uni-Bremen</group-select>") {
		t.Errorf("body %s", r.body)
	}
	if d.Host != g.host() || !d.SecondPassword || !d.Reachable || !d.Cisco {
		t.Errorf("%+v", d)
	}
}

func TestProbeUsesUsergroupAndUserAgent(t *testing.T) {
	g := newGateway(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("<config-auth/>")) })
	opts := g.opts()
	opts.UserAgent = "AnyConnect Windows 5.1"
	d, err := Probe(context.Background(), g.host(), "/staff/", "", opts)
	if err != nil {
		t.Fatal(err)
	}
	r := g.seen()[0]
	if r.path != "/staff" || r.header.Get("User-Agent") != "AnyConnect Windows 5.1" ||
		!strings.Contains(string(r.body), "<group-access>https://"+g.host()+"/staff</group-access>") ||
		strings.Contains(string(r.body), "group-select") {
		t.Errorf("%+v", r)
	}
	if d.Usergroup != "staff" || d.Error.Key != "detect.no_form" {
		t.Errorf("%+v", d)
	}
}

func TestUnreachableAndHTTPErrors(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := ln.Addr().String()
	ln.Close()
	d, err := Probe(context.Background(), closed, "", "", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if d.Reachable || d.Error.Key != "detect.unreachable" || arg(d.Error, "host") != closed ||
		!strings.Contains(arg(d.Error, "reason").(string), "Connection refused") {
		t.Errorf("%+v", d)
	}

	g := newGateway(t, func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	d, _ = Probe(context.Background(), g.host(), "", "", g.opts())
	if !d.Reachable || d.Cisco || !notCiscoHTTP(d, 404) {
		t.Errorf("%+v", d)
	}
}

func TestUntrustedCertificateIsUnreachable(t *testing.T) {
	g := newGateway(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("<config-auth/>")) })
	d, _ := Probe(context.Background(), g.host(), "", "", Options{})
	if d.Reachable || d.Error.Key != "detect.unreachable" || arg(d.Error, "host") != g.host() || len(g.seen()) != 0 {
		t.Errorf("%+v", d)
	}
}

func TestTimeoutIsUnreachable(t *testing.T) {
	g := newGateway(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	opts := g.opts()
	opts.Timeout = 200 * time.Millisecond
	d, _ := Probe(context.Background(), g.host(), "", "", opts)
	if d.Reachable || arg(d.Error, "host") != g.host() || arg(d.Error, "reason") != "timed out" {
		t.Errorf("%+v", d)
	}
}

func TestInvalidInputFailsBeforeAnyRequest(t *testing.T) {
	for _, c := range []struct{ host, group, field string }{
		{"vpn example", "", "host"}, {"vpn.example.edu", `a"b`, "authgroup"}, {"", "", "host"},
	} {
		_, err := Probe(context.Background(), c.host, "", c.group, Options{})
		var fe *FieldError
		if !errors.As(err, &fe) || fe.Field != c.field {
			t.Errorf("%q %q: %v", c.host, c.group, err)
		}
	}
}

func TestSplitAddress(t *testing.T) {
	for in, want := range map[string][2]string{
		"https://vpn.uni-muenster.de/exchange": {"vpn.uni-muenster.de", "exchange"},
		" vpn.Example.edu ":                    {"vpn.example.edu", ""},
		"http://vpn.example.edu/":              {"vpn.example.edu", ""},
		"HTTPS://http://vpn.example.edu.":      {"vpn.example.edu", ""},
		"vpn.example.edu:8443/a/b":             {"vpn.example.edu:8443", "a/b"},
		"vpn.Kassel.de":                        {"vpn.kassel.de", ""}, // Kelvin sign: Python's lower() makes it "k"
	} {
		host, group, err := SplitAddress(in)
		if err != nil || host != want[0] || group != want[1] {
			t.Errorf("%q: %q %q %v", in, host, group, err)
		}
	}
	_, _, err := SplitAddress("vpn.example.edu/a b")
	var fe *FieldError
	if !errors.As(err, &fe) || fe.Field != "usergroup" {
		t.Errorf("%v", err)
	}
	for _, in := range []string{"vpn.İ.edu", "localhost", strings.Repeat("a.", 127) + "abc", "httpſ://vpn.example.edu"} {
		if _, _, err := SplitAddress(in); !errors.As(err, &fe) || fe.Field != "host" {
			t.Errorf("%q: %v", in, err)
		}
	}
}

func TestRedirectPostsTheSameBodyAgain(t *testing.T) {
	member := newGateway(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(fixture(t, "bremen.xml")) })
	balancer := newGateway(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, member.URL+"/", http.StatusFound)
	})
	d, _ := Probe(context.Background(), balancer.host(), "", "", balancer.opts()) // one test cert for both
	if !d.Cisco || d.Host != balancer.host() {
		t.Fatalf("%+v", d)
	}
	first, again := balancer.seen()[0], member.seen()
	if len(again) != 1 {
		t.Fatalf("%d requests", len(again))
	}
	r := again[0]
	if r.method != "POST" || string(r.body) != string(first.body) || r.host != member.host() ||
		r.header.Get("User-Agent") != DefaultUserAgent || r.header.Get("X-Aggregate-Auth") != "1" {
		t.Errorf("%+v", r)
	}
}

func TestRedirectAwayFromHTTPSIsRefused(t *testing.T) {
	g := newGateway(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://"+r.Host+"/", http.StatusFound)
	})
	d, _ := Probe(context.Background(), g.host(), "", "", g.opts())
	if !d.Reachable || !notCiscoHTTP(d, 302) || len(g.seen()) != 1 {
		t.Errorf("%+v", d)
	}
}

func TestRedirectLimits(t *testing.T) {
	// Distinct URLs: urllib follows MaxRedirects of them and refuses the next.
	g := newGateway(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.Path+"x", http.StatusTemporaryRedirect)
	})
	d, _ := Probe(context.Background(), g.host(), "", "", g.opts())
	if !notCiscoHTTP(d, 307) || len(g.seen()) != 1+MaxRedirects {
		t.Errorf("%+v after %d requests", d, len(g.seen()))
	}
	// One and the same URL: max_repeats (4) applies instead.
	g = newGateway(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/same", http.StatusMovedPermanently)
	})
	d, _ = Probe(context.Background(), g.host(), "", "", g.opts())
	if !notCiscoHTTP(d, 301) || len(g.seen()) != 1+maxRepeats {
		t.Errorf("%+v after %d requests", d, len(g.seen()))
	}
}

func TestOversizedReplyIsNotCisco(t *testing.T) {
	g := newGateway(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<config-auth>" + strings.Repeat("x", MaxReply) + "</config-auth>"))
	})
	d, _ := Probe(context.Background(), g.host(), "", "", g.opts())
	if !d.Reachable || d.Cisco || d.Error.Key != "detect.not_cisco" {
		t.Errorf("%+v", d)
	}
}

// arg is the value of a named argument of a text, nil if it has none.
func arg(text i18n.Text, name string) any {
	for _, a := range text.Args {
		if a.Name == name {
			return a.Value
		}
	}
	return nil
}

// notCiscoHTTP: the address answered with this HTTP status instead of a Cisco reply.
func notCiscoHTTP(d Detection, code int) bool {
	return d.Error.Key == "detect.not_cisco_http" && arg(d.Error, "code") == code
}
