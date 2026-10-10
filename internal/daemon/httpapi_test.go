//go:build !windows

// The daemon tests drive the POSIX tunnel with the fake openconnect, like tests/test_daemon.py (posix_only).

package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DavidVinu/uni-vpn/internal/config"
	"github.com/DavidVinu/uni-vpn/internal/credentials"
	"github.com/DavidVinu/uni-vpn/internal/detect"
	"github.com/DavidVinu/uni-vpn/internal/messages"
	"github.com/DavidVinu/uni-vpn/internal/pac"
	"github.com/DavidVinu/uni-vpn/internal/totp"
)

// Port of tests/test_httpapi.py.

type header struct{ key, value string }

type reply struct {
	status  int
	headers map[string]string
	body    []byte
}

func (r reply) json(t *testing.T) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(r.body, &v); err != nil {
		t.Fatalf("%v: %q", err, r.body)
	}
	return v
}

// raw sends one request by hand; the caller's Host or Content-Length replace the defaults.
func raw(t *testing.T, port int, method, path string, hdrs []header, body []byte) reply {
	t.Helper()
	c, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	all := []header{{"Host", "127.0.0.1"}, {"Content-Length", strconv.Itoa(len(body))}}
	for _, h := range hdrs {
		all = slices.DeleteFunc(all, func(o header) bool { return strings.EqualFold(o.key, h.key) })
		all = append(all, h)
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "%s %s HTTP/1.1", method, path)
	for _, h := range all {
		fmt.Fprintf(&b, "\r\n%s: %s", h.key, h.value)
	}
	b.WriteString("\r\n\r\n")
	b.Write(body)
	c.Write(b.Bytes())
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	data, err := io.ReadAll(c)
	if err != nil {
		t.Fatal(err)
	}
	head, payload, _ := bytes.Cut(data, []byte("\r\n\r\n"))
	lines := strings.Split(string(head), "\r\n")
	status, _ := strconv.Atoi(strings.Fields(lines[0])[1])
	got := map[string]string{}
	for _, line := range lines[1:] {
		k, v, _ := strings.Cut(line, ": ")
		got[strings.ToLower(k)] = v
	}
	return reply{status, got, payload}
}

var apiHeaders = []header{{"X-Uni-VPN", "1"}, {"Content-Type", "application/json"}}

func (h *harness) http(method, path string, hdrs []header, body string) reply {
	return raw(h.t, h.cfg.HTTPPort, method, path, hdrs, []byte(body))
}

func (h *harness) post(path, body string) reply { return h.http("POST", path, apiHeaders, body) }

func TestStatusJSON(t *testing.T) {
	h := newHarness(t)
	h.start(nil)
	r := h.http("GET", "/status.json", nil, "")
	if r.status != 200 || r.headers["content-type"] != "application/json" {
		t.Fatal(r.status, r.headers)
	}
	data := r.json(t)
	if data["protocol"] != 1.0 || data["state"] != "idle" || strings.Contains(string(r.body), "password") {
		t.Fatal(data)
	}
}

func TestStatusPage(t *testing.T) {
	h := newHarness(t)
	h.start(nil)
	r := h.http("GET", "/", nil, "")
	if r.status != 200 || !strings.Contains(r.headers["content-type"], "text/html") ||
		!bytes.Contains(r.body, []byte("Uni VPN")) || !bytes.Contains(r.body, []byte("/status.json")) {
		t.Fatal(r.status, r.headers)
	}
}

func TestNotFound(t *testing.T) {
	h := newHarness(t)
	h.start(nil)
	if r := h.http("GET", "/nope", nil, ""); r.status != 404 {
		t.Fatal(r.status)
	}
}

func TestPostWithoutHeaderIsForbidden(t *testing.T) {
	h := newHarness(t)
	d := h.start(nil)
	if r := h.http("POST", "/api/connect", nil, ""); r.status != 403 {
		t.Fatal(r.status)
	}
	if s, _ := d.snapshot(); s != Idle {
		t.Fatal(s)
	}
}

func TestPostWithForeignOriginIsForbidden(t *testing.T) {
	h := newHarness(t)
	h.start(nil)
	if r := h.http("POST", "/api/connect", []header{{"X-Uni-VPN", "1"}, {"Origin", "https://evil.example"}}, ""); r.status != 403 {
		t.Fatal(r.status)
	}
}

