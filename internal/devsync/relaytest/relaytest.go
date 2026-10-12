// Package relaytest is relay/worker.js in memory, for tests: the same routes, limits and
// answers.
package relaytest

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// The limits of relay/worker.js.
const (
	maxPairBlob     = 8 * 1024
	maxGroupBlob    = 256 * 1024
	maxNewGroupBlob = 64 * 1024
)

// Relay is the server's state; Groups counts what tests look at.
type Relay struct {
	mu     sync.Mutex
	pairs  map[string][]byte
	groups map[string]*fakeGroup
}

type fakeGroup struct {
	token   string
	version int64
	body    []byte
	final   bool
}

var hexID = regexp.MustCompile(`\A[0-9a-f]{32}\z`)

// Start serves a relay until the test ends and returns it with its base URL.
func Start(t testing.TB) (*Relay, string) {
	f := &Relay{pairs: map[string][]byte{}, groups: map[string]*fakeGroup{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv.URL
}

// Groups is the number of groups stored.
func (f *Relay) Groups() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.groups)
}

func (f *Relay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if len(parts) != 3 || parts[0] != "v1" || !hexID.MatchString(parts[2]) {
		http.Error(w, "not found", 404)
		return
	}
	id := parts[2]
	body, _ := io.ReadAll(r.Body)
	switch parts[1] {
	case "pair":
		switch r.Method {
		case "PUT":
			if len(body) == 0 || len(body) > maxPairBlob {
				http.Error(w, "too large", 413)
				return
			}
			if _, ok := f.pairs[id]; ok {
				http.Error(w, "exists", 409)
				return
			}
			f.pairs[id] = body
			w.WriteHeader(201)
		case "GET":
			b, ok := f.pairs[id]
			if !ok {
				http.Error(w, "not found", 404)
				return
			}
			delete(f.pairs, id)
			w.Write(b)
		default:
			http.Error(w, "method", 405)
		}
	case "group":
		auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		sum := sha256.Sum256([]byte(auth))
		token := hex.EncodeToString(sum[:])
		g := f.groups[id]
		if g != nil && g.token != token {
			http.Error(w, "forbidden", 403)
			return
		}
		switch r.Method {
		case "GET":
			if g == nil {
				http.Error(w, "not found", 404)
				return
			}
			w.Header().Set("ETag", `"`+strconv.FormatInt(g.version, 10)+`"`)
			w.Write(g.body)
		case "PUT":
			create := r.Header.Get("If-None-Match") == "*"
			if len(body) == 0 || len(body) > maxGroupBlob || create && len(body) > maxNewGroupBlob {
				http.Error(w, "too large", 413)
				return
			}
			if create {
				if g != nil {
					http.Error(w, "exists", 412)
					return
				}
				g = &fakeGroup{token: token}
				f.groups[id] = g
			} else {
				if g == nil {
					http.Error(w, "not found", 404)
					return
				}
				if r.Header.Get("If-Match") != `"`+strconv.FormatInt(g.version, 10)+`"` {
					http.Error(w, "changed", 412)
					return
				}
			}
			if g.final {
				http.Error(w, "gone", 410)
				return
			}
			g.version++
			g.body = body
			g.final = r.Header.Get("Sync-Final") == "1"
			w.Header().Set("ETag", `"`+strconv.FormatInt(g.version, 10)+`"`)
			w.WriteHeader(200)
		default:
			http.Error(w, "method", 405)
		}
	default:
		http.Error(w, "not found", 404)
	}
}
