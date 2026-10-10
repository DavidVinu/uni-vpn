// Package httpapi is the status page and JSON API on 127.0.0.1:<http_port>. It mirrors
// uni_vpn/httpapi.py: same routes, status codes, headers, guards and message texts. Like the
// Python core it speaks just enough HTTP/1.1 itself (one request per connection) instead of
// using net/http, so the answers stay byte for byte the same.
package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/DavidVinu/uni-vpn/internal/config"
	"github.com/DavidVinu/uni-vpn/internal/detect"
	"github.com/DavidVinu/uni-vpn/internal/i18n"
	"github.com/DavidVinu/uni-vpn/internal/pyjson"
	"github.com/DavidVinu/uni-vpn/internal/totp"
	unis "github.com/DavidVinu/uni-vpn/internal/universities"
	assets "github.com/DavidVinu/uni-vpn/uni_vpn"
)

const (
	MaxHeader = 16 * 1024
	MaxBody   = 64 * 1024
	// asyncio's StreamReader limit: readuntil gives up on a head longer than this.
	streamLimit = 64 * 1024
	ioTimeout   = 10 * time.Second
)

var contentLength = regexp.MustCompile(`\A[0-9]+\z`)

// ValueError is an input problem the daemon reports (Python's ValueError); its text goes to
// the page.
type ValueError struct {
	Msg  string
	Text *i18n.Text // the message as a catalog text, nil when it has none
}

func (e *ValueError) Error() string { return e.Msg }

// Unwrap hands the catalog text to i18n.Of.
func (e *ValueError) Unwrap() error {
	if e.Text == nil {
		return nil
	}
	return *e.Text
}

// TextValueError is a ValueError with a catalog text.
func TextValueError(text i18n.Text) *ValueError { return &ValueError{Msg: text.String(), Text: &text} }

// Daemon is what the API needs from the daemon.
type Daemon interface {
	Status() (pyjson.Object, error)
	PAC() (string, error)
	StateName() string
	RequestConnect()
	RequestDisconnect()
	StartRepair()
	SetPassword(password string) error
	SetDomains(text string) ([]string, error)
	SetAutoUpdate(enabled bool) error
	// SetLanguage: the app's language, "" follows the system. A *ValueError before setup.
	SetLanguage(code string) error
	SetTOTP(token string) error
	NoteCodeShown()
	// CompleteSetup: token "" means none. The error is a *unis.FieldError naming the step,
	// a *ValueError, or anything else (500).
	CompleteSetup(user, password, token, university string, overrides []config.Setting) error
}

// DetectFunc probes a gateway; tests replace it, there is no gateway in CI.
type DetectFunc func(ctx context.Context, host, usergroup, group string) (detect.Detection, error)

// Server serves the API.
type Server struct {
	Host     string
	Port     int
	Detector DetectFunc

	daemon Daemon
	log    *slog.Logger
	ln     net.Listener
	wg     sync.WaitGroup
	mu     sync.Mutex
	conns  map[net.Conn]struct{}
}

// New returns a server; Start listens.
func New(daemon Daemon, host string, port int, log *slog.Logger) *Server {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Server{Host: host, Port: port, daemon: daemon, log: log, conns: map[net.Conn]struct{}{},
		Detector: func(ctx context.Context, host, usergroup, group string) (detect.Detection, error) {
			return detect.Probe(ctx, host, usergroup, group, detect.Options{})
		}}
}

// AllowedOrigin: "null" (sandboxed iframe, data: page) is a foreign origin, not a missing one.
// present is false when there is no Origin header (Python: None).
func AllowedOrigin(origin string, present bool, port int) bool {
	if !present || origin == "" {
		return true
	}
	return origin == fmt.Sprintf("http://127.0.0.1:%d", port) || origin == fmt.Sprintf("http://localhost:%d", port)
}

// AllowedHost protects against DNS rebinding: loopback names only, otherwise a foreign page
// reads same-origin.
func AllowedHost(host string, present bool, port int) bool {
	if !present {
		return false
	}
	p := strconv.Itoa(port)
	return host == "127.0.0.1" || host == "127.0.0.1:"+p || host == "localhost" || host == "localhost:"+p
}

// Start listens on Host:Port.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", net.JoinHostPort(s.Host, strconv.Itoa(s.Port)))
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()
	s.wg.Add(1)
	go s.serve(ln)
	return nil
}

// Addr is the listening address, nil before Start.
func (s *Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return nil
	}
	return s.ln.Addr()
}

