//go:build windows

package credentials

import (
	"crypto/rand"
	"encoding/hex"
	"testing"
)

// Port of tests/test_windows_native.py CredentialManagerTests: real Credential Manager.
func TestCredentialManagerWriteReadDelete(t *testing.T) {
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	target := CredentialTarget("uni-vpn-test", hex.EncodeToString(id))
	c := nativeCred{}
	t.Cleanup(func() { c.Delete(target) })
	if got, err := c.Read(target); got != nil || err != nil {
		t.Fatalf("got %q, %v", got, err)
	}
	if err := c.Write(target, "ab123", []byte("pä$$ w0rd"), ""); err != nil {
		t.Fatal(err)
	}
	if got, err := c.Read(target); string(got) != "pä$$ w0rd" || err != nil {
		t.Fatalf("got %q, %v", got, err)
	}
	if err := c.Write(target, "ab123", []byte("second"), "Uni VPN"); err != nil {
		t.Fatal(err)
	}
	if got, _ := c.Read(target); string(got) != "second" {
		t.Fatalf("got %q", got)
	}
	if !c.Delete(target) {
		t.Fatal("delete failed")
	}
	if got, err := c.Read(target); got != nil || err != nil {
		t.Fatalf("got %q, %v", got, err)
	}
	if c.Delete(target) {
		t.Fatal("second delete succeeded")
	}
}
