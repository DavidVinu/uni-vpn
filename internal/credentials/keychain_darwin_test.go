//go:build darwin

package credentials

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/DavidVinu/uni-vpn/internal/platform"
)

// Port of tests/test_credentials.py MacKeychainRoundtrip: real security roundtrip in a
// throwaway keychain (also on CI runners without a GUI session). Every step has a timeout,
// so a hanging keychain dialog does not silently block the run.

func sec(t *testing.T, check bool, args ...string) string {
	t.Helper()
	t.Logf("security %s", strings.Join(args[:min(2, len(args))], " "))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, platform.Security, args...).Output()
	if err != nil && check {
		t.Fatalf("security %v: %v", args, err)
	}
	return string(out)
}

func throwawayKeychain(t *testing.T) {
	keychain := fmt.Sprintf("/tmp/uni-vpn-test-%d.keychain-db", os.Getpid())
	oldDefault := strings.Trim(strings.TrimSpace(sec(t, false, "default-keychain", "-d", "user")), `"`)
	var oldList []string
	for _, line := range strings.Split(sec(t, false, "list-keychains", "-d", "user"), "\n") {
		if l := strings.Trim(strings.TrimSpace(line), `"`); l != "" {
			oldList = append(oldList, l)
		}
	}
	t.Cleanup(func() {
		if oldDefault != "" {
			sec(t, false, "default-keychain", "-d", "user", "-s", oldDefault)
		}
		sec(t, false, append([]string{"list-keychains", "-d", "user", "-s"}, oldList...)...)
		sec(t, false, "delete-keychain", keychain)
	})
	sec(t, true, "create-keychain", "-p", "ci", keychain)
	sec(t, true, "unlock-keychain", "-p", "ci", keychain)
	sec(t, true, "set-keychain-settings", keychain) // no auto-lock
	sec(t, true, append([]string{"list-keychains", "-d", "user", "-s", keychain}, oldList...)...)
	sec(t, true, "default-keychain", "-d", "user", "-s", keychain)
}

func mustGet(t *testing.T, get func(context.Context, string, time.Duration) ([]byte, error), user, want string) {
	t.Helper()
	got, err := get(context.Background(), user, 10*time.Second)
	if err != nil || string(got) != want {
		t.Fatalf("got %q, %v, want %q", got, err, want)
	}
}

func mustDelete(t *testing.T, del func(string) (bool, error), user string) {
	t.Helper()
	if ok, err := del(user); !ok || err != nil {
		t.Fatalf("got %v, %v", ok, err)
	}
}

func TestKeychainRoundtripSimple(t *testing.T) {
	throwawayKeychain(t)
	user := "uni-vpn-citest-simple"
	if err := StorePassword(user, "simple123"); err != nil {
		t.Fatal(err)
	}
	mustGet(t, GetPassword, user, "simple123")
	mustDelete(t, DeletePassword, user)
}

func TestKeychainRoundtripTotpIsSeparateEntry(t *testing.T) {
	throwawayKeychain(t)
	user := "uni-vpn-citest-totp"
	if err := StorePassword(user, "password"); err != nil {
		t.Fatal(err)
	}
	if err := StoreTotp(user, "base32:GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"); err != nil {
		t.Fatal(err)
	}
	mustGet(t, GetPassword, user, "password")
	mustGet(t, GetTotp, user, "base32:GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ")
	mustDelete(t, DeleteTotp, user)
	if _, err := GetTotp(context.Background(), user, 10*time.Second); !errors.Is(err, ErrTotpMissing) {
		t.Fatalf("got %v", err)
	}
	mustGet(t, GetPassword, user, "password")
	mustDelete(t, DeletePassword, user)
}

func TestKeychainRoundtripSpecialCharacters(t *testing.T) {
	throwawayKeychain(t)
	user := "uni-vpn-citest"
	if err := StorePassword(user, `ci pass "quoted" \ back`); err != nil {
		t.Fatal(err)
	}
	mustGet(t, GetPassword, user, `ci pass "quoted" \ back`)
	if err := StorePassword(user, "second"); err != nil { // overwrite = delete and create anew
		t.Fatal(err)
	}
	mustGet(t, GetPassword, user, "second")
	mustDelete(t, DeletePassword, user)
	if _, err := GetPassword(context.Background(), user, 10*time.Second); !errors.Is(err, ErrPasswordMissing) {
		t.Fatalf("got %v", err)
	}
}
