// Package certs holds the certificates some university gateways need but do not send
// (extra.pem), for systems whose own trusted certificates lack them.
package certs

import (
	"bytes"
	"crypto/x509"
	_ "embed"
	"os"
	"path/filepath"
	"sync"
)

//go:embed extra.pem
var PEM []byte

// Pool is the system's trusted certificates plus PEM, for Go's TLS clients. On macOS, iOS and
// Windows Go asks the system first and tries PEM only when the system says no. nil (the
// system's alone) when the system's cannot be read.
var Pool = sync.OnceValue(func() *x509.CertPool {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		return nil
	}
	pool.AppendCertsFromPEM(PEM)
	return pool
})

// WriteFile writes system (the system's own certificates, nil for none) and then PEM to path,
// for openconnect's --cafile, which adds a file's certificates to the system's. It returns
// path, or "" when it cannot write it.
func WriteFile(path string, system []byte) string {
	data := system
	if len(data) > 0 && data[len(data)-1] != '\n' {
		data = append(data[:len(data):len(data)], '\n')
	}
	data = append(data[:len(data):len(data)], PEM...)
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		return path
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	f, err := os.CreateTemp(dir, filepath.Base(path)+".*")
	if err != nil {
		return ""
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(f.Name(), 0o644)
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return ""
	}
	return path
}
