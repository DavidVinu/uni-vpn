package certs

import (
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Intermediates that gateways' next certificates come from: DigiCert's ECC G5 (DigiCert issues
// from G5 by default from 2026-11-12, only from 2027-06-08; uovpn.uottawa.ca is DigiCert ECC) and
// GlobalSign's R46 (no R3 issuance after 2026-08-27; cn-vpn.uwaterloo.ca is GlobalSign R3).
// Android 10-13 lack both roots, macOS 14 the DigiCert one; they must end at PEM alone.
func TestFutureIntermediates(t *testing.T) {
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(PEM)
	at := time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC)
	for _, name := range []string{"digicert-g5-ecc-ca1.pem", "globalsign-atlas-r46.pem", "globalsign-atlas-e46.pem"} {
		data, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		c := parse(t, data)[0]
		opts := x509.VerifyOptions{Roots: roots, CurrentTime: at, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
		if _, err := c.Verify(opts); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
