//go:build !windows

package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DavidVinu/uni-vpn/internal/config"
	"github.com/DavidVinu/uni-vpn/internal/credentials"
	"github.com/DavidVinu/uni-vpn/internal/devsync/relaytest"
	"github.com/DavidVinu/uni-vpn/internal/pyjson"
)

// userKeyring keeps the password and second factor by user, as the system keyrings do.
type userKeyring struct {
	mu        sync.Mutex
	d         *Daemon
	passwords map[string]string
	tokens    map[string]string
}

func (k *userKeyring) options(o *Options) {
	o.PasswordGetter = func(context.Context) ([]byte, error) {
		user := k.d.user()
		k.mu.Lock()
		defer k.mu.Unlock()
		pw, ok := k.passwords[user]
		if !ok {
			return nil, credentials.ErrPasswordMissing
		}
		return []byte(pw), nil
	}
	o.PasswordSetter = func(pw string) error {
		user := k.d.user()
		k.mu.Lock()
		defer k.mu.Unlock()
		k.passwords[user] = pw
		return nil
	}
	o.TOTPGetter = func(context.Context) ([]byte, error) {
		user := k.d.user()
		k.mu.Lock()
		defer k.mu.Unlock()
		token, ok := k.tokens[user]
		if !ok {
			return nil, credentials.ErrTotpMissing
		}
		return []byte(token), nil
	}
	o.TOTPSetter = func(token string) error {
		user := k.d.user()
		k.mu.Lock()
		defer k.mu.Unlock()
		k.tokens[user] = token
		return nil
	}
}