// Stop closes the listener and waits up to 5 s for the requests in progress.
func (s *Server) Stop() {
	s.mu.Lock()
	ln := s.ln
	s.mu.Unlock()
	if ln == nil {
		return
	}
	ln.Close()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		s.log.Warn("HTTP: connections not closed in time")
		s.mu.Lock()
		for c := range s.conns {
			c.Close()
		}
		s.mu.Unlock()
	}
}

func (s *Server) serve(ln net.Listener) {
	defer s.wg.Done()
	for {
		c, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			time.Sleep(time.Second) // like asyncio when out of file descriptors
			continue
		}
		s.mu.Lock()
		s.conns[c] = struct{}{}
		s.mu.Unlock()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() {
				s.mu.Lock()
				delete(s.conns, c)
				s.mu.Unlock()
				c.Close()
			}()
			s.handle(c)
		}()
	}
}

// readHead is readuntil(b"\r\n\r\n") with asyncio's buffer limit.
func readHead(r *bufio.Reader) ([]byte, bool) {
	var head []byte
	for {
		b, err := r.ReadByte()
		if err != nil {
			return nil, false
		}
		head = append(head, b)
		if bytes.HasSuffix(head, []byte("\r\n\r\n")) {
			return head, true
		}
		if len(head) > streamLimit+3 {
			return nil, false
		}
	}
}

// latin1 is bytes.decode("latin-1").
func latin1(b []byte) string {
	runes := make([]rune, len(b))
	for i, c := range b {
		runes[i] = rune(c)
	}
	return string(runes)
}

type headers map[string]string

func (h headers) get(key string) (string, bool) {
	v, ok := h[key]
	return v, ok
}

func (s *Server) handle(c net.Conn) {
	r := bufio.NewReader(c)
	c.SetReadDeadline(time.Now().Add(ioTimeout))
	head, ok := readHead(r)
	if !ok || len(head) > MaxHeader {
		return
	}
	lines := strings.Split(latin1(head), "\r\n")
	parts := strings.SplitN(lines[0], " ", 3)
	if len(parts) != 3 {
		return
	}
	method, target := parts[0], parts[1]
	hdrs := headers{}
	for _, line := range lines[1:] {
		if key, value, found := strings.Cut(line, ": "); found {
			hdrs[strings.ToLower(key)] = value
		}
	}
	raw, ok := hdrs.get("content-length")
	if !ok {
		raw = "0"
	}
	raw = PyStrip(raw)
	if raw == "" {
		raw = "0"
	}
	if !contentLength.MatchString(raw) {
		s.respond(c, 400, "text/plain", []byte("invalid Content-Length"), hdrs)
		return
	}
	trimmed := strings.TrimLeft(raw, "0")
	length, err := strconv.Atoi(trimmed)
	if trimmed == "" {
		length, err = 0, nil
	}
	if err != nil || length > MaxBody {
		s.respond(c, 413, "text/plain", []byte("too large"), hdrs)
		return
	}
	body := []byte{}
	if length > 0 {
		c.SetReadDeadline(time.Now().Add(ioTimeout))
		body = make([]byte, length)
		if _, err := io.ReadFull(r, body); err != nil {
			return
		}
	}
	c.SetReadDeadline(time.Time{})
	path, query, _ := strings.Cut(target, "?")
	host, present := hdrs.get("host")
	if !AllowedHost(host, present, s.Port) {
		s.respond(c, 403, "text/plain", []byte("forbidden"), hdrs)
		return
	}
	status, ctype, payload := s.routeSafe(method, path, query, hdrs, body)
	s.respond(c, status, ctype, payload, hdrs)
}

var reasons = map[int]string{200: "OK", 204: "No Content", 400: "Bad Request", 403: "Forbidden", 404: "Not Found",
	405: "Method Not Allowed", 413: "Payload Too Large", 500: "Internal Server Error"}

