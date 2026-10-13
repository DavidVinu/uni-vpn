//go:build !windows

package daemon

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/DavidVinu/uni-vpn/internal/config"
	"github.com/DavidVinu/uni-vpn/internal/credentials"
)

// memCred is a credential store; with fold, Credential Manager: target names compare without case.
type memCred struct {
	fold bool
	mu   sync.Mutex
	m    map[string][]byte
}

func (c *memCred) key(target string) string {
	if c.fold {
		return strings.ToLower(target)
	}
	return target
}
func (c *memCred) Read(target string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m[c.key(target)], nil
}
func (c *memCred) Write(target, _ string, secret []byte, _ string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[c.key(target)] = secret
	return nil
}
func (c *memCred) Delete(target string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.m[c.key(target)]
	delete(c.m, c.key(target))
	return ok
}

// A corrected ID that differs only in case ("AA111" typed, "aa111" meant): the new account's
// secrets stay, also where the store ignores case (Windows), and the old ones go, also where it
// does not (the phones' secrets.json, secret-tool).
func TestACaseOnlyChangeOfTheIDKeepsTheSecrets(t *testing.T) {
	for _, fold := range []bool{true, false} {
		store := &memCred{fold: fold, m: map[string][]byte{}}
		old := credentials.Default
		credentials.Default = &credentials.Keyring{GOOS: "windows", Cred: store}
		t.Cleanup(func() { credentials.Default = old })

		h := newHarness(t)
		a := setupAccounts[2] // heidelberg, with a TOTP secret
		upper := strings.ToUpper(a.user)
		h.cfg.User, h.cfg.University, h.cfg.Host = upper, a.university, a.host
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := config.WriteInitial(path, upper, a.university, a.overrides); err != nil {
			t.Fatal(err)
		}
		d := h.start(func(o *Options) {
			o.ConfigPath = path
			o.PasswordGetter, o.PasswordSetter, o.TOTPGetter, o.TOTPSetter, o.SecretsDeleter = nil, nil, nil, nil, nil
		})
		if err := d.CompleteSetup(upper, a.password, a.token, a.university, a.overrides); err != nil {
			t.Fatal(err)
		}
		if err := d.CompleteSetup(a.user, a.password, a.token, a.university, a.overrides); err != nil {
			t.Fatal(err)
		}
		pw, err := credentials.GetPassword(context.Background(), a.user, 5e9)
		if err != nil || string(pw) != a.password {
			t.Fatalf("fold %v: password of %s after the case correction: %q %v", fold, a.user, pw, err)
		}
		tok, err := credentials.GetTotp(context.Background(), a.user, 5e9)
		if err != nil || string(tok) != a.token {
			t.Fatalf("fold %v: TOTP of %s after the case correction: %q %v", fold, a.user, tok, err)
		}
		store.mu.Lock()
		for target := range store.m {
			if strings.Contains(target, upper) {
				t.Errorf("fold %v: the old ID's %s stays", fold, target)
			}
		}
		store.mu.Unlock()
	}
}
