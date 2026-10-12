// Package fakelinotp is a stand-in for a LinOTP 3.x self service portal in tests: the
// /userservice calls uni-vpn makes, with LinOTP's session cookie, its session parameter and
// its replay protection.
package fakelinotp

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/DavidVinu/uni-vpn/internal/totp"
)

// Portal is the fake. Users sign in with Password and, when they have a token, Code.
type Portal struct {
	sync.Mutex
	*httptest.Server

	User, Password string
	// Code is the current code of the user's existing token; "" means no token, so the
	// password alone signs in.
	Code string
	// NoVerify: the portal does not offer the "Testen" step (policy).
	NoVerify bool
	// RefuseEnroll is the error message of a policy that allows no enrollment.
	RefuseEnroll string
	// NoTokenList: the portal does not offer usertokenlist.
	NoTokenList bool
	// Skew is how far the portal's clock is ahead of this computer's.
	Skew time.Duration

	sessions map[string]string // cookie value -> state
	used     map[string]bool   // codes used, per token
	// Tokens are the enrolled tokens: serial -> otpauth secret ("base32:...").
	Tokens   map[string]string
	Verified map[string]bool
	Deleted  []string
	LoggedIn int
	// Challenged counts sign-ins that chose the token first, like the portal's page.
	Challenged int
	Calls      []string
	// Descriptions are what the enrollments named the tokens: serial -> description.
	Descriptions map[string]string
}

// Start runs the fake over TLS; use Server.Client() for its certificate.
func Start() *Portal {
	p := &Portal{User: "ab123", Password: "pw", Code: "111111", sessions: map[string]string{},
		used: map[string]bool{}, Tokens: map[string]string{}, Descriptions: map[string]string{}, Verified: map[string]bool{}}
	p.Server = httptest.NewTLSServer(http.HandlerFunc(p.serve))
	return p
}

func newSession() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func reply(w http.ResponseWriter, value any, detail map[string]any) {
	body := map[string]any{"jsonrpc": "2.0802", "result": map[string]any{"status": true, "value": value}, "id": 1}
	if len(detail) > 0 {
		body["detail"] = detail
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(body)
}

func refuse(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"status": false,
		"error": map[string]any{"code": code, "message": message}}})
}

// setState gives the session a new cookie value, like LinOTP at each sign-in step.
func (p *Portal) setState(w http.ResponseWriter, old, state string) {
	delete(p.sessions, old)
	value := newSession()
	p.sessions[value] = state
	http.SetCookie(w, &http.Cookie{Name: "user_selfservice", Value: value, Path: "/", Secure: true})
}

