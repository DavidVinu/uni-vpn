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
