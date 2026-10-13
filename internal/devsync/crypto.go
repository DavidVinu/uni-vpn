// Package devsync keeps a user's devices in step: settings and secrets travel end-to-end
// encrypted through a small relay that only ever sees random ids and ciphertext. Devices join
// by scanning a QR code or typing the short code under it. docs/sync.md has the formats.
package devsync

import (
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/scrypt"
)

// Format is the first byte of every blob and the "format" of every document.
const Format = 1

var errDecrypt = errors.New("sync data could not be decrypted")

func random(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return b
}

func derive(secret []byte, info string, n int) []byte {
	out, err := hkdf.Key(sha256.New, secret, nil, "uni-vpn "+info, n)
	if err != nil {
		panic(err)
	}
	return out
}

// seal is version byte, 24-byte nonce, XChaCha20-Poly1305 ciphertext. The context string
// is the additional data, so a blob cannot be replayed into another slot kind.
func seal(key []byte, context string, plain []byte) []byte {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		panic(err)
	}
	nonce := random(chacha20poly1305.NonceSizeX)
	out := append([]byte{Format}, nonce...)
	return aead.Seal(out, nonce, plain, []byte("uni-vpn "+context))
}

func open(key []byte, context string, blob []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	if len(blob) < 1+chacha20poly1305.NonceSizeX+aead.Overhead() || blob[0] != Format {
		return nil, errDecrypt
	}
	nonce, ct := blob[1:1+chacha20poly1305.NonceSizeX], blob[1+chacha20poly1305.NonceSizeX:]
	plain, err := aead.Open(nil, nonce, ct, []byte("uni-vpn "+context))
	if err != nil {
		return nil, errDecrypt
	}
	return plain, nil
}

// group is what every device of one sync group shares: the root key. The relay id, the
// relay password and the encryption key all come from it.
type group struct{ root []byte }

func (g group) id() string         { return hex.EncodeToString(derive(g.root, "group id", 16)) }
func (g group) token() string      { return hex.EncodeToString(derive(g.root, "group token", 32)) }
func (g group) key() []byte        { return derive(g.root, "group key", 32) }
func newGroup() group              { return group{root: random(32)} }
func (g group) valid() bool        { return len(g.root) == 32 }
func (g group) equal(o group) bool { return string(g.root) == string(o.root) }

// box seals a new root key for one device: ephemeral X25519 public key, then a sealed blob
// with a key from the shared secret. Only that device's private key opens it.
func box(peer []byte, plain []byte) ([]byte, error) {
	pub, err := ecdh.X25519().NewPublicKey(peer)
	if err != nil {
		return nil, err
	}
	eph, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	shared, err := eph.ECDH(pub)
	if err != nil {
		return nil, err
	}
	epub := eph.PublicKey().Bytes()
	key := derive(append(shared, append(epub, peer...)...), "device box", 32)
	return append(epub, seal(key, "device box", plain)...), nil
}

func unbox(priv *ecdh.PrivateKey, sealed []byte) ([]byte, error) {
	if len(sealed) < 32 {
		return nil, errDecrypt
	}
	epub, err := ecdh.X25519().NewPublicKey(sealed[:32])
	if err != nil {
		return nil, errDecrypt
	}
	shared, err := priv.ECDH(epub)
	if err != nil {
		return nil, errDecrypt
	}
	own := priv.PublicKey().Bytes()
	key := derive(append(shared, append(sealed[:32:32], own...)...), "device box", 32)
	return open(key, "device box", sealed[32:])
}

// --- Pairing codes ---------------------------------------------------------------

// Crockford's base32: no I, L, O, U, so nothing is mistaken when typed.
const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// CodeLength is 12 characters, 60 bits, shown as XXXX-XXXX-XXXX.
const CodeLength = 12

// QRPrefix starts the text in the QR code; a phone recognizes uni-vpn codes by it.
const QRPrefix = "UNIVPN:"

func newCode() string {
	b := random(CodeLength)
	var s strings.Builder
	for _, c := range b {
		s.WriteByte(alphabet[c&31])
	}
	return s.String()
}

// FormatCode groups a code for reading: XXXX-XXXX-XXXX.
func FormatCode(code string) string {
	return code[0:4] + "-" + code[4:8] + "-" + code[8:12]
}

// NormalizeCode accepts what people type or what a QR code holds: any case, spaces and
// dashes, O for 0, I and L for 1. ok is false when it is not a code.
func NormalizeCode(text string) (code string, ok bool) {
	text = strings.ToUpper(strings.TrimSpace(text))
	text = strings.TrimPrefix(text, QRPrefix)
	var s strings.Builder
	for _, r := range text {
		switch {
		case r == ' ' || r == '-' || r == '\t':
			continue
		case r == 'O':
			r = '0'
		case r == 'I' || r == 'L':
			r = '1'
		}
		if r > 127 || !strings.ContainsRune(alphabet, r) {
			return "", false
		}
		s.WriteRune(r)
	}
	if s.Len() != CodeLength {
		return "", false
	}
	return s.String(), true
}

// pairing is what both sides of one code derive: slot A, written by the device showing the
// code, slot B, written by the device that typed or scanned it, and the key for both.
type pairing struct {
	a, b  string
	key   []byte
	close *sync.Once // set by show
}

// scrypt makes each guess at a 60-bit code expensive; the code is the only secret. r=1, p=8
// needs 4 MiB at a time where r=8, p=1 needed 32 MiB for the same work: an iPhone pairs in
// its tunnel extension, which may use about 50 MB in all.
var scryptN = 1 << 15

func pairingFor(code string) pairing {
	s, err := scrypt.Key([]byte(code), []byte("uni-vpn pairing"), scryptN, 1, 8, 32)
	if err != nil {
		panic(err)
	}
	return pairing{
		a:   hex.EncodeToString(derive(s, "pairing slot a", 16)),
		b:   hex.EncodeToString(derive(s, "pairing slot b", 16)),
		key: derive(s, "pairing key", 32),
	}
}
