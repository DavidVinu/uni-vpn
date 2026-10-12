// Package linotp adds a code generator for uni-vpn to the user's account in a LinOTP self
// service portal (Heidelberg's MFA portal), the same calls the portal's own pages make for
// "Soft-Token (zeitbasiert)": sign in, enroll, test. API: LinOTP 3.x, /userservice/<action>.
package linotp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

// SessionCookie holds the portal session; every call after the sign-in repeats its value as
// the "session" parameter.
const SessionCookie = "user_selfservice"

var (
	// ErrPassword: the portal refused the user name or password.
	ErrPassword = errors.New("linotp: user name or password refused")
	// ErrCode: the one-time code was wrong or already used.
	ErrCode = errors.New("linotp: code refused")
)

// RefusedError is a call the portal answered with an error, for example a token limit or a
// policy that does not allow self enrollment.
type RefusedError struct {
	Action  string
	Message string
}

func (e *RefusedError) Error() string {
	return fmt.Sprintf("linotp: %s refused: %s", e.Action, e.Message)
}

// Client talks to one portal. It keeps the session between calls; use it from one goroutine.
type Client struct {
	base    *url.URL
	http    *http.Client
	session string
	tokens  []string // the active tokens the sign-in may ask a code of
}

// New is a client for the portal at base (https://mfa.example.edu/...; only the scheme and
// host count). dial nil means a direct connection.
func New(base string, dial func(ctx context.Context, network, address string) (net.Conn, error)) (*Client, error) {
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("linotp: bad portal address %q", base)
	}
	u = &url.URL{Scheme: u.Scheme, Host: u.Host, Path: "/"}
	jar, _ := cookiejar.New(nil)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	if dial != nil {
		transport.DialContext = dial
	}
	return &Client{base: u, http: &http.Client{Jar: jar, Transport: transport, Timeout: 30 * time.Second}}, nil
}

// SetHTTPClient replaces the HTTP client, keeping a cookie jar (tests).
func (c *Client) SetHTTPClient(h *http.Client) {
	if h.Jar == nil {
		h.Jar, _ = cookiejar.New(nil)
	}
	c.http = h
}

type reply struct {
	Result struct {
		Status bool            `json:"status"`
		Value  json.RawMessage `json:"value"`
		Error  *struct {
			Code    json.RawMessage `json:"code"`
			Message string          `json:"message"`
		} `json:"error"`
	} `json:"result"`
	Detail json.RawMessage `json:"detail"`
}

func (r *reply) True() bool { return string(r.Result.Value) == "true" }

// call posts params as JSON to /userservice/<action>, with the session when there is one.
func (c *Client) call(ctx context.Context, action string, params map[string]any) (*reply, error) {
	if params == nil {
		params = map[string]any{}
	}
	if c.session != "" {
		params["session"] = c.session
	}
	body, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	target := c.base.JoinPath("userservice", action)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	// The session cookie gets a new value at each sign-in step.
	for _, cookie := range c.http.Jar.Cookies(target) {
		if cookie.Name == SessionCookie {
			c.session = cookie.Value
		}
	}
	if res.StatusCode == http.StatusUnauthorized {
		return nil, &RefusedError{action, "no valid session"}
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("linotp: %s: HTTP %d", action, res.StatusCode)
	}
	var r reply
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("linotp: %s: unexpected answer", action)
	}
	if !r.Result.Status {
		message := "unknown error"
		if r.Result.Error != nil && r.Result.Error.Message != "" {
			message = r.Result.Error.Message
		}
		return nil, &RefusedError{action, message}
	}
	return &r, nil
}

// Login signs in with the user name and password. needsCode: the portal asks for a one-time
// code from one of the user's existing code generators next (Code).
func (c *Client) Login(ctx context.Context, user, password string) (needsCode bool, err error) {
	c.session = ""
	c.http.Jar, _ = cookiejar.New(nil) // no cookie of an earlier try counts as a session
	r, err := c.call(ctx, "login", map[string]any{"username": user, "password": password})
	if err != nil {
		return false, err
	}
	if r.True() {
		return false, nil
	}
	// A correct password opens a session that waits for the code; a wrong one opens none.
	if c.session == "" {
		return false, ErrPassword
	}
	var detail struct {
		TokenList []struct {
			Serial string `json:"LinOtp.TokenSerialnumber"`
			Active *bool  `json:"LinOtp.Isactive"`
		} `json:"tokenList"`
	}
	json.Unmarshal(r.Detail, &detail)
	c.tokens = nil
	for _, token := range detail.TokenList {
		if token.Serial != "" && (token.Active == nil || *token.Active) {
			c.tokens = append(c.tokens, token.Serial)
		}
	}
	return true, nil
}

// Code finishes the sign-in with a one-time code from any of the user's code generators.
func (c *Client) Code(ctx context.Context, otp string) error {
	// With one token, choose it like the portal's own page does: its challenge checks the
	// code alone, without the token's PIN. With several, the code is checked against all.
	if len(c.tokens) == 1 {
		if _, err := c.call(ctx, "login", map[string]any{"serial": c.tokens[0]}); err != nil {
			var refused *RefusedError
			if !errors.As(err, &refused) {
				return err
			}
		}
	}
	r, err := c.call(ctx, "login", map[string]any{"otp": otp})
	if err != nil {
		return err
	}
	if !r.True() {
		return ErrCode
	}
	return nil
}

// Enroll adds a time-based code generator with this description. It returns the token's
// serial number and its otpauth:// address, which carries the secret. The token works at once.
func (c *Client) Enroll(ctx context.Context, description string) (serial, otpauth string, err error) {
	r, err := c.call(ctx, "enroll", map[string]any{"type": "totp", "description": description})
	if err != nil {
		return "", "", err
	}
	var detail struct {
		Serial    string `json:"serial"`
		GoogleURL struct {
			Value string `json:"value"`
		} `json:"googleurl"`
	}
	if err := json.Unmarshal(r.Detail, &detail); err != nil || detail.Serial == "" ||
		!strings.HasPrefix(detail.GoogleURL.Value, "otpauth://") {
		return detail.Serial, "", &RefusedError{"enroll", "no otpauth address in the answer"}
	}
	return detail.Serial, detail.GoogleURL.Value, nil
}

// Verify tests the new token with a code from it, like the portal's "Testen" step. tested is
// false when the portal does not offer the test; the token works anyway.
func (c *Client) Verify(ctx context.Context, serial, otp string) (tested bool, err error) {
	if _, err := c.call(ctx, "verify", map[string]any{"serial": serial}); err != nil {
		var refused *RefusedError
		if errors.As(err, &refused) {
			return false, nil
		}
		return false, err
	}
	r, err := c.call(ctx, "verify", map[string]any{"serial": serial, "otp": otp})
	if err != nil {
		return false, err
	}
	if !r.True() {
		return true, ErrCode
	}
	return true, nil
}

// Delete removes a token again, after a failed setup.
func (c *Client) Delete(ctx context.Context, serial string) error {
	_, err := c.call(ctx, "delete", map[string]any{"serial": serial})
	return err
}

// Logout ends the session.
func (c *Client) Logout(ctx context.Context) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base.JoinPath("userservice", "logout").String(), nil)
	if err != nil {
		return
	}
	if res, err := c.http.Do(req); err == nil {
		res.Body.Close()
	}
	c.session = ""
}
