// Package octunnel drives libopenconnect in the core's own process (build tag libopenconnect),
// for iOS, which runs no child processes. It answers the login forms the way the openconnect
// program does with --passwd-on-stdin and --non-inter, and reports the same output lines
// (main.c wording), so the daemon classifies them exactly as it classifies the program's.
package octunnel

import (
	"fmt"
	"strings"
	"sync"
)

// Options are what the openconnect program gets on its command line and stdin.
type Options struct {
	Host, UserAgent, User, AuthGroup, UserGroup, OS string
	Password                                        []byte
	SecondPassword                                  string // "push" for duo_push, "" otherwise
	TOTPSecret                                      string // "base32:..." for totp_field, "" otherwise
	// NoExternalAuth offers no web sign-in, like the program's --no-external-auth. Only the
	// iPhone's library can be told (build-native.sh adds the call), as can AuthGroup's going
	// into the first request; elsewhere the login forms pick the group.
	NoExternalAuth   bool
	ReconnectTimeout int          // 60
	Line             func(string) // every progress and form line, like openconnect's stderr
	// Cancel, when closed, aborts a login in progress, like SIGTERM to the program.
	Cancel <-chan struct{}
}

// ExitError is a failed Connect with the exit code the openconnect program would have had.
type ExitError struct {
	Code   int
	Reason string
}

func (e *ExitError) Error() string { return fmt.Sprintf("openconnect: %s (code %d)", e.Reason, e.Code) }

// lineWriter splits the library's progress text into lines, as the program's stderr reader
// sees them; the text arrives in pieces that need not end at a line break.
type lineWriter struct {
	mu   sync.Mutex
	rest strings.Builder
	fn   func(string)
}

func (w *lineWriter) write(text string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for {
		i := strings.IndexByte(text, '\n')
		if i < 0 {
			w.rest.WriteString(text)
			return
		}
		w.rest.WriteString(text[:i])
		line := strings.TrimRight(w.rest.String(), "\r")
		w.rest.Reset()
		text = text[i+1:]
		if w.fn != nil {
			w.fn(line)
		}
	}
}

// flush emits an unfinished last line.
func (w *lineWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.rest.Len() > 0 && w.fn != nil {
		w.fn(w.rest.String())
	}
	w.rest.Reset()
}