func (p *Portal) serve(w http.ResponseWriter, r *http.Request) {
	p.Lock()
	defer p.Unlock()
	action, ok := strings.CutPrefix(r.URL.Path, "/userservice/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	p.Calls = append(p.Calls, action)
	params := map[string]string{}
	if r.Method == http.MethodPost {
		if r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "json only", http.StatusBadRequest)
			return
		}
		var raw map[string]any
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		for k, v := range raw {
			s, ok := v.(string)
			if !ok {
				http.Error(w, "text only", http.StatusBadRequest)
				return
			}
			params[k] = s
		}
	}
	cookie, _ := r.Cookie("user_selfservice")
	cookieValue := ""
	if cookie != nil {
		cookieValue = cookie.Value
	}
	if action == "logout" {
		delete(p.sessions, cookieValue)
		http.SetCookie(w, &http.Cookie{Name: "user_selfservice", Value: "", Path: "/", MaxAge: -1})
		reply(w, true, nil)
		return
	}
	state := ""
	if params["session"] != "" {
		if params["session"] != cookieValue || p.sessions[cookieValue] == "" {
			http.Error(w, `{"result":{"status":false,"error":{"code":-1,"message":"No valid session"}}}`, http.StatusUnauthorized)
			return
		}
		state = p.sessions[cookieValue]
	} else if action != "login" {
		http.Error(w, `{"result":{"status":false,"error":{"code":-1,"message":"No valid session"}}}`, http.StatusUnauthorized)
		return
	}
	switch action {
	case "login":
		p.login(w, params, state, cookieValue)
		return
	}
	if state != "authenticated" {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	switch action {
	case "enroll":
		if p.RefuseEnroll != "" {
			refuse(w, 411, p.RefuseEnroll)
			return
		}
		if params["type"] != "totp" {
			refuse(w, -311, "unsupported type")
			return
		}
		key := make([]byte, 20)
		rand.Read(key)
		secret := strings.TrimRight(base32.StdEncoding.EncodeToString(key), "=")
		serial := "TOTP" + strings.ToUpper(hex.EncodeToString(key[:4]))
		p.Tokens[serial] = "base32:" + secret
		p.Descriptions[serial] = params["description"]
		reply(w, true, map[string]any{"serial": serial,
			"otpkey":    map[string]any{"value": "seed://" + hex.EncodeToString(key)},
			"googleurl": map[string]any{"order": "0", "description": "OTPAuth Url", "value": "otpauth://totp/LinOTP:" + serial + "?secret=" + secret + "&issuer=LinOTP"}})
	case "verify":
		if p.NoVerify {
			refuse(w, -311, "policy: verify not allowed")
			return
		}
		token, ok := p.Tokens[params["serial"]]
		if !ok {
			refuse(w, -311, "no token")
			return
		}
		otp, has := params["otp"]
		if !has {
			reply(w, false, map[string]any{"message": "Please enter your otp", "replyMode": []string{"offline"}})
			return
		}
		want, _ := totp.CodeAt(token, time.Now().Add(p.Skew))
		replay := p.used[params["serial"]+"/"+otp]
		p.used[params["serial"]+"/"+otp] = true
		ok = otp == want && !replay
		if ok {
			p.Verified[params["serial"]] = true
		}
		reply(w, ok, nil)
	case "usertokenlist":
		if p.NoTokenList {
			http.NotFound(w, r)
			return
		}
		list := []any{}
		for serial := range p.Tokens {
			list = append(list, map[string]any{"LinOtp.TokenSerialnumber": serial, "LinOtp.TokenDesc": p.Descriptions[serial],
				"LinOtp.TokenType": "TOTP", "LinOtp.Isactive": true})
		}
		reply(w, list, nil)
	case "delete":
		delete(p.Tokens, params["serial"])
		p.Deleted = append(p.Deleted, params["serial"])
		reply(w, true, nil)
	default:
		http.NotFound(w, r)
	}
}

func (p *Portal) login(w http.ResponseWriter, params map[string]string, state, cookie string) {
	switch {
	case state == "" && params["username"] != "":
		if params["username"] != p.User || params["password"] != p.Password {
			reply(w, false, nil)
			return
		}
		if p.Code == "" {
			p.setState(w, cookie, "authenticated")
			p.LoggedIn++
			reply(w, true, nil)
			return
		}
		p.setState(w, cookie, "credentials_verified")
		reply(w, false, map[string]any{"message": "credential verified - additional authentication parameter required",
			"tokenList": []any{map[string]any{"LinOtp.TokenSerialnumber": "TOTP0001", "LinOtp.TokenType": "TOTP",
				"LinOtp.Isactive": true}}})
	case state == "credentials_verified" && params["serial"] != "":
		if params["serial"] != "TOTP0001" {
			reply(w, false, nil)
			return
		}
		p.Challenged++
		p.setState(w, cookie, "challenge_triggered")
		reply(w, false, map[string]any{"transactionId": "1234", "message": "Please enter your otp", "linotp_tokentype": "totp"})
	case (state == "credentials_verified" || state == "challenge_triggered") && params["otp"] != "":
		ok := params["otp"] == p.Code && !p.used["existing/"+params["otp"]]
		p.used["existing/"+params["otp"]] = true
		if !ok {
			reply(w, false, nil)
			return
		}
		p.setState(w, cookie, "authenticated")
		p.LoggedIn++
		reply(w, true, nil)
	default:
		reply(w, false, nil)
	}
}