func TestPostWithNullOriginIsForbidden(t *testing.T) {
	h := newHarness(t)
	d := h.start(nil)
	r := h.http("POST", "/api/connect", []header{{"X-Uni-VPN", "1"}, {"Origin", "null"}}, "")
	if _, cors := r.headers["access-control-allow-origin"]; r.status != 403 || cors {
		t.Fatal(r.status, r.headers)
	}
	if s, _ := d.snapshot(); s != Idle {
		t.Fatal(s)
	}
	r = h.http("GET", "/status.json", []header{{"Origin", "null"}}, "")
	if _, cors := r.headers["access-control-allow-origin"]; r.status != 200 || cors {
		t.Fatal(r.status, r.headers)
	}
}

func TestOriginWithSpecialCharactersIsNeverReflected(t *testing.T) {
	h := newHarness(t)
	h.start(nil)
	for _, origin := range []string{"chrome-extension://abc\nX-Injected: 1", "moz-extension://abc;evil", "chrome-extension://a b"} {
		for _, req := range []struct {
			method string
			want   int
		}{{"POST", 403}, {"GET", 200}} {
			path := "/api/connect"
			if req.method == "GET" {
				path = "/status.json"
			}
			r := h.http(req.method, path, []header{{"X-Uni-VPN", "1"}, {"Origin", origin}}, "")
			_, cors := r.headers["access-control-allow-origin"]
			_, injected := r.headers["x-injected"]
			if r.status != req.want || cors || injected {
				t.Fatal(origin, r.status, r.headers)
			}
		}
	}
}

func TestForeignHostHeaderIsForbidden(t *testing.T) {
	h := newHarness(t)
	d := h.start(nil)
	port := strconv.Itoa(h.cfg.HTTPPort)
	for _, host := range []string{"evil.example:1081", "evil.example:" + port, "127.0.0.1:9", ""} {
		if r := h.http("GET", "/status.json", []header{{"Host", host}}, ""); r.status != 403 {
			t.Fatal(host, r.status)
		}
		if r := h.http("POST", "/api/connect", []header{{"Host", host}, {"X-Uni-VPN", "1"}}, ""); r.status != 403 {
			t.Fatal(host, r.status)
		}
	}
	if s, _ := d.snapshot(); s != Idle {
		t.Fatal(s)
	}
	for _, host := range []string{"127.0.0.1", "127.0.0.1:" + port, "localhost", "localhost:" + port} {
		if r := h.http("GET", "/status.json", []header{{"Host", host}}, ""); r.status != 200 {
			t.Fatal(host, r.status)
		}
	}
}

func TestResponsesDenyFraming(t *testing.T) {
	h := newHarness(t)
	h.start(nil)
	for _, path := range []string{"/", "/status.json", "/nope"} {
		if r := h.http("GET", path, nil, ""); r.headers["x-frame-options"] != "DENY" {
			t.Fatal(path, r.headers)
		}
	}
}

func TestBadContentLengthIsRejected(t *testing.T) {
	h := newHarness(t)
	d := h.start(nil)
	for _, value := range []string{"abc", "1e5", "-5", "12abc", "0x10"} {
		r := h.http("POST", "/api/connect", []header{{"X-Uni-VPN", "1"}, {"Content-Length", value}}, "")
		if r.status != 400 || !bytes.Contains(r.body, []byte("Content-Length")) {
			t.Fatal(value, r.status, string(r.body))
		}
	}
	if s, _ := d.snapshot(); s != Idle {
		t.Fatal(s)
	}
}

func TestConnectAndDisconnect(t *testing.T) {
	h := newHarness(t)
	d := h.start(nil)
	r := h.http("POST", "/api/connect", []header{{"X-Uni-VPN", "1"}}, "")
	if r.status != 200 || r.json(t)["ok"] != true {
		t.Fatal(r.status)
	}
	waitState(t, d, Connected)
	r = h.http("POST", "/api/disconnect", []header{{"X-Uni-VPN", "1"},
		{"Origin", "http://127.0.0.1:" + strconv.Itoa(h.cfg.HTTPPort)}}, "")
	if r.status != 200 {
		t.Fatal(r.status)
	}
	waitState(t, d, Idle)
}

func TestExtensionOriginsGetNoCORSHeader(t *testing.T) {
	h := newHarness(t)
	h.start(nil)
	for _, origin := range []string{"moz-extension://x", "chrome-extension://x"} {
		r := h.http("GET", "/status.json", []header{{"Origin", origin}}, "")
		if _, cors := r.headers["access-control-allow-origin"]; r.status != 200 || cors {
			t.Fatal(origin, r.headers)
		}
	}
}