// A desktop linked to the group, but the group's account could not be used there (the keyring
// refused): the user sets up another account by hand. A round that starts while that setup waits
// on the keyring must not put the group's account back once the typed one is in.
func TestRoundDuringAFirstSetupByHandKeepsTheTypedAccount(t *testing.T) {
	ctx := context.Background()
	_, url := relaytest.Start(t)
	a, c := setupAccounts[0], setupAccounts[2]
	laptopK := &userKeyring{passwords: map[string]string{a.user: a.password}, tokens: map[string]string{}}
	desktopK := &userKeyring{passwords: map[string]string{}, tokens: map[string]string{}}
	laptopH := newHarness(t)
	laptopH.cfg.User, laptopH.cfg.University, laptopH.cfg.Host, laptopH.cfg.MFA = a.user, a.university, a.host, "none"
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := config.WriteInitial(path, a.user, a.university, a.overrides); err != nil {
		t.Fatal(err)
	}
	laptop := laptopH.start(syncOptions(url, dir, func(o *Options) { o.ConfigPath = path; laptopK.options(o) }),
		func(d *Daemon) { laptopK.d = d })
	desktopH := newHarness(t)
	desktopDir := t.TempDir()
	// The keyring refuses until the user types an account: rounds that try the group's again
	// meanwhile fail the same way.
	var hold, typing atomic.Bool
	entered, release := make(chan struct{}), make(chan struct{})
	desktop := desktopH.start(syncOptions(url, desktopDir, func(o *Options) {
		o.NeedsSetup, o.ConfigPath = true, filepath.Join(desktopDir, "config.toml")
		desktopK.options(o)
		set := o.PasswordSetter
		o.PasswordSetter = func(pw string) error {
			if !typing.Load() {
				return errors.New("keyring locked")
			}
			if hold.CompareAndSwap(true, false) {
				close(entered)
				<-release // the keyring's prompt is open
			}
			return set(pw)
		}
	}), func(d *Daemon) { desktopK.d = d })
	offer, err := laptop.Sync("offer", pyjson.O())
	if err != nil {
		t.Fatal(err)
	}
	pairing, _ := offer.Get("pairing")
	code, _ := pairing.(pyjson.Object).Get("code")
	if _, err := desktop.Sync("enter", pyjson.O("code", code)); err != nil {
		t.Fatal(err)
	}
	if statusValue(t, desktop, "setup_needed") != true {
		t.Fatal("the group's account was taken over; the test needs it refused")
	}
	typing.Store(true)
	hold.Store(true)
	done := make(chan error, 1)
	go func() { done <- desktop.CompleteSetup(c.user, c.password, c.token, c.university, c.overrides) }()
	<-entered
	round := make(chan error, 1)
	go func() { round <- desktop.sync.Sync(ctx) }()
	time.Sleep(200 * time.Millisecond) // a round that took the group's account would wait for the setup now
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	<-round
	for range 3 {
		for _, d := range []*Daemon{desktop, laptop} {
			if err := d.sync.Sync(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	if lu, du := statusValue(t, laptop, "user"), statusValue(t, desktop, "user"); du != c.user || lu != c.user {
		t.Errorf("laptop %v, desktop %v: want the account typed last (%s) on both", lu, du, c.user)
	}
}

// A round read this device's settings (no account yet) and takes the group's account over only
// once the user has saved one typed by hand: the typed one stays, on every device.
func TestRoundThatReadBeforeASetupByHandKeepsTheTypedAccount(t *testing.T) {
	ctx := context.Background()
	_, url := relaytest.Start(t)
	a, c := setupAccounts[0], setupAccounts[2]
	laptopK := &userKeyring{passwords: map[string]string{a.user: a.password}, tokens: map[string]string{}}
	desktopK := &userKeyring{passwords: map[string]string{}, tokens: map[string]string{}}
	laptopH := newHarness(t)
	laptopH.cfg.User, laptopH.cfg.University, laptopH.cfg.Host, laptopH.cfg.MFA = a.user, a.university, a.host, "none"
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := config.WriteInitial(path, a.user, a.university, a.overrides); err != nil {
		t.Fatal(err)
	}
	laptop := laptopH.start(syncOptions(url, dir, func(o *Options) { o.ConfigPath = path; laptopK.options(o) }),
		func(d *Daemon) { laptopK.d = d })
	desktopH := newHarness(t)
	desktopDir := t.TempDir()
	// The keyring refuses until the user types an account, then asks before it takes the first.
	var hold, typing, armed atomic.Bool
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	desktop := desktopH.start(syncOptions(url, desktopDir, func(o *Options) {
		o.NeedsSetup, o.ConfigPath = true, filepath.Join(desktopDir, "config.toml")
		desktopK.options(o)
		set := o.PasswordSetter
		o.PasswordSetter = func(pw string) error {
			if !typing.Load() {
				return errors.New("keyring locked")
			}
			if hold.CompareAndSwap(true, false) {
				close(entered)
				<-release // the keyring's prompt is open
			}
			return set(pw)
		}
	}), func(d *Daemon) {
		desktopK.d = d
		d.beforeApply = func() {
			if !armed.CompareAndSwap(true, false) {
				return
			}
			// The round has read the settings; now the user saves the account typed by hand.
			typing.Store(true)
			hold.Store(true)
			go func() { done <- d.CompleteSetup(c.user, c.password, c.token, c.university, c.overrides) }()
			<-entered
		}
	})
	offer, err := laptop.Sync("offer", pyjson.O())
	if err != nil {
		t.Fatal(err)
	}
	pairing, _ := offer.Get("pairing")
	code, _ := pairing.(pyjson.Object).Get("code")
	if _, err := desktop.Sync("enter", pyjson.O("code", code)); err != nil {
		t.Fatal(err)
	}
	if statusValue(t, desktop, "setup_needed") != true {
		t.Fatal("the group's account was taken over; the test needs it refused")
	}
	armed.Store(true)
	round := make(chan error, 1)
	go func() { round <- desktop.sync.Sync(ctx) }()
	<-entered
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	<-round
	for range 3 {
		for _, d := range []*Daemon{desktop, laptop} {
			if err := d.sync.Sync(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	if lu, du := statusValue(t, laptop, "user"), statusValue(t, desktop, "user"); du != c.user || lu != c.user {
		t.Errorf("laptop %v, desktop %v: want the account typed last (%s) on both", lu, du, c.user)
	}
}

// typedInTheNotifyGap: a desktop linked to the laptop's group (account a), whose keyring refused
// the group's account; the user then sets up c by hand. A round reads this device's settings
// after that setup wrote config.toml but before its Notify (while the proxy rule is refreshed):
// what it read already has what was typed. Then a few more rounds on both devices.
func typedInTheNotifyGap(t *testing.T, laptopH *harness, a, c testAccount, laptopK, desktopK *userKeyring) (laptop, desktop *Daemon) {
	t.Helper()
	ctx := context.Background()
	_, url := relaytest.Start(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := config.WriteInitial(path, a.user, a.university, a.overrides); err != nil {
		t.Fatal(err)
	}
	laptop = laptopH.start(syncOptions(url, dir, func(o *Options) { o.ConfigPath = path; laptopK.options(o) }),
		func(d *Daemon) { laptopK.d = d })
	desktopH := newHarness(t)
	desktopDir := t.TempDir()
	var typing, armed atomic.Bool
	applying := make(chan struct{})
	round := make(chan error, 1)
	desktop = desktopH.start(syncOptions(url, desktopDir, func(o *Options) {
		o.NeedsSetup, o.ConfigPath = true, filepath.Join(desktopDir, "config.toml")
		desktopK.options(o)
		set := o.PasswordSetter
		o.PasswordSetter = func(pw string) error {
			if !typing.Load() {
				return errors.New("keyring locked")
			}
			return set(pw)
		}
		refresh := o.ProxyRefresh
		o.ProxyRefresh = func(port int) {
			refresh(port)
			if armed.CompareAndSwap(true, false) {
				go func() { round <- desktop.sync.Sync(ctx) }()
				select {
				case <-applying:
				case <-time.After(5 * time.Second):
					t.Error("the round did not reach Apply")
				}
			}
		}
	}), func(d *Daemon) {
		desktopK.d = d
		d.beforeApply = func() {
			if typing.Load() {
				select {
				case <-applying:
				default:
					close(applying)
				}
			}
		}
	})
	offer, err := laptop.Sync("offer", pyjson.O())
	if err != nil {
		t.Fatal(err)
	}
	pairing, _ := offer.Get("pairing")
	code, _ := pairing.(pyjson.Object).Get("code")
	if _, err := desktop.Sync("enter", pyjson.O("code", code)); err != nil {
		t.Fatal(err)
	}
	if statusValue(t, desktop, "setup_needed") != true {
		t.Fatal("the group's account was taken over; the test needs it refused")
	}
	typing.Store(true)
	armed.Store(true)
	if err := desktop.CompleteSetup(c.user, c.password, c.token, c.university, c.overrides); err != nil {
		t.Fatal(err)
	}
	select {
	case <-round:
	case <-time.After(10 * time.Second):
		t.Fatal("no round in the gap (the rule did not change?)")
	}
	for range 3 {
		for _, d := range []*Daemon{desktop, laptop} {
			if err := d.sync.Sync(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	return laptop, desktop
}

// Another account typed: it stays, on every device, with its own password.
func TestRoundBeforeTheNotifyOfASetupByHandKeepsTheTypedAccount(t *testing.T) {
	a, c := setupAccounts[0], setupAccounts[2]
	laptopK := &userKeyring{passwords: map[string]string{a.user: a.password}, tokens: map[string]string{}}
	desktopK := &userKeyring{passwords: map[string]string{}, tokens: map[string]string{}}
	laptopH := newHarness(t)
	laptopH.cfg.User, laptopH.cfg.University, laptopH.cfg.Host, laptopH.cfg.MFA = a.user, a.university, a.host, "none"
	laptop, desktop := typedInTheNotifyGap(t, laptopH, a, c, laptopK, desktopK)
	lu, du := statusValue(t, laptop, "user"), statusValue(t, desktop, "user")
	if du != c.user || lu != c.user {
		t.Errorf("laptop %v, desktop %v: want the account typed last (%s) on both", lu, du, c.user)
	}
	laptopK.mu.Lock()
	defer laptopK.mu.Unlock()
	if pw, ok := laptopK.passwords[a.user]; ok && pw != a.password {
		t.Errorf("%s now has the password typed for %s: %q", a.user, c.user, pw)
	}
}

// The same account with a new password typed: the new one stays, on every device.
func TestRoundBeforeTheNotifyOfASetupByHandKeepsTheTypedPassword(t *testing.T) {
	a := setupAccounts[2]
	c := a
	c.password = "pw-new"
	laptopK := &userKeyring{passwords: map[string]string{a.user: a.password}, tokens: map[string]string{a.user: a.token}}
	desktopK := &userKeyring{passwords: map[string]string{}, tokens: map[string]string{}}
	laptopH := newHarness(t)
	prof, err := config.ProfileConfig(a.university, a.overrides)
	if err != nil {
		t.Fatal(err)
	}
	config.CopyProfile(laptopH.cfg, &prof)
	laptopH.cfg.User = a.user
	typedInTheNotifyGap(t, laptopH, a, c, laptopK, desktopK)
	laptopK.mu.Lock()
	lpw := laptopK.passwords[a.user]
	laptopK.mu.Unlock()
	desktopK.mu.Lock()
	dpw := desktopK.passwords[a.user]
	desktopK.mu.Unlock()
	if dpw != c.password || lpw != c.password {
		t.Errorf("laptop %q, desktop %q: want the password typed last (%s) on both", lpw, dpw, c.password)
	}
}

// A set-up desktop joins the laptop's group: a password typed while the join takes the group's
// settings over leaves the group's account in place on both.
func TestJoinWithAPasswordTypedMidRoundTakesTheGroupsAccount(t *testing.T) {
	ctx := context.Background()
	_, url := relaytest.Start(t)
	a, c := setupAccounts[0], setupAccounts[1]
	laptopK := &userKeyring{passwords: map[string]string{a.user: a.password}, tokens: map[string]string{}}
	desktopK := &userKeyring{passwords: map[string]string{c.user: c.password}, tokens: map[string]string{}}
	laptopH := newHarness(t)
	laptopH.cfg.User, laptopH.cfg.University, laptopH.cfg.Host, laptopH.cfg.MFA = a.user, a.university, a.host, "none"
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := config.WriteInitial(path, a.user, a.university, a.overrides); err != nil {
		t.Fatal(err)
	}
	laptop := laptopH.start(syncOptions(url, dir, func(o *Options) { o.ConfigPath = path; laptopK.options(o) }),
		func(d *Daemon) { laptopK.d = d })
	desktopH := newHarness(t)
	desktopH.cfg.User, desktopH.cfg.University, desktopH.cfg.Host, desktopH.cfg.MFA = c.user, c.university, c.host, "none"
	ddir := t.TempDir()
	dpath := filepath.Join(ddir, "config.toml")
	if err := config.WriteInitial(dpath, c.user, c.university, c.overrides); err != nil {
		t.Fatal(err)
	}
	var armed atomic.Bool
	armed.Store(true)
	desktop := desktopH.start(syncOptions(url, ddir, func(o *Options) { o.ConfigPath = dpath; desktopK.options(o) }),
		func(d *Daemon) {
			desktopK.d = d
			d.beforeApply = func() {
				if armed.CompareAndSwap(true, false) {
					if err := d.SetPassword("typed-meanwhile"); err != nil {
						t.Error(err)
					}
				}
			}
		})
	offer, err := laptop.Sync("offer", pyjson.O())
	if err != nil {
		t.Fatal(err)
	}
	pairing, _ := offer.Get("pairing")
	code, _ := pairing.(pyjson.Object).Get("code")
	desktop.Sync("enter", pyjson.O("code", code))
	for range 3 {
		for _, d := range []*Daemon{desktop, laptop} {
			d.sync.Sync(ctx)
		}
	}
	if lu, du := statusValue(t, laptop, "user"), statusValue(t, desktop, "user"); lu != a.user || du != a.user {
		t.Errorf("laptop %v, desktop %v: want the group's account (%s) after the join", lu, du, a.user)
	}
}
