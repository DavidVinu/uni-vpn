//go:build !windows

package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DavidVinu/uni-vpn/internal/config"
	"github.com/DavidVinu/uni-vpn/internal/credentials"
	"github.com/DavidVinu/uni-vpn/internal/devsync"
	"github.com/DavidVinu/uni-vpn/internal/messages"
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

// Settings, Account with another university ID; the keyring refuses the new password (the
// user cancels its prompt). A browser tab asks for the tunnel while the prompt is open: its
// attempt reads the new ID's password, which is not there yet. Once the setup failed, the old
// account is back with its own password, so the app must not say "No password saved" for it.
func TestFailedAccountChangeLeavesNoMissingPasswordBehind(t *testing.T) {
	h := newHarness(t)
	h.cfg.MFA = "none"
	h.cfg.DemandWindow = 30
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := config.WriteInitial(path, "u", "other", []config.Setting{{Key: "host", Value: "vpn.example"}, {Key: "mfa", Value: "none"}}); err != nil {
		t.Fatal(err)
	}
	var kmu sync.Mutex
	keyring := map[string]string{"u": "old-pw"}
	var d *Daemon
	d = h.start(func(o *Options) {
		o.ConfigPath = path
		o.PasswordGetter = func(ctx context.Context) ([]byte, error) {
			user := d.user() // like keyringArgs
			kmu.Lock()
			defer kmu.Unlock()
			if pw, ok := keyring[user]; ok {
				return []byte(pw), nil
			}
			return nil, credentials.ErrPasswordMissing
		}
		o.PasswordSetter = func(pw string) error {
			// The keyring's prompt is open; a browser tab of the university asks meanwhile.
			d.RequestConnect()
			waitFor(3*time.Second, func() bool {
				d.mu.Lock()
				defer d.mu.Unlock()
				return !d.loopRunning
			})
			return &credentials.KeyringError{Msg: "prompt dismissed"}
		}
	})
	err := d.CompleteSetup("cd456", "new-pw", "", "other",
		[]config.Setting{{Key: "host", Value: "vpn.example"}, {Key: "mfa", Value: "none"}})
	if err == nil {
		t.Fatal("setup did not fail")
	}
	time.Sleep(300 * time.Millisecond)
	d.mu.Lock()
	user, state, message := d.cfg.User, d.state, d.message
	d.mu.Unlock()
	t.Logf("after the failed setup: user %s, state %s, message %q, action %v", user, state, message.ID,
		statusValue(t, d, "action"))
	if user != "u" {
		t.Fatal(user)
	}
	if message == messages.PasswordMissing {
		t.Errorf("the old account %s has its password, yet the app says %q with the %q fix", user, message.Text, message.Action)
	}
}

// The same with a second factor: the old account's is in the keyring, yet the app sends the
// user to set it up again.
func TestFailedAccountChangeLeavesNoMissingSecondFactorBehind(t *testing.T) {
	h := newHarness(t)
	h.cfg.DemandWindow = 30
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := config.WriteInitial(path, "u", "heidelberg", nil); err != nil {
		t.Fatal(err)
	}
	h.cfg.User, h.cfg.MFA = "u", "totp_field"
	var kmu sync.Mutex
	passwords := map[string]string{"u": "old-pw"}
	totps := map[string]string{"u": "base32:GEZDGNBVGY3TQOJQ"}
	var d *Daemon
	d = h.start(func(o *Options) {
		o.ConfigPath = path
		o.PasswordGetter = func(ctx context.Context) ([]byte, error) {
			user := d.user()
			kmu.Lock()
			defer kmu.Unlock()
			if pw, ok := passwords[user]; ok {
				return []byte(pw), nil
			}
			return nil, credentials.ErrPasswordMissing
		}
		o.TOTPGetter = func(ctx context.Context) ([]byte, error) {
			user := d.user()
			kmu.Lock()
			defer kmu.Unlock()
			if s, ok := totps[user]; ok {
				return []byte(s), nil
			}
			return nil, credentials.ErrTotpMissing
		}
		o.PasswordSetter = func(pw string) error {
			user := d.user()
			kmu.Lock()
			passwords[user] = pw
			kmu.Unlock()
			return nil
		}
		o.TOTPSetter = func(string) error {
			d.RequestConnect() // a browser tab while the keyring asks
			waitFor(3*time.Second, func() bool {
				d.mu.Lock()
				defer d.mu.Unlock()
				return !d.loopRunning
			})
			return &credentials.KeyringError{Msg: "prompt dismissed"}
		}
	})
	err := d.CompleteSetup("cd456", "new-pw", "base32:GEZDGNBVGY3TQOJR", "heidelberg", nil)
	if err == nil {
		t.Fatal("setup did not fail")
	}
	time.Sleep(300 * time.Millisecond)
	d.mu.Lock()
	user, state, message := d.cfg.User, d.state, d.message
	d.mu.Unlock()
	t.Logf("after the failed setup: user %s, state %s, message %q, action %v", user, state, message.ID,
		statusValue(t, d, "action"))
	if message == messages.TOTPMissing {
		t.Errorf("the old account %s has its second factor, yet the app says %q with the %q fix", user, message.Text, message.Action)
	}
}

