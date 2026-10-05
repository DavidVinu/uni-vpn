// Package detect probes a Cisco AnyConnect gateway without credentials: group list, login
// fields, SAML. It mirrors uni_vpn/detect.py.
//
// It sends the XML init request openconnect sends first and reads the login form the server
// answers with. Nothing that identifies the user is sent. The result prefills the setup
// assistant for a university that is not in universities.json.
//
// Measured on 2026-10-05 against eleven gateways: every server names
// <auth-method>single-sign-on-v2</auth-method> in <opaque> once the client advertises SSO, even
// with a password form, so only <sso-v2-login> or an "sso" input means SAML. Load balancers
// answer the POST with a 302 to a cluster member, which must get the same POST again.
package detect

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	MaxReply     = 256 * 1024
	MaxRedirects = 3
	NotCisco     = "This address does not answer like a Cisco AnyConnect gateway"
	// DefaultTimeout is the socket timeout: it bounds connect and every single read or write,
	// not the whole probe, as Python's socket timeout does.
	DefaultTimeout = 10 * time.Second
	// maxRepeats is urllib's HTTPRedirectHandler.max_repeats: visits of one and the same URL.
	maxRepeats = 4
)

// Field is a text or password input of the login form.
type Field struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Label string `json:"label"`
}

// Suggestion holds profile values for the setup assistant.
type Suggestion struct {
	Host      string `json:"host"`
	Usergroup string `json:"usergroup"`
	Authgroup string `json:"authgroup"`
	MFA       string `json:"mfa"`
}

// Detection is what the gateway revealed. Its JSON matches Python's Detection.as_dict().
type Detection struct {
	Host      string
	Usergroup string
	Reachable bool
	Cisco     bool
	SAML      bool
	Groups    []string
	// Group is the group this form belongs to: the selected option, else the group alias.
	Group          string
	Fields         []Field
	SecondPassword bool
	Message        string
	Error          string
}

// Suggestion returns profile values for the setup assistant. The second factor is a guess: a
// second password field is usually TOTP, but without one the server may still want an appended
// code, Duo, or show the OTP field only after the password (Heidelberg).
func (d Detection) Suggestion() Suggestion {
	mfa := "none"
	if d.SAML {
		mfa = "saml"
	} else if d.SecondPassword {
		mfa = "totp_field"
	}
	authgroup := ""
	if len(d.Groups) > 1 {
		authgroup = d.Group
	}
	return Suggestion{Host: d.Host, Usergroup: d.Usergroup, Authgroup: authgroup, MFA: mfa}
}

// MarshalJSON writes the keys of as_dict() in its order; empty lists stay [] rather than null.
func (d Detection) MarshalJSON() ([]byte, error) {
	groups, fields := d.Groups, d.Fields
	if groups == nil {
		groups = []string{}
	}
	if fields == nil {
		fields = []Field{}
	}
	return json.Marshal(struct {
		Host           string     `json:"host"`
		Usergroup      string     `json:"usergroup"`
		Reachable      bool       `json:"reachable"`
		Cisco          bool       `json:"cisco"`
		SAML           bool       `json:"saml"`
		Groups         []string   `json:"groups"`
		Group          string     `json:"group"`
		Fields         []Field    `json:"fields"`
		SecondPassword bool       `json:"second_password"`
		Message        string     `json:"message"`
		Error          string     `json:"error"`
		Suggestion     Suggestion `json:"suggestion"`
	}{d.Host, d.Usergroup, d.Reachable, d.Cisco, d.SAML, groups, d.Group, fields,
		d.SecondPassword, d.Message, d.Error, d.Suggestion()})
}

// SplitAddress turns "https://vpn.example.edu/staff" into ("vpn.example.edu", "staff").
// The error is a *FieldError.
func SplitAddress(text string) (host, usergroup string, err error) {
	value := pyStrip(text)
	// Both prefixes in turn, as Python's loop does: "https://http://x" ends up as "x".
	for _, prefix := range []string{"https://", "http://"} {
		if len(value) >= len(prefix) && asciiLower(value[:len(prefix)]) == prefix {
			value = value[len(prefix):]
		}
	}
	host, path, _ := strings.Cut(value, "/")
	if host, err = checkHost(host); err != nil {
		return "", "", err
	}
	if usergroup, err = checkUsergroup(path); err != nil {
		return "", "", err
	}
	return host, usergroup, nil
}