func (s *Server) respond(c net.Conn, status int, ctype string, payload []byte, req headers) {
	reason, ok := reasons[status]
	if !ok {
		reason = "Status"
	}
	contentType := "Content-Type: " + ctype
	if strings.HasPrefix(ctype, "text/") {
		contentType += "; charset=utf-8"
	}
	lines := []string{fmt.Sprintf("HTTP/1.1 %d %s", status, reason), contentType,
		fmt.Sprintf("Content-Length: %d", len(payload)), "Cache-Control: no-store", "Connection: close",
		"X-Content-Type-Options: nosniff", "X-Frame-Options: DENY"}
	if origin, present := req.get("origin"); origin != "" && AllowedOrigin(origin, present, s.Port) {
		lines = append(lines, "Access-Control-Allow-Origin: "+origin, "Vary: Origin",
			"Access-Control-Allow-Methods: GET, POST, OPTIONS",
			"Access-Control-Allow-Headers: X-Uni-VPN, Content-Type")
	}
	out := append([]byte(strings.Join(lines, "\r\n")+"\r\n\r\n"), payload...)
	c.SetWriteDeadline(time.Now().Add(ioTimeout))
	c.Write(out)
}

func (s *Server) routeSafe(method, path, query string, hdrs headers, body []byte) (status int, ctype string, payload []byte) {
	defer func() {
		if r := recover(); r != nil {
			s.log.Error(fmt.Sprintf("HTTP error: %v", r))
			status, ctype, payload = 500, "text/plain", []byte("internal error")
		}
	}()
	status, ctype, payload, err := s.route(method, path, query, hdrs, body)
	if err != nil {
		// The status page must never die.
		s.log.Error(fmt.Sprintf("HTTP error: %s", err))
		return 500, "text/plain", []byte("internal error")
	}
	return status, ctype, payload
}

func jsonReply(status int, v any) (int, string, []byte, error) {
	return status, "application/json", pyjson.Marshal(v), nil
}

func text(status int, msg string) (int, string, []byte, error) {
	return status, "text/plain", []byte(msg), nil
}

// problem is an error for the app: the English text, plus key and arguments (error_t) when it
// can be translated. extra are name, value pairs that go before them.
func problem(status int, err error, extra ...any) (int, string, []byte, error) {
	payload := append(pyjson.O("ok", false), pyjson.O(extra...)...)
	return jsonReply(status, append(payload, pyjson.O("error", err.Error(), "error_t", i18n.Of(err))...))
}

// loads is json.loads(body.decode("utf-8")); utf8 is false for a UnicodeDecodeError.
func loads(body []byte) (v any, utf8OK bool, err error) {
	if !utf8.Valid(body) {
		return nil, false, nil
	}
	v, err = pyjson.Unmarshal(body)
	return v, true, err
}

// item is data[key] in Python: ok is false for a KeyError or TypeError.
func item(data any, key string) (any, bool) {
	obj, ok := data.(pyjson.Object)
	if !ok {
		return nil, false
	}
	return obj.Get(key)
}

func (s *Server) route(method, path, query string, hdrs headers, body []byte) (int, string, []byte, error) {
	if method == "OPTIONS" {
		return 204, "text/plain", nil, nil
	}
	if method == "GET" {
		switch path {
		case "/":
			return 200, "text/html", assets.IndexHTML, nil
		case "/status.json":
			status, err := s.daemon.Status()
			if err != nil {
				return 0, "", nil, err
			}
			// The native app's menus ask with their system languages: ?menu=de-DE,en
			values, _ := url.ParseQuery(query)
			if wanted := values.Get("menu"); wanted != "" {
				language, _ := status.Get("language")
				setting, _ := language.(string)
				status = append(status, pyjson.Member{Key: "menu", Value: i18n.Menu(i18n.Negotiate(setting, wanted))})
			}
			return jsonReply(200, status)
		case "/locales.json":
			return jsonReply(200, i18n.Catalogs())
		case "/proxy.pac":
			text, err := s.daemon.PAC()
			if err != nil {
				return 0, "", nil, err
			}
			return 200, "application/x-ns-proxy-autoconfig", []byte(text), nil
		case "/universities.json":
			return jsonReply(200, pyjson.O("default", unis.DefaultID, "universities", unis.PublicList()))
		}
		return text(404, "not found")
	}
	if method != "POST" {
		return text(405, "method not allowed")
	}
	origin, present := hdrs.get("origin")
	if v, _ := hdrs.get("x-uni-vpn"); v != "1" || !AllowedOrigin(origin, present, s.Port) {
		return text(403, "forbidden")
	}
	d := s.daemon
	switch path {
	case "/api/connect":
		d.RequestConnect()
	case "/api/disconnect":
		d.RequestDisconnect()
	case "/api/repair":
		d.StartRepair()
	case "/api/password":
		return s.password(body)
	case "/api/domains":
		return s.domains(body)
	case "/api/auto-update":
		return s.autoUpdate(body)
	case "/api/language":
		return s.language(body)
	case "/api/totp":
		return s.totp(body)
	case "/api/totp/check":
		return s.totpCheck(body)
	case "/api/detect":
		return s.detect(body)
	case "/api/setup":
		return s.setup(body)
	default:
		return text(404, "not found")
	}
	return jsonReply(200, pyjson.O("ok", true, "state", d.StateName()))
}

