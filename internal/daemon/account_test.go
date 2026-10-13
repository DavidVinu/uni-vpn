//go:build !windows

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DavidVinu/uni-vpn/internal/config"
	"github.com/DavidVinu/uni-vpn/internal/credentials"
)

// Settings, Account: another university whose second factor the keyring then refuses. An
// attempt that read the keyring while the new user was set must not sign in to the old
// gateway with the new account's password once completeSetup put the old user back.
func TestFailedAccountChangeNeverSendsTheNewPasswordToTheOldGateway(t *testing.T) {
	h := newHarness(t)
	h.cfg.MFA = "none"
	h.cfg.DemandWindow = 30
	args := filepath.Join(t.TempDir(), "args")
	t.Setenv("FAKE_ARGS_FILE", args)
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := config.WriteInitial(path, "u", "heidelberg", nil); err != nil {
		t.Fatal(err)
	}
	var kmu sync.Mutex
	keyring := map[string]string{"u": "old-pw"}
	var d *Daemon
	reading := make(chan struct{})
	var readOnce sync.Once
	setupDone := make(chan struct{})
	d = h.start(func(o *Options) {
		o.ConfigPath = path
		o.PasswordGetter = func(ctx context.Context) ([]byte, error) {
			user := d.user() // like keyringArgs
			readOnce.Do(func() { close(reading) })
			<-setupDone // a slow keyring
			kmu.Lock()
			defer kmu.Unlock()
			pw, ok := keyring[user]
			if !ok {
				return nil, credentials.ErrPasswordMissing
			}
			return []byte(pw), nil
		}
		o.PasswordSetter = func(pw string) error {
			kmu.Lock()
			keyring[d.user()] = pw
			kmu.Unlock()
			// A browser tab of the old university asks for the tunnel just now.
			go d.RequestConnect()
			return nil
		}
		o.TOTPSetter = func(string) error {
			<-reading
			return &credentials.KeyringError{Msg: "keyring locked"}
		}
	})
	err := d.CompleteSetup("cd456", "new-pw", "base32:GEZDGNBVGY3TQOJQ", "heidelberg", nil)
	if err == nil {
		t.Fatal("setup did not fail")
	}
	close(setupDone)
	waitFor(3*time.Second, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.state == Connected || d.state.final()
	})
	data, _ := os.ReadFile(args)
	if lines := h.pwLines(); len(lines) > 0 && strings.Contains(strings.Join(lines, ","), "new-pw") {
		t.Fatalf("the new account's password went to the old gateway:\nargs: %s\npasswords: %v", data, lines)
	}
}

type testAccount struct {
	user, university, host, password, token string
	overrides                               []config.Setting
}

var setupAccounts = []testAccount{
	{"aa111", "other", "vpn.a.example", "pw-A", "", []config.Setting{{Key: "host", Value: "vpn.a.example"}, {Key: "mfa", Value: "none"}}},
	{"bb222", "other", "vpn.b.example", "pw-B", "", []config.Setting{{Key: "host", Value: "vpn.b.example"}, {Key: "mfa", Value: "none"}}},
	{"cc333", "heidelberg", "vpn-ac.uni-heidelberg.de", "pw-C", "base32:GEZDGNBVGY3TQOJQ", nil},
}

func setupAccount(user string) (testAccount, bool) {
	for _, a := range setupAccounts {
		if a.user == user {
			return a, true
		}
	}
	return testAccount{}, false
}

// Two setups at once (the user's own, and an account another device just changed to): the
// one that started first ends last. The account in use must still be one account: its user,
// its gateway, its password.
// A setup from Settings waiting on the keyring while the account from another device comes in:
// one account in the end, its gateway with its user.
func TestOverlappingSetupsKeepOneAccount(t *testing.T) {
	h := newHarness(t)
	a, b, c := setupAccounts[0], setupAccounts[1], setupAccounts[2]
	h.cfg.User, h.cfg.University, h.cfg.Host, h.cfg.MFA = a.user, a.university, a.host, "none"
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := config.WriteInitial(path, a.user, a.university, a.overrides); err != nil {
		t.Fatal(err)
	}
	var kmu sync.Mutex
	passwords := map[string]string{a.user: a.password}
	totps := map[string]string{}
	slow := make(chan struct{})
	var d *Daemon
	d = h.start(func(o *Options) {
		o.ConfigPath = path
		o.PasswordGetter = func(context.Context) ([]byte, error) {
			kmu.Lock()
			defer kmu.Unlock()
			return []byte(passwords[d.user()]), nil
		}
		o.TOTPGetter = func(context.Context) ([]byte, error) {
			kmu.Lock()
			defer kmu.Unlock()
			return []byte(totps[d.user()]), nil
		}
		o.PasswordSetter = func(pw string) error {
			user := d.user()
			if user == b.user {
				<-slow // a keyring that asks first
			}
			kmu.Lock()
			defer kmu.Unlock()
			passwords[user] = pw
			return nil
		}
		o.TOTPSetter = func(s string) error {
			kmu.Lock()
			defer kmu.Unlock()
			totps[d.user()] = s
			return nil
		}
	})
	first := make(chan error, 1)
	go func() { first <- d.CompleteSetup(b.user, b.password, b.token, b.university, b.overrides) }()
	time.Sleep(200 * time.Millisecond)
	time.AfterFunc(time.Second, func() { close(slow) })
	if err := d.CompleteSetup(c.user, c.password, c.token, c.university, c.overrides); err != nil {
		t.Fatal(err)
	}
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	user, host, switching := d.cfg.User, d.cfg.Host, d.switching
	d.mu.Unlock()
	data, _ := os.ReadFile(path)
	if acc, _ := setupAccount(user); acc.host != host || switching || !strings.Contains(string(data), `user = "`+user+`"`) {
		t.Fatalf("user %s host %s switching %v\n%s", user, host, switching, data)
	}
}