func TestPasswordEndpoint(t *testing.T) {
	h := newHarness(t)
	d := h.start(nil)
	if r := h.post("/api/password", `{"password": "new"}`); r.status != 200 {
		t.Fatal(r.status)
	}
	if !slices.Equal(h.stored, []string{"new"}) {
		t.Fatal(h.stored)
	}
	waitState(t, d, Connected)
}

func TestPasswordEndpointRejectsBadJSON(t *testing.T) {
	h := newHarness(t)
	h.start(nil)
	for _, body := range []string{"{not json", `{"password": ""}`} {
		if r := h.http("POST", "/api/password", []header{{"X-Uni-VPN", "1"}}, body); r.status != 400 {
			t.Fatal(body, r.status)
		}
	}
}

func TestPasswordEndpointRejectsNewline(t *testing.T) {
	h := newHarness(t)
	d := h.start(nil)
	for _, password := range []string{"a\nb", "a\rb", "pw\n", "pw\r\ndelete-generic-password -s x"} {
		body, _ := json.Marshal(map[string]string{"password": password})
		r := h.post("/api/password", string(body))
		if r.status != 400 || string(r.body) != `{"ok": false, "error": "The password must be on one line", `+
			`"error_t": {"key": "password.line_break", "args": {}}}` {
			t.Fatal(password, r.status, string(r.body))
		}
	}
	if len(h.stored) != 0 {
		t.Fatal(h.stored)
	}
	if s, _ := d.snapshot(); s != Idle {
		t.Fatal(s)
	}
}

// --- TOTP -------------------------------------------------------------------------

func TestStatusPageHasTOTPForm(t *testing.T) {
	h := newHarness(t)
	h.start(nil)
	r := h.http("GET", "/", nil, "")
	// The portal link comes from the profile now, not from the page.
	if !bytes.Contains(r.body, []byte("/api/totp")) || bytes.Contains(r.body, []byte("mfa.uni-heidelberg.de")) {
		t.Fatal("page")
	}
	if url := h.http("GET", "/status.json", nil, "").json(t)["mfa_portal_url"]; url != "https://mfa.uni-heidelberg.de/" {
		t.Fatal(url)
	}
}

func TestStatusPageHasTheUniversityPicker(t *testing.T) {
	h := newHarness(t)
	h.start(nil)
	r := h.http("GET", "/", nil, "")
	for _, needle := range []string{`id="s-uni"`, `role="combobox"`, "/universities.json", "/api/detect", `id="s-mfa"`,
		"{portal}", "university: uni.id"} {
		if !bytes.Contains(r.body, []byte(needle)) {
			t.Fatal(needle)
		}
	}
	if bytes.Contains(r.body, []byte("—")) {
		t.Fatal("no em-dashes")
	}
}

func TestTOTPEndpointNormalizesStoresAndAnswersWithCheckCode(t *testing.T) {
	h := newHarness(t)
	d := h.start(nil)
	r := h.post("/api/totp", `{"secret": "gezd gnbv gy3t qojq gezd gnbv gy3t qojq"}`)
	if r.status != 200 {
		t.Fatal(r.status, string(r.body))
	}
	token := "base32:GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	code, _ := totp.Code(token)
	if data := r.json(t); data["ok"] != true || data["code"] != code {
		t.Fatal(data)
	}
	if !slices.Equal(h.storedTOTP, []string{token}) {
		t.Fatal(h.storedTOTP)
	}
	waitState(t, d, Connected)
}

func TestTOTPEndpointAcceptsOtpauthURI(t *testing.T) {
	h := newHarness(t)
	h.start(nil)
	if r := h.post("/api/totp", `{"secret": "otpauth://totp/Uni:ab1?secret=GEZDGNBVGY3TQOJQ&issuer=Uni"}`); r.status != 200 {
		t.Fatal(r.status)
	}
	if !slices.Equal(h.storedTOTP, []string{"base32:GEZDGNBVGY3TQOJQ"}) {
		t.Fatal(h.storedTOTP)
	}
}

