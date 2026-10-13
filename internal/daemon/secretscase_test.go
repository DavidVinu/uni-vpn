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

// ciCred is Credential Manager: target names compare without case.
type ciCred struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (c *ciCred) Read(target string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m[strings.ToLower(target)], nil
}
func (c *ciCred) Write(target, _ string, secret []byte, _ string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[strings.ToLower(target)] = secret
	return nil
}
func (c *ciCred) Delete(target string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.m[strings.ToLower(target)]
	delete(c.m, strings.ToLower(target))
	return ok
}

// A corrected ID that differs only in case ("AA111" typed, "aa111" meant) on Windows: the
// new account's secrets must stay.
func TestACaseOnlyChangeOfTheIDKeepsTheSecrets(t *testing.T) {
	store := &ciCred{m: map[string][]byte{}}
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
		t.Fatalf("password of %s after the case correction: %q %v", a.user, pw, err)
	}
	tok, err := credentials.GetTotp(context.Background(), a.user, 5e9)
	if err != nil || string(tok) != a.token {
		t.Fatalf("TOTP of %s after the case correction: %q %v", a.user, tok, err)
	}
}
