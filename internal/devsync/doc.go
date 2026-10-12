package devsync

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
)

// Keys of a document. A domain is one key each, so adding a website on one device and
// removing another on a second device both survive.
const (
	KeyAccount    = "account"  // {"university", "user", "overrides": {field: value}}
	KeyPassword   = "password" // string
	KeyTOTP       = "totp"     // string, "" without a second factor
	KeyLanguage   = "language" // string, "" follows the system
	KeyAutoUpdate = "auto_update"
	DomainPrefix  = "domain/" // + host: true listed, false removed
	DevicePrefix  = "device/" // + device id: Device, null once removed
)

// Secret keys never leave the keyring except inside the encrypted document.
func isSecret(key string) bool { return key == KeyPassword || key == KeyTOTP }

// Entry is one value with the time (Unix milliseconds) and device of its last change.
type Entry struct {
	V json.RawMessage `json:"v"`
	T int64           `json:"t"`
	D string          `json:"d"`
}

// newer: the later change wins, the device id breaks a tie so every device agrees.
func (e Entry) newer(o Entry) bool {
	if e.T != o.T {
		return e.T > o.T
	}
	return e.D > o.D
}

// Moved is what an old group holds after a device was removed: the new root key, sealed
// for each device that stays. A device not listed was removed.
type Moved struct {
	Keys map[string][]byte `json:"keys"`
}

// Doc is the decrypted content of a group.
type Doc struct {
	Format  int              `json:"format"`
	Entries map[string]Entry `json:"entries,omitempty"`
	Moved   *Moved           `json:"moved,omitempty"`
}

// Device is the value of a device/<id> entry.
type Device struct {
	Name     string `json:"name"`
	Platform string `json:"platform"`
	Pub      []byte `json:"pub"` // X25519 public key, for the sealed key after a removal
}

func compact(v json.RawMessage) json.RawMessage {
	var b bytes.Buffer
	if json.Compact(&b, v) != nil {
		return v
	}
	return b.Bytes()
}

// hash identifies a value without keeping it. sync.json from before keyedHash has these.
func hash(v json.RawMessage) string {
	sum := sha256.Sum256(compact(v))
	return hex.EncodeToString(sum[:16])
}

// keyedHash is hash for sync.json: with a key from the keyring, so the file tells nothing
// about a password, not even to someone who tries every likely one.
func keyedHash(key []byte, v json.RawMessage) string {
	m := hmac.New(sha256.New, key)
	m.Write(compact(v))
	return hex.EncodeToString(m.Sum(nil)[:16])
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

var jsonFalse = json.RawMessage("false")

func isNull(v json.RawMessage) bool { return len(v) == 0 || string(compact(v)) == "null" }

// Domains lists the hosts a document has switched on, in key order.
func Domains(entries map[string]Entry) []string {
	var out []string
	for k, e := range entries {
		if strings.HasPrefix(k, DomainPrefix) && string(compact(e.V)) == "true" {
			out = append(out, strings.TrimPrefix(k, DomainPrefix))
		}
	}
	slices.Sort(out)
	return out
}