func (s *Server) password(body []byte) (int, string, []byte, error) {
	data, utf8OK, err := loads(body)
	value, ok := item(data, "password")
	if !utf8OK || err != nil || !ok {
		return text(400, "expected JSON with 'password'")
	}
	password, isStr := value.(string)
	if !isStr || password == "" {
		return text(400, "password empty")
	}
	if strings.ContainsAny(password, "\n\r") {
		return problem(400, i18n.T("password.line_break"))
	}
	if err := s.daemon.SetPassword(password); err != nil {
		return problem(500, err) // the error text goes to the page
	}
	return jsonReply(200, pyjson.O("ok", true, "state", s.daemon.StateName()))
}

func (s *Server) domains(body []byte) (int, string, []byte, error) {
	data, utf8OK, err := loads(body)
	value, ok := item(data, "text")
	if !utf8OK || err != nil || !ok {
		return text(400, "expected JSON with 'text'")
	}
	t, isStr := value.(string)
	if !isStr {
		return text(400, "domains must be text")
	}
	domains, err := s.daemon.SetDomains(t)
	if err != nil {
		var ve *ValueError
		if errors.As(err, &ve) {
			return problem(400, ve)
		}
		return problem(500, err)
	}
	return jsonReply(200, pyjson.O("ok", true, "domains", domains))
}

func (s *Server) autoUpdate(body []byte) (int, string, []byte, error) {
	data, utf8OK, err := loads(body)
	value, ok := item(data, "enabled")
	if !utf8OK || err != nil || !ok {
		return text(400, "expected JSON with 'enabled'")
	}
	enabled, isBool := value.(bool)
	if !isBool {
		return text(400, "enabled must be true or false")
	}
	if err := s.daemon.SetAutoUpdate(enabled); err != nil {
		var ve *ValueError
		if errors.As(err, &ve) {
			return problem(409, ve)
		}
		return problem(500, err)
	}
	return jsonReply(200, pyjson.O("ok", true, "enabled", enabled))
}

func (s *Server) language(body []byte) (int, string, []byte, error) {
	data, utf8OK, err := loads(body)
	value, ok := item(data, "language")
	if !utf8OK || err != nil || !ok {
		return text(400, "expected JSON with 'language'")
	}
	language, isStr := value.(string)
	if !isStr || !i18n.Valid(language) {
		return text(400, "unknown language")
	}
	if err := s.daemon.SetLanguage(language); err != nil {
		var ve *ValueError
		if errors.As(err, &ve) {
			return problem(409, ve)
		}
		return problem(500, err)
	}
	return jsonReply(200, pyjson.O("ok", true, "language", language))
}

func (s *Server) totp(body []byte) (int, string, []byte, error) {
	data, utf8OK, err := loads(body)
	value, ok := item(data, "secret")
	if !utf8OK || err != nil || !ok {
		return text(400, "expected JSON with 'secret'")
	}
	secret, isStr := value.(string)
	if !isStr {
		return text(400, "secret must be text")
	}
	token, err := totp.Normalize(secret)
	if err != nil {
		return problem(400, err)
	}
	if err := s.daemon.SetTOTP(token); err != nil {
		return problem(500, err)
	}
	code, err := totp.Code(token)
	if err != nil {
		return 0, "", nil, err
	}
	return jsonReply(200, pyjson.O("ok", true, "state", s.daemon.StateName(), "code", code))
}

// totpCheck: the check code for a secret without storing it. The setup assistant shows it for
// the portal's "Testen" step before anything is saved.
func (s *Server) totpCheck(body []byte) (int, string, []byte, error) {
	data, utf8OK, err := loads(body)
	if !utf8OK {
		return text(400, "expected JSON with 'secret'")
	}
	if err != nil {
		return problem(400, err) // a JSONDecodeError is a ValueError here
	}
	value, ok := item(data, "secret")
	if !ok {
		return text(400, "expected JSON with 'secret'")
	}
	secret, _ := value.(string)
	token, err := totp.Normalize(secret)
	if err != nil {
		return problem(400, err)
	}
	remaining := totp.Step - int(time.Now().Unix())%totp.Step
	s.daemon.NoteCodeShown()
	code, err := totp.Code(token)
	if err != nil {
		return 0, "", nil, err
	}
	return jsonReply(200, pyjson.O("ok", true, "code", code, "remaining", remaining))
}