func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// xmlEscape mirrors xml.sax.saxutils.escape: only &, < and >.
var xmlEscape = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace

// InitRequest is the body openconnect sends first.
func InitRequest(host, usergroup, group string) []byte {
	groupSelect := ""
	if group != "" {
		groupSelect = "<group-select>" + xmlEscape(group) + "</group-select>"
	}
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>` + "\n" +
		`<config-auth client="vpn" type="init" aggregate-auth-version="2">` +
		`<version who="vpn">v9.12</version><device-id>linux-64</device-id>` +
		groupSelect + `<group-access>` + xmlEscape("https://"+host+"/"+usergroup) + `</group-access>` +
		`<capabilities><auth-method>single-sign-on-v2</auth-method></capabilities></config-auth>`)
}

// ParseReply reads the gateway's answer to the init request.
func ParseReply(data []byte, host, usergroup string) Detection {
	result := Detection{Host: host, Usergroup: usergroup, Reachable: true}
	// No DTDs: a gateway never sends one, and it is the way to entity expansion attacks.
	head := data
	if len(head) > 4096 {
		head = head[:4096]
	}
	if len(data) > MaxReply || strings.Contains(asciiUpper(head), "<!DOCTYPE") {
		result.Error = NotCisco
		return result
	}
	root, err := parseXML(data)
	if err != nil || !root.is("config-auth") {
		result.Error = NotCisco
		return result
	}
	result.Cisco = true
	auth := root.find("auth")
	if auth == nil {
		result.Error = "The gateway sent no login form"
		return result
	}
	result.Message = pyStrip(auth.findText("message"))
	result.SAML = auth.find("sso-v2-login") != nil || auth.iter("input", func(i *element) bool {
		t, _ := i.get("type")
		return t == "sso"
	})
	if form := auth.find("form"); form != nil {
		for _, item := range form.findAll("input") {
			if t, _ := item.get("type"); t == "text" || t == "password" {
				name, _ := item.get("name")
				lbl, _ := item.get("label")
				result.Fields = append(result.Fields, Field{Name: name, Type: t, Label: lbl})
			}
		}
		for _, sel := range form.findAll("select") {
			if name, ok := sel.get("name"); !ok || name != "group_list" {
				continue
			}
			for _, option := range sel.findAll("option") {
				name := pyStrip(option.text)
				if name == "" {
					continue
				}
				result.Groups = append(result.Groups, name)
				if s, _ := option.get("selected"); s == "true" {
					result.Group = name
				}
			}
		}
	}
	if result.Group == "" {
		result.Group = pyStrip(root.findText("opaque", "group-alias"))
	}
	for _, f := range result.Fields {
		if f.Name == "secondary_password" {
			result.SecondPassword = true
		}
	}
	return result
}

// asciiUpper mirrors bytes.upper, which leaves non-ASCII bytes alone.
func asciiUpper(b []byte) string {
	out := make([]byte, len(b))
	for i, c := range b {
		if 'a' <= c && c <= 'z' {
			c -= 'a' - 'A'
		}
		out[i] = c
	}
	return string(out)
}

// Options tune Probe. The zero value matches Python's defaults.
type Options struct {
	UserAgent string        // default DefaultUserAgent
	Timeout   time.Duration // default DefaultTimeout
	RootCAs   *x509.CertPool
}

// httpError mirrors urllib.error.HTTPError: the gateway answered, but not with 2xx.
type httpError struct{ code int }

func (e *httpError) Error() string { return "HTTP " + strconv.Itoa(e.code) }

// Probe asks the gateway for its login form. The error is a *FieldError for an invalid
// address or group; network trouble ends up in Detection.Error.
func Probe(ctx context.Context, host, usergroup, group string, opts Options) (Detection, error) {
	var err error
	if host, err = checkHost(host); err != nil {
		return Detection{}, err
	}
	if usergroup, err = checkUsergroup(usergroup); err != nil {
		return Detection{}, err
	}
	if group, err = checkText("authgroup", group); err != nil {
		return Detection{}, err
	}
	if opts.UserAgent == "" {
		opts.UserAgent = DefaultUserAgent
	}
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	data, err := post(ctx, "https://"+host+"/"+usergroup, InitRequest(host, usergroup, group), opts)
	var he *httpError
	if errors.As(err, &he) {
		return Detection{Host: host, Usergroup: usergroup, Reachable: true,
			Error: fmt.Sprintf("%s (HTTP %d)", NotCisco, he.code)}, nil
	}
	if err != nil {
		return Detection{Host: host, Usergroup: usergroup,
			Error: fmt.Sprintf("Could not reach %s: %s", host, reason(err))}, nil
	}
	return ParseReply(data, host, usergroup), nil
}

// deadlineConn gives every read and write its own deadline, like a Python socket timeout.
type deadlineConn struct {
	net.Conn
	timeout time.Duration
}

func (c *deadlineConn) Read(b []byte) (int, error) {
	if err := c.Conn.SetReadDeadline(time.Now().Add(c.timeout)); err != nil {
		return 0, err
	}
	return c.Conn.Read(b)
}

func (c *deadlineConn) Write(b []byte) (int, error) {
	if err := c.Conn.SetWriteDeadline(time.Now().Add(c.timeout)); err != nil {
		return 0, err
	}
	return c.Conn.Write(b)
}

func newTransport(opts Options) *http.Transport {
	dialer := &net.Dialer{Timeout: opts.Timeout}
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true) // urllib speaks HTTP/1.1 only
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment, // urllib's ProxyHandler reads the same variables
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := dialer.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			return &deadlineConn{conn, opts.Timeout}, nil
		},
		// ssl.create_default_context: verified chain and host name, TLS 1.2 or newer.
		TLSClientConfig:    &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: opts.RootCAs},
		DisableCompression: true, // urllib sends Accept-Encoding: identity and never gunzips
		DisableKeepAlives:  true,
		Protocols:          protocols,
	}
}

// post sends the init request and follows redirects the way detect._RepostRedirects and
// urllib's HTTPRedirectHandler do: the same POST again, https only, at most MaxRedirects
// distinct URLs and maxRepeats visits of one URL. No cookies, as urllib keeps none.
func post(ctx context.Context, target string, body []byte, opts Options) ([]byte, error) {
	transport := newTransport(opts)
	defer transport.CloseIdleConnections()
	var visited map[string]int
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header = http.Header{
			"User-Agent":          {opts.UserAgent},
			"X-Transcend-Version": {"1"},
			"X-Aggregate-Auth":    {"1"},
			"X-Support-Http-Auth": {"true"},
			"Accept":              {"*/*"},
			"Content-Type":        {"application/x-www-form-urlencoded"},
			"Accept-Encoding":     {"identity"},
		}
		req.Close = true // urllib sends Connection: close
		resp, err := transport.RoundTrip(req)
		if err != nil {
			return nil, err
		}
		code := resp.StatusCode
		if code >= 200 && code < 300 {
			data, err := io.ReadAll(io.LimitReader(resp.Body, MaxReply+1))
			resp.Body.Close()
			return data, err
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, MaxReply))
		resp.Body.Close()
		next, ok := redirectTarget(resp)
		if !ok {
			return nil, &httpError{code}
		}
		if visited == nil {
			visited = map[string]int{}
		} else if visited[next] >= maxRepeats || len(visited) >= MaxRedirects {
			return nil, &httpError{code}
		}
		visited[next]++
		target = next
	}
}

// redirectTarget is the https URL a 301/302/303/307/308 points to.
func redirectTarget(resp *http.Response) (string, bool) {
	switch resp.StatusCode {
	case 301, 302, 303, 307, 308:
	default:
		return "", false
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		loc = resp.Header.Get("Uri")
	}
	if loc == "" {
		return "", false
	}
	u, err := resp.Request.URL.Parse(loc)
	if err != nil {
		return "", false
	}
	next := u.String()
	if !strings.HasPrefix(strings.ToLower(next), "https://") {
		return "", false
	}
	return next, true
}

// reason turns a network error into roughly what Python puts after "Could not reach host: ".
func reason(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	var ne net.Error
	var dns *net.DNSError
	switch {
	case errors.As(err, &ne) && ne.Timeout():
		return "timed out"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "Connection refused"
	case errors.As(err, &dns) && dns.IsNotFound:
		return "Name or service not known"
	}
	return err.Error()
}
