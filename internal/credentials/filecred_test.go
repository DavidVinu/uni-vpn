package credentials

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestFileStoreRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), SecretsFile)
	key := bytes.Repeat([]byte{7}, 32)
	k := &Keyring{GOOS: "android", Cred: &FileStore{Path: path, Key: key}}
	if _, err := k.GetPassword(context.Background(), "ab123", time.Second, nil); !errors.Is(err, ErrPasswordMissing) {
		t.Fatalf("empty store: %v", err)
	}
	if err := k.StorePassword("ab123", "pässwort"); err != nil {
		t.Fatal(err)
	}
	if err := k.StoreTotp("ab123", "base32:JBSWY3DPEHPK3PXP"); err != nil {
		t.Fatal(err)
	}
	got, err := k.GetPassword(context.Background(), "ab123", time.Second, nil)
	if err != nil || string(got) != "pässwort" {
		t.Fatalf("password %q %v", got, err)
	}
	data, _ := os.ReadFile(path)
	if bytes.Contains(data, []byte("pässwort")) || bytes.Contains(data, []byte("JBSWY3DP")) {
		t.Fatal("secret stored in clear")
	}
	if info, _ := os.Stat(path); runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode().Perm())
	}
	// Another key cannot read it.
	other := &Keyring{GOOS: "android", Cred: &FileStore{Path: path, Key: bytes.Repeat([]byte{8}, 32)}}
	var ke *KeyringError
	if _, err := other.GetPassword(context.Background(), "ab123", time.Second, nil); !errors.As(err, &ke) {
		t.Fatalf("wrong key: %v", err)
	}
	if ok, _ := k.DeletePassword("ab123"); !ok {
		t.Fatal("delete")
	}
	if _, err := k.GetPassword(context.Background(), "ab123", time.Second, nil); !errors.Is(err, ErrPasswordMissing) {
		t.Fatalf("after delete: %v", err)
	}
	if got, _ := k.GetTotp(context.Background(), "ab123", time.Second, nil); string(got) != "base32:JBSWY3DPEHPK3PXP" {
		t.Fatalf("totp %q", got)
	}
}

func TestFileStoreWithoutKey(t *testing.T) {
	k := &Keyring{GOOS: "android", Cred: &FileStore{Path: filepath.Join(t.TempDir(), SecretsFile)}}
	var ke *KeyringError
	if err := k.StorePassword("ab123", "x"); !errors.As(err, &ke) {
		t.Fatalf("store without key: %v", err)
	}
}

// The iPhone app keeps its secrets like the Android app.
func TestIOSUsesTheFileStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), SecretsFile)
	k := &Keyring{GOOS: "ios", Cred: &FileStore{Path: path, Key: bytes.Repeat([]byte{7}, 32)}}
	if err := k.StorePassword("ab123", "pw"); err != nil {
		t.Fatal(err)
	}
	if got, err := k.GetPassword(context.Background(), "ab123", time.Second, nil); err != nil || string(got) != "pw" {
		t.Fatal(got, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

// The iOS core starts again in the same process, possibly with another folder.
func TestReloadFileStore(t *testing.T) {
	for _, n := range []byte{1, 2} {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)
		t.Setenv(KeyEnv, strings.Repeat(fmt.Sprintf("%02x", n), 32))
		s := ReloadFileStore()
		if s != AndroidStore() || s.Path != filepath.Join(dir, "uni-vpn", SecretsFile) ||
			!bytes.Equal(s.Key, bytes.Repeat([]byte{n}, 32)) {
			t.Fatalf("%+v", s)
		}
	}
}