func TestTOTPEndpointRejectsBadInput(t *testing.T) {
	h := newHarness(t)
	d := h.start(nil)
	for _, body := range []string{"{nope", `{"secret": ""}`, `{"secret": "0189"}`,
		`{"secret": "otpauth://hotp/x?secret=GEZDGNBVGY3TQOJQ"}`, `{"secret": 12}`} {
		if r := h.post("/api/totp", body); r.status != 400 || len(r.body) == 0 {
			t.Fatal(body, r.status)
		}
	}
	if len(h.storedTOTP) != 0 {
		t.Fatal(h.storedTOTP)
	}
	if s, _ := d.snapshot(); s != Idle {
		t.Fatal(s)
	}
}

func TestTOTPEndpointNeedsCSRFHeader(t *testing.T) {
	h := newHarness(t)
	h.start(nil)
	r := h.http("POST", "/api/totp", []header{{"Content-Type", "application/json"}}, `{"secret": "GEZDGNBVGY3TQOJQ"}`)
	if r.status != 403 || len(h.storedTOTP) != 0 {
		t.Fatal(r.status)
	}
}

func TestStatusNeverLeaksSecrets(t *testing.T) {
	h := newHarness(t)
	d := h.start(nil)
	d.RequestConnect()
	waitState(t, d, Connected)
	r := h.http("GET", "/status.json", nil, "")
	if bytes.Contains(r.body, []byte("GEZDGNBVGY3TQOJQ")) || bytes.Contains(r.body, []byte("pw-s3cret")) {
		t.Fatal(string(r.body))
	}
}

// --- Setup ------------------------------------------------------------------------

func (h *harness) startSetup() (*Daemon, string) {
	cfgPath := filepath.Join(h.t.TempDir(), "uni-vpn", "config.toml")
	h.cfg.User = ""
	d := h.start(func(o *Options) { o.ConfigError, o.ConfigPath, o.NeedsSetup = "missing", cfgPath, true })
	return d, cfgPath
}

func TestStatusReportsSetupNeededAndConnectIsRefused(t *testing.T) {
	h := newHarness(t)
	d, _ := h.startSetup()
	data := h.http("GET", "/status.json", nil, "").json(t)
	if data["setup_needed"] != true || data["state"] != "idle" {
		t.Fatal(data)
	}
	d.RequestConnect()
	if h.pwLines() != nil {
		t.Fatal(h.pwLines())
	}
}

func TestSetupWritesConfigStoresSecretsAndConnects(t *testing.T) {
	h := newHarness(t)
	d, cfgPath := h.startSetup()
	r := h.post("/api/setup", `{"user": "ab123", "password": "pw", "secret": "GEZDGNBVGY3TQOJQ"}`)
	code, _ := totp.Code("base32:GEZDGNBVGY3TQOJQ")
	if r.status != 200 || r.json(t)["code"] != code {
		t.Fatal(r.status, string(r.body))
	}
	if data, _ := os.ReadFile(cfgPath); !strings.Contains(string(data), `user = "ab123"`) {
		t.Fatal(string(data))
	}
	if !slices.Equal(h.stored, []string{"pw"}) || !slices.Equal(h.storedTOTP, []string{"base32:GEZDGNBVGY3TQOJQ"}) {
		t.Fatal(h.stored, h.storedTOTP)
	}
	waitState(t, d, Connected)
	if h.http("GET", "/status.json", nil, "").json(t)["setup_needed"] != false {
		t.Fatal("setup_needed")
	}
}

func TestChangingTheAccountFromSettingsReconnectsAndDropsOldOverrides(t *testing.T) {
	h := newHarness(t)
	d, cfgPath := h.startSetup()
	r := h.post("/api/setup", `{"user": "ab123", "password": "pw", "secret": "", "university": "other", `+
		`"profile": {"host": "vpn.example.edu", "authgroup": "staff", "mfa": "none"}}`)
	if r.status != 200 {
		t.Fatal(r.status, string(r.body))
	}
	waitState(t, d, Connected)
	d.mu.Lock()
	connects := d.connectCount
	d.mu.Unlock()
	r = h.post("/api/setup", `{"user": "cd456", "password": "pw2", "secret": "GEZDGNBVGY3TQOJQ", "university": "heidelberg"}`)
	if r.status != 200 {
		t.Fatal(r.status, string(r.body))
	}
	data, _ := os.ReadFile(cfgPath)
	text := string(data)
	if strings.Contains(text, "vpn.example.edu") || strings.Contains(text, "authgroup") || !strings.Contains(text, `user = "cd456"`) {
		t.Fatal(text)
	}
	d.mu.Lock()
	user, uni, host := d.cfg.User, d.cfg.University, d.cfg.Host
	d.mu.Unlock()
	if user != "cd456" || uni != "heidelberg" || host != "vpn-ac.uni-heidelberg.de" {
		t.Fatal(user, uni, host)
	}
	waitState(t, d, Connected)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.connectCount <= connects {
		t.Fatal(d.connectCount, connects)
	}
}