// Settings, Account on the laptop: another university and ID. A sync round runs while the
// keyring stores the new password (the periodic round, or one another device woke): it must not
// send the group an account that is half the old one (the old university with the new ID), which
// the desktop then signs in with at the old gateway.
func TestSyncDuringAnAccountChangeSendsNoHalfAccount(t *testing.T) {
	ctx := context.Background()
	laptop, laptopH, desktop, desktopH := syncedPair(t, nil)
	_ = desktopH
	storing := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	laptopH.mu.Lock()
	laptop.opts.PasswordSetter = func(pw string) error {
		if laptop.user() == "zz999" {
			once.Do(func() { close(storing) })
			<-release // the keyring's prompt is open
		}
		laptopH.mu.Lock()
		defer laptopH.mu.Unlock()
		laptopH.stored = append(laptopH.stored, pw)
		return nil
	}
	laptopH.mu.Unlock()
	other := []config.Setting{{Key: "host", Value: "vpn.b.example"}, {Key: "mfa", Value: "none"}}
	done := make(chan error, 1)
	go func() { done <- laptop.CompleteSetup("zz999", "pw-B", "", "other", other) }()
	<-storing
	if err := laptop.sync.Sync(ctx); !errors.Is(err, devsync.ErrBusy) {
		t.Errorf("laptop round during the change: %v", err)
	}
	// The desktop's own round (every 30 minutes) comes before the laptop's next one.
	if err := desktop.sync.Sync(ctx); err != nil {
		t.Logf("desktop round: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	desktop.mu.Lock()
	user, university, host := desktop.cfg.User, desktop.cfg.University, desktop.cfg.Host
	desktop.mu.Unlock()
	t.Logf("desktop after its round: user %s, university %s, host %s", user, university, host)
	if user == "zz999" && host != "vpn.b.example" {
		t.Errorf("the desktop took the new ID %s with the old gateway %s (%s)", user, host, university)
	}
	desktopH.mu.Lock()
	t.Logf("desktop keyring writes: %v", desktopH.stored)
	desktopH.mu.Unlock()
	// The laptop's next round (woken by the setup's Notify) and the desktop's next one.
	laptop.sync.Sync(ctx)
	desktop.sync.Sync(ctx)
	desktop.mu.Lock()
	user, university, host = desktop.cfg.User, desktop.cfg.University, desktop.cfg.Host
	desktop.mu.Unlock()
	desktopH.mu.Lock()
	t.Logf("desktop after the next rounds: user %s, university %s, host %s, keyring writes %v", user, university, host, desktopH.stored)
	desktopH.mu.Unlock()
	if user != "zz999" || host != "vpn.b.example" {
		t.Errorf("the desktop did not take the new account: %s %s", user, host)
	}
}

// A change to a university with its own websites (Heidelberg) changes the proxy rule: the
// browsers are told, as after an edit of the list, or they keep the old rule until a restart.
func TestAccountChangeToOtherWebsitesRefreshesTheProxyRule(t *testing.T) {
	h := newHarness(t)
	a, c := setupAccounts[0], setupAccounts[2]
	h.cfg.User, h.cfg.University, h.cfg.Host, h.cfg.MFA, h.cfg.DefaultDomains = a.user, a.university, a.host, "none", nil
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := config.WriteInitial(path, a.user, a.university, a.overrides); err != nil {
		t.Fatal(err)
	}
	d := h.start(func(o *Options) { o.ConfigPath = path })
	before, _ := d.PAC()
	if err := d.CompleteSetup(c.user, c.password, c.token, c.university, c.overrides); err != nil {
		t.Fatal(err)
	}
	after, _ := d.PAC()
	h.mu.Lock()
	refreshed := append([]int(nil), h.refreshed...)
	h.mu.Unlock()
	if before == after || !strings.Contains(after, "sogo.uni-heidelberg.de") {
		t.Fatalf("the rule did not change:\n%s", after)
	}
	if len(refreshed) != 1 || refreshed[0] != h.cfg.HTTPPort {
		t.Fatalf("browsers told %v, want once on %d", refreshed, h.cfg.HTTPPort)
	}
}

// Another ID (a mistyped one corrected, or another account): the old one's secrets go, so that
// an uninstall, which knows only the current ID, leaves none behind. The same ID keeps them.
func TestAccountChangeDeletesTheOldAccountsSecrets(t *testing.T) {
	h := newHarness(t)
	a, c := setupAccounts[0], setupAccounts[2]
	h.cfg.User, h.cfg.University, h.cfg.Host, h.cfg.MFA = a.user, a.university, a.host, "none"
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := config.WriteInitial(path, a.user, a.university, a.overrides); err != nil {
		t.Fatal(err)
	}
	d := h.start(func(o *Options) { o.ConfigPath = path })
	deleted := func() []string {
		h.mu.Lock()
		defer h.mu.Unlock()
		return append([]string(nil), h.deletedSecrets...)
	}
	if err := d.CompleteSetup(a.user, a.password, a.token, a.university, a.overrides); err != nil {
		t.Fatal(err)
	}
	if got := deleted(); len(got) != 0 {
		t.Fatal("same account:", got)
	}
	if err := d.CompleteSetup(c.user, c.password, c.token, c.university, c.overrides); err != nil {
		t.Fatal(err)
	}
	if got := deleted(); len(got) != 1 || got[0] != a.user {
		t.Fatalf("deleted %v, want [%s]", got, a.user)
	}
}