// detect: "Not listed" in the setup assistant, what the gateway's login form looks like.
func (s *Server) detect(body []byte) (int, string, []byte, error) {
	data, utf8OK, err := loads(body)
	address, ok := item(data, "host")
	if !utf8OK || err != nil || !ok {
		return text(400, "expected JSON with 'host'")
	}
	group, found := item(data, "group")
	if !found {
		group = ""
	}
	addr, ok1 := address.(string)
	grp, ok2 := group.(string)
	if !ok1 || !ok2 {
		return text(400, "host and group must be text")
	}
	host, usergroup, err := detect.SplitAddress(addr)
	var result detect.Detection
	if err == nil {
		result, err = s.Detector(context.Background(), host, usergroup, grp)
	}
	if err != nil {
		var fe *detect.FieldError
		var ue *unis.FieldError
		switch {
		case errors.As(err, &fe):
			return problem(400, fe, "field", fe.Field)
		case errors.As(err, &ue):
			return problem(400, ue, "field", ue.Field)
		}
		return 0, "", nil, err
	}
	fields, err := pyjson.Unmarshal(pyjson.Marshal(result))
	if err != nil {
		return 0, "", nil, err
	}
	return jsonReply(200, append(pyjson.O("ok", true), fields.(pyjson.Object)...))
}

// setup: errors name the step that has to change, so the assistant can go back to it.
func (s *Server) setup(body []byte) (int, string, []byte, error) {
	fail := func(status int, field any, err error) (int, string, []byte, error) {
		return problem(status, err, "field", field)
	}
	data, utf8OK, err := loads(body)
	obj, isObj := data.(pyjson.Object)
	userV, ok1 := obj.Get("user")
	passwordV, ok2 := obj.Get("password")
	if !utf8OK || err != nil || !isObj || !ok1 || !ok2 {
		return fail(400, nil, errors.New("expected JSON with 'user' and 'password'"))
	}
	get := func(key string, def any) any {
		if v, ok := obj.Get(key); ok {
			return v
		}
		return def
	}
	secretV := get("secret", "")
	universityV := get("university", unis.DefaultID)
	overridesV := get("profile", nil)
	if !pyjson.Truthy(overridesV) {
		overridesV = pyjson.Object{}
	}
	user, okU := userV.(string)
	password, okP := passwordV.(string)
	secret, okS := secretV.(string)
	university, okN := universityV.(string)
	overrides, okO := overridesV.(pyjson.Object)
	if !okU || !okP || !okS || !okN || !okO {
		return fail(400, nil, errors.New("user, password, secret and university must be text, profile an object"))
	}
	if !config.ValidUser(PyStrip(user)) {
		return fail(400, "user", i18n.T("setup.invalid_user"))
	}
	if password == "" || strings.ContainsAny(password, "\n\r") {
		return fail(400, "password", i18n.T("setup.enter_password"))
	}
	token := ""
	if PyStrip(secret) != "" {
		token, err = totp.Normalize(secret)
		if err != nil {
			return fail(400, "totp", err)
		}
	}
	settings := make([]config.Setting, len(overrides))
	for i, m := range overrides {
		settings[i] = config.Setting{Key: m.Key, Value: m.Value}
	}
	if err := s.daemon.CompleteSetup(user, password, token, university, settings); err != nil {
		var ue *unis.FieldError
		var ve *ValueError
		switch {
		case errors.As(err, &ue):
			return fail(400, ue.Field, ue)
		case errors.As(err, &ve):
			return fail(400, "user", ve)
		}
		return fail(500, nil, err)
	}
	var code any
	if token != "" {
		c, err := totp.Code(token)
		if err != nil {
			return 0, "", nil, err
		}
		code = c
	}
	return jsonReply(200, pyjson.O("ok", true, "state", s.daemon.StateName(), "code", code))
}

// PyStrip is Python's str.strip(): Unicode whitespace including the separators \x1c-\x1f.
func PyStrip(s string) string { return strings.TrimFunc(s, PyIsSpace) }

// PyIsSpace is str.isspace() for one character.
func PyIsSpace(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\v', '\f', '\r', 0x1c, 0x1d, 0x1e, 0x1f, 0x85, 0xa0, 0x1680, 0x2028, 0x2029, 0x202f,
		0x205f, 0x3000:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}