func TestKeyringFailureWritesNoConfigSoTheAssistantStays(t *testing.T) {
	h := newHarness(t)
	h.totpSetter = func(string) error { return &credentials.KeyringError{Msg: "keyring locked"} }
	d, cfgPath := h.startSetup()
	r := h.post("/api/setup", `{"user": "ab123", "password": "pw", "secret": "GEZDGNBVGY3TQOJQ"}`)
	if r.status != 500 || !bytes.Contains(r.body, []byte("keyring locked")) {
		t.Fatal(r.status, string(r.body))
	}
	if _, err := os.Stat(cfgPath); err == nil {
		t.Fatal("config written")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.needsSetup || d.cfg.User != "" {
		t.Fatal(d.needsSetup, d.cfg.User)
	}
}

func TestCheckCodeMarksTheWindowAsUsed(t *testing.T) {
	// The assistant asks to type the check code into the MFA portal, which uses it up.
	h := newHarness(t)
	d, _ := h.startSetup()
	if r := h.post("/api/totp/check", `{"secret": "GEZDGNBVGY3TQOJQ"}`); r.status != 200 {
		t.Fatal(r.status)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.hasOTPStep || d.lastOTPStep != time.Now().Unix()/30 {
		t.Fatal(d.lastOTPStep)
	}
}

func TestSetupRejectsBadInputWithoutWriting(t *testing.T) {
	h := newHarness(t)
	_, cfgPath := h.startSetup()
	for _, body := range []string{
		`{"user": "ab 1\"", "password": "pw", "secret": "GEZDGNBVGY3TQOJQ"}`,
		`{"user": "ab1", "password": "", "secret": "GEZDGNBVGY3TQOJQ"}`,
		`{"user": "ab1", "password": "a\nb", "secret": "GEZDGNBVGY3TQOJQ"}`,
		`{"user": "ab1", "password": "pw", "secret": "0189"}`,
		`{"user": "ab1", "password": "pw"}`,
	} {
		if r := h.post("/api/setup", body); r.status != 400 {
			t.Fatal(body, r.status)
		}
	}
	if _, err := os.Stat(cfgPath); err == nil || len(h.stored) != 0 {
		t.Fatal("written")
	}
}

func TestSetupNeedsCSRFHeader(t *testing.T) {
	h := newHarness(t)
	h.startSetup()
	r := h.http("POST", "/api/setup", []header{{"Content-Type", "application/json"}},
		`{"user": "ab1", "password": "pw", "secret": "GEZDGNBVGY3TQOJQ"}`)
	if r.status != 403 {
		t.Fatal(r.status)
	}
}

func TestTOTPCheckShowsCodeWithoutStoring(t *testing.T) {
	h := newHarness(t)
	h.start(nil)
	r := h.post("/api/totp/check", `{"secret": "otpauth://totp/x?secret=GEZDGNBVGY3TQOJQ"}`)
	if r.status != 200 {
		t.Fatal(r.status, string(r.body))
	}
	data := r.json(t)
	if len(data["code"].(string)) != 6 || data["remaining"].(float64) < 1 || data["remaining"].(float64) > 30 {
		t.Fatal(data)
	}
	if len(h.storedTOTP) != 0 {
		t.Fatal(h.storedTOTP)
	}
	if r := h.post("/api/totp/check", `{"secret": "nope!"}`); r.status != 400 {
		t.Fatal(r.status)
	}
}

func TestRepairButtonStartsTheRepair(t *testing.T) {
	h := newHarness(t)
	calls := make(chan struct{}, 2)
	h.start(func(o *Options) {
		o.RepairStart = func() (Process, error) { calls <- struct{}{}; return newFakeProcess(), nil }
	})
	if r := h.post("/api/repair", "{}"); r.status != 200 {
		t.Fatal(r.status)
	}
	if len(calls) != 1 {
		t.Fatal(len(calls))
	}
}

func TestErrorKindNamesTheFactor(t *testing.T) {
	h := newHarness(t)
	d := h.start(nil)
	d.forceSet(AuthFailed, messages.TOTPRejected)
	if statusValue(t, d, "error_kind") != "totp" || statusValue(t, d, "action") != "totp" ||
		statusValue(t, d, "message_id") != "totp_rejected" {
		t.Fatal()
	}
	d.forceSet(Keyring, messages.PasswordMissing)
	if statusValue(t, d, "error_kind") != "password" {
		t.Fatal()
	}
	d.forceSet(Idle, messages.NotConnected)
	if statusValue(t, d, "error_kind") != nil || statusValue(t, d, "action") != nil {
		t.Fatal()
	}
	// A repair is offered as an action, but it is not about a factor.
	d.forceSet(Error, messages.ProgramMissing)
	if statusValue(t, d, "action") != "repair" || statusValue(t, d, "error_kind") != nil {
		t.Fatal()
	}
}

// --- Universities -------------------------------------------------------------------

func (h *harness) postSetup(body string) (int, map[string]any) {
	r := h.post("/api/setup", body)
	return r.status, r.json(h.t)
}

func TestUniversityWithoutTOTPNeedsNoSecret(t *testing.T) {
	h := newHarness(t)
	h.totpErr = credentials.ErrTotpMissing
	d, cfgPath := h.startSetup()
	status, data := h.postSetup(`{"user": "ab123", "password": "pw", "university": "bonn"}`)
	if status != 200 || data["code"] != nil {
		t.Fatal(status, data)
	}
	if text, _ := os.ReadFile(cfgPath); !strings.Contains(string(text), `university = "bonn"`) {
		t.Fatal(string(text))
	}
	if !slices.Equal(h.stored, []string{"pw"}) || len(h.storedTOTP) != 0 {
		t.Fatal(h.stored, h.storedTOTP)
	}
	d.mu.Lock()
	host, mfa, name := d.cfg.Host, d.cfg.MFA, d.cfg.UniversityName
	d.mu.Unlock()
	if host != "unibn-vpn.uni-bonn.de" || mfa != "none" || name != "University of Bonn" {
		t.Fatal(host, mfa, name)
	}
	waitState(t, d, Connected)
}

func TestUnlistedUniversityWritesItsProfile(t *testing.T) {
	h := newHarness(t)
	h.totpErr = credentials.ErrTotpMissing
	d, cfgPath := h.startSetup()
	status, data := h.postSetup(`{"user": "ab123", "password": "pw", "secret": "", "university": "other", ` +
		`"profile": {"host": "VPN.Example.edu", "usergroup": "staff", "authgroup": "Staff (Split)", "mfa": "none"}}`)
	if status != 200 {
		t.Fatal(status, data)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.University != "other" || cfg.Host != "vpn.example.edu" || cfg.Usergroup != "staff" ||
		cfg.Authgroup != "Staff (Split)" || cfg.MFA != "none" {
		t.Fatal(cfg)
	}
	d.mu.Lock()
	host := d.cfg.Host
	d.mu.Unlock()
	if host != "vpn.example.edu" {
		t.Fatal(host)
	}
	waitState(t, d, Connected)
}

func TestErrorsNameTheUniversityStep(t *testing.T) {
	h := newHarness(t)
	_, cfgPath := h.startSetup()
	for body, field := range map[string]string{
		`"university": "fu-berlin"`:            "university",
		`"university": "nowhere"`:              "university",
		`"university": "other", "profile": {}`: "host",
		`"university": "other", "profile": {"host": "vpn.example.edu", "mfa": "sms"}`: "mfa",
		`"university": "heidelberg"`: "totp",
	} {
		status, data := h.postSetup(`{"user": "ab123", "password": "pw", ` + body + `}`)
		if status != 400 || data["field"] != field {
			t.Fatal(body, status, data)
		}
	}
	if _, data := h.postSetup(`{"user": "ab1", "password": "pw", "university": "fu-berlin"}`); data["error"] != messages.SAMLRequired.Text {
		t.Fatal(data)
	}
	if _, err := os.Stat(cfgPath); err == nil || len(h.stored) != 0 {
		t.Fatal("written")
	}
}

func TestUserWithARealmIsAccepted(t *testing.T) {
	h := newHarness(t)
	h.totpErr = credentials.ErrTotpMissing
	_, cfgPath := h.startSetup()
	status, data := h.postSetup(`{"user": "st1@stud.uni-stuttgart.de", "password": "pw", "university": "stuttgart"}`)
	if status != 200 {
		t.Fatal(status, data)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil || cfg.LoginName() != "st1@stud.uni-stuttgart.de" {
		t.Fatal(err)
	}
}

// --- Detect -------------------------------------------------------------------------

func TestUniversitiesJSONListsTheRegistryWithoutNotes(t *testing.T) {
	h := newHarness(t)
	h.start(nil)
	r := h.http("GET", "/universities.json", nil, "")
	if r.status != 200 {
		t.Fatal(r.status)
	}
	var data struct {
		Default      string           `json:"default"`
		Universities []map[string]any `json:"universities"`
	}
	json.Unmarshal(r.body, &data)
	if data.Default != "heidelberg" {
		t.Fatal(data.Default)
	}
	var ids []string
	for _, u := range data.Universities {
		if _, notes := u["notes"]; notes {
			t.Fatal("notes")
		}
		if u["id"] == "heidelberg" && u["mfa_portal_url"] != "https://mfa.uni-heidelberg.de/" {
			t.Fatal(u)
		}
		ids = append(ids, u["id"].(string))
	}
	if !slices.Contains(ids, "fu-berlin") {
		t.Fatal(ids)
	}
}

func TestDetectRunsTheProbeAndReturnsTheSuggestion(t *testing.T) {
	h := newHarness(t)
	d := h.start(nil)
	fixture, err := os.ReadFile(filepath.Join(repoRoot(t), "tests", "fixtures", "detect", "bremen.xml"))
	if err != nil {
		t.Fatal(err)
	}
	var calls [][3]string
	d.HTTP().Detector = func(_ context.Context, host, usergroup, group string) (detect.Detection, error) {
		calls = append(calls, [3]string{host, usergroup, group})
		return detect.ParseReply(fixture, host, usergroup), nil
	}
	r := h.post("/api/detect", `{"host": "https://VPN.uni-bremen.de/", "group": "Tunnel-Uni-Bremen"}`)
	if r.status != 200 {
		t.Fatal(r.status, string(r.body))
	}
	if !slices.Equal(calls, [][3]string{{"vpn.uni-bremen.de", "", "Tunnel-Uni-Bremen"}}) {
		t.Fatal(calls)
	}
	data := r.json(t)
	groups := fmt.Sprint(data["groups"])
	if data["ok"] != true || groups != "[Tunnel-All-Traffic Tunnel-Uni-Bremen]" ||
		data["suggestion"].(map[string]any)["mfa"] != "totp_field" {
		t.Fatal(data)
	}
	if !bytes.HasPrefix(r.body, []byte(`{"ok": true, `)) {
		t.Fatal(string(r.body))
	}
}

func TestDetectRefusesBadInputWithoutProbing(t *testing.T) {
	h := newHarness(t)
	d := h.start(nil)
	d.HTTP().Detector = func(context.Context, string, string, string) (detect.Detection, error) {
		t.Error("no probe for invalid input")
		return detect.Detection{}, nil
	}
	for _, body := range []string{`{"host": "not a host"}`, `{"host": 5}`, "[]", "{\"host\": \"vpn.example.edu\", \"group\": \"a\nb\"}"} {
		if r := h.post("/api/detect", body); r.status != 400 {
			t.Fatal(body, r.status)
		}
	}
}

func TestDetectNeedsCSRFHeader(t *testing.T) {
	h := newHarness(t)
	h.start(nil)
	if r := h.http("POST", "/api/detect", []header{{"Content-Type", "application/json"}}, `{"host": "vpn.example.edu"}`); r.status != 403 {
		t.Fatal(r.status)
	}
}

// --- PAC -------------------------------------------------------------------------

func TestProxyPACServesDefaultsWithSocksPort(t *testing.T) {
	h := newHarness(t)
	h.start(nil)
	r := h.http("GET", "/proxy.pac", nil, "")
	if r.status != 200 || r.headers["content-type"] != "application/x-ns-proxy-autoconfig" ||
		r.headers["cache-control"] != "no-store" {
		t.Fatal(r.status, r.headers)
	}
	text := string(r.body)
	if !strings.Contains(text, "FindProxyForURL") || !strings.Contains(text, "SOCKS5 127.0.0.1:"+strconv.Itoa(h.cfg.SocksPort)) {
		t.Fatal(text)
	}
	for _, domain := range pac.DefaultDomains {
		if !strings.Contains(text, domain) {
			t.Fatal(domain)
		}
	}
}

func TestStatusJSONListsDomainsAndPACURL(t *testing.T) {
	h := newHarness(t)
	h.start(nil)
	data := h.http("GET", "/status.json", nil, "").json(t)
	if fmt.Sprint(data["domains"]) != fmt.Sprint(pac.DefaultDomains) ||
		data["pac_url"] != "http://127.0.0.1:"+strconv.Itoa(h.cfg.HTTPPort)+"/proxy.pac" {
		t.Fatal(data)
	}
}

func TestStatusPageHasDomainForm(t *testing.T) {
	h := newHarness(t)
	h.start(nil)
	r := h.http("GET", "/", nil, "")
	if !bytes.Contains(r.body, []byte("/api/domains")) || !bytes.Contains(r.body, []byte(`id="domains"`)) ||
		bytes.Contains(r.body, []byte("Extension")) {
		t.Fatal("page")
	}
}

func TestDomainsEndpointWritesFileRefreshesProxyAndUpdatesPAC(t *testing.T) {
	h := newHarness(t)
	h.start(nil)
	r := h.post("/api/domains", `{"text": "Example.ORG\n# comment\nsogo.uni-heidelberg.de\n"}`)
	if r.status != 200 || fmt.Sprint(r.json(t)["domains"]) != "[example.org sogo.uni-heidelberg.de]" {
		t.Fatal(r.status, string(r.body))
	}
	if got, _ := pac.ReadDomains(h.domainsPath, pac.DefaultDomains); !slices.Equal(got, []string{"example.org", "sogo.uni-heidelberg.de"}) {
		t.Fatal(got)
	}
	if !slices.Equal(h.refreshed, []int{h.cfg.HTTPPort}) {
		t.Fatal(h.refreshed)
	}
	r = h.http("GET", "/proxy.pac", nil, "")
	if !bytes.Contains(r.body, []byte("example.org")) || bytes.Contains(r.body, []byte("elearning-med")) {
		t.Fatal(string(r.body))
	}
}

func TestDomainsEndpointRejectsInvalidLinesAndChangesNothing(t *testing.T) {
	h := newHarness(t)
	h.start(nil)
	r := h.post("/api/domains", `{"text": "sogo.uni-heidelberg.de\nbroken\n"}`)
	if r.status != 400 || !bytes.Contains(r.body, []byte(`"key": "domains.not_hostname", "args": {"line": 2`)) {
		t.Fatal(r.status, string(r.body))
	}
	if _, err := os.Stat(h.domainsPath); err == nil || len(h.refreshed) != 0 {
		t.Fatal("changed")
	}
}

func TestDomainsEndpointNeedsCSRFHeader(t *testing.T) {
	h := newHarness(t)
	h.start(nil)
	if r := h.http("POST", "/api/domains", []header{{"Content-Type", "application/json"}}, `{"text": "example.org"}`); r.status != 403 {
		t.Fatal(r.status)
	}
}

// --- Exact answers ------------------------------------------------------------------

func TestAnswersAreByteForByteLikePython(t *testing.T) {
	h := newHarness(t)
	h.start(nil)
	r := h.http("POST", "/api/totp/check", apiHeaders, "{not json")
	if r.status != 400 || string(r.body) != `{"ok": false, "error": "Expecting property name enclosed in double `+
		`quotes: line 1 column 2 (char 1)", "error_t": null}` {
		t.Fatal(r.status, string(r.body))
	}
	r = h.http("POST", "/api/auto-update", apiHeaders, `{"enabled": true}`)
	if r.status != 409 || string(r.body) != `{"ok": false, "error": "Finish the setup first", `+
		`"error_t": {"key": "setup.finish_first", "args": {}}}` {
		t.Fatal(r.status, string(r.body))
	}
	c, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(h.cfg.HTTPPort))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte("POST /api/connect HTTP/1.1\r\nHost: localhost\r\nX-Uni-VPN: 1\r\n\r\n"))
	data, _ := io.ReadAll(c)
	head, body, _ := strings.Cut(string(data), "\r\n\r\n")
	lines := strings.Split(head, "\r\n")
	want := []string{"HTTP/1.1 200 OK", "Content-Type: application/json", "Content-Length: " + strconv.Itoa(len(body)),
		"Cache-Control: no-store", "Connection: close", "X-Content-Type-Options: nosniff", "X-Frame-Options: DENY"}
	if !slices.Equal(lines, want) || !strings.HasPrefix(body, `{"ok": true, "state": "`) {
		t.Fatalf("%q", data)
	}
}
