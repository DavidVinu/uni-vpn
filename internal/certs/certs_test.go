package certs

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func parse(t *testing.T, data []byte) []*x509.Certificate {
	t.Helper()
	var out []*x509.Certificate
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			return out
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, c)
	}
}

func TestExtraCertificates(t *testing.T) {
	want := []struct{ subject, issuer string }{
		{"HARICA TLS RSA Root CA 2021", "Hellenic Academic and Research Institutions RootCA 2015"},
		{"HARICA TLS ECC Root CA 2021", "Hellenic Academic and Research Institutions ECC RootCA 2015"},
		{"emSign Root TLS CA - G1", "emSign Root CA - G1"},
		{"emSign Root TLS CA - G3", "emSign ECC Root CA - G3"},
		{"DigiCert TLS RSA4096 Root G5", "DigiCert Global Root CA"},
		{"DigiCert TLS ECC P384 Root G5", "DigiCert Global Root G3"},
		{"GlobalSign Root R46", "GlobalSign"},
		{"GlobalSign Root E46", "GlobalSign"},
	}
	got := parse(t, PEM)
	if len(got) != len(want) {
		t.Fatalf("%d certificates, want %d", len(got), len(want))
	}
	for i, c := range got {
		if c.Subject.CommonName != want[i].subject || c.Issuer.CommonName != want[i].issuer || !c.IsCA {
			t.Errorf("certificate %d: %q by %q (CA %v), want %q by %q", i, c.Subject.CommonName, c.Issuer.CommonName, c.IsCA, want[i].subject, want[i].issuer)
		}
	}
}

func TestAndroidCopy(t *testing.T) {
	android, err := os.ReadFile(filepath.Join("..", "..", "app", "android", "app", "src", "main", "assets", "extra-ca.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(android, PEM) {
		t.Error("app/android/app/src/main/assets/extra-ca.pem differs from internal/certs/extra.pem")
	}
}

// The intermediates two gateways send (vpn.service.osu.edu, vpn2fa.hku.hk) end at PEM's
// certificates alone.
func TestGatewayIntermediates(t *testing.T) {
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(PEM)
	at := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	for _, name := range []string{"incommon-ovg2c.pem", "incommon-ovg3c.pem", "geotrust-g5-ca1.pem"} {
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

func TestPool(t *testing.T) {
	pool := Pool()
	if pool == nil {
		t.Skip("no system certificates here")
	}
	if Pool() != pool {
		t.Error("Pool is built again")
	}
	for _, c := range parse(t, PEM) {
		if _, err := c.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
			t.Errorf("%s: %v", c.Subject.CommonName, err)
		}
	}
}

func TestWriteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "ca.pem")
	if got := WriteFile(path, nil); got != path {
		t.Fatalf("WriteFile = %q", got)
	}
	if data, _ := os.ReadFile(path); !bytes.Equal(data, PEM) {
		t.Error("without system certificates the file is PEM")
	}
	system := []byte("-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----")
	if got := WriteFile(path, system); got != path {
		t.Fatalf("WriteFile = %q", got)
	}
	data, _ := os.ReadFile(path)
	if want := append(append(append([]byte{}, system...), '\n'), PEM...); !bytes.Equal(data, want) {
		t.Errorf("file = %q", data)
	}
	if system[len(system)-1] != '-' {
		t.Error("WriteFile changed its argument")
	}
	info, _ := os.Stat(path)
	if WriteFile(path, system) != path {
		t.Fatal("same content again")
	}
	if again, _ := os.Stat(path); !again.ModTime().Equal(info.ModTime()) {
		t.Error("the same content is written again")
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Errorf("left files behind: %v", entries)
	}
	blocked := filepath.Join(dir, "file")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := WriteFile(filepath.Join(blocked, "ca.pem"), nil); got != "" {
		t.Errorf("WriteFile under a file = %q", got)
	}
}
