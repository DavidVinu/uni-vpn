package octunnel

import (
	"reflect"
	"testing"
)

func TestLineWriterSplitsLikeStderr(t *testing.T) {
	var got []string
	w := &lineWriter{fn: func(s string) { got = append(got, s) }}
	w.write("Connected to 1.2.3.4:443\n")
	w.write("\nCertificate from VPN server \"x\" failed verification.\nReason: ")
	w.write("bad\r\n")
	w.write("Password:")
	w.flush()
	w.flush()
	want := []string{"Connected to 1.2.3.4:443", "", `Certificate from VPN server "x" failed verification.`,
		"Reason: bad", "Password:"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%q", got)
	}
}
