//go:build windows

package wintunnel

import (
	"errors"
	"testing"

	"github.com/DavidVinu/uni-vpn/internal/tunnel"
)

func TestPasswordIsPassedInTheANSICodePage(t *testing.T) {
	got, err := ansiEncode(1252, []byte("pä€"))
	if err != nil || string(got) != "p\xe4\x80" {
		t.Fatalf("%q %v", got, err)
	}
	_, err = ansiEncode(1252, []byte("p\u4e2d"))
	var pe *tunnel.PasswordEncodingError
	if !errors.As(err, &pe) || pe.Msg != "The password contains characters openconnect cannot read on this Windows (cp1252)" {
		t.Fatal(err)
	}
}
