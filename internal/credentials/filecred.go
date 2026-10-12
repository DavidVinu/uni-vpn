package credentials

import (
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/chacha20poly1305"

	"github.com/DavidVinu/uni-vpn/internal/platform"
)

// KeyEnv names the variable that hands the Android app's secret key to the core: 32 bytes as
// hex. The app keeps the key wrapped by the Android Keystore, so the file below is useless
// without the phone's hardware key.
const KeyEnv = "UNI_VPN_SECRETS_KEY"

// SecretsFile is where FileStore keeps the secrets, next to config.toml.
const SecretsFile = "secrets.json"

// FileStore is a CredStore in one file: target -> XChaCha20-Poly1305 ciphertext of the secret,
// with the target as associated data so entries cannot be swapped.
type FileStore struct {
	Path string
	Key  []byte

	mu sync.Mutex
}

var (
	androidMu    sync.Mutex
	androidStore *FileStore
)

// AndroidStore is the FileStore of the phone apps (Android and iOS): secrets.json in the
// config folder, the key from KeyEnv, both read on the first call.
func AndroidStore() *FileStore {
	androidMu.Lock()
	defer androidMu.Unlock()
	if androidStore == nil {
		androidStore = phoneStore()
	}
	return androidStore
}

// ReloadFileStore reads the config folder and KeyEnv again: the iOS core can start more than
// once in one process.
func ReloadFileStore() *FileStore {
	androidMu.Lock()
	defer androidMu.Unlock()
	androidStore = phoneStore()
	return androidStore
}

func phoneStore() *FileStore {
	key, _ := hex.DecodeString(os.Getenv(KeyEnv))
	return &FileStore{Path: filepath.Join(platform.ConfigDir(), SecretsFile), Key: key}
}

func (s *FileStore) aead() (cipher.AEAD, error) {
	if len(s.Key) != chacha20poly1305.KeySize {
		return nil, &CredentialError{"The app did not hand over its secret key"}
	}
	return chacha20poly1305.NewX(s.Key)
}

func (s *FileStore) load() (map[string]string, error) {
	data, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, &CredentialError{err.Error()}
	}
	entries := map[string]string{}
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, &CredentialError{"The secrets file is damaged"}
	}
	return entries, nil
}

func (s *FileStore) save(entries map[string]string) error {
	data, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return &CredentialError{err.Error()}
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.Path), SecretsFile+".*")
	if err != nil {
		return &CredentialError{err.Error()}
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), s.Path)
	}
	if werr != nil {
		os.Remove(tmp.Name())
		return &CredentialError{werr.Error()}
	}
	return nil
}

// Read returns nil, nil when there is no entry.
func (s *FileStore) Read(target string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	aead, err := s.aead()
	if err != nil {
		return nil, err
	}
	entries, err := s.load()
	if err != nil {
		return nil, err
	}
	sealed, ok := entries[target]
	if !ok {
		return nil, nil
	}
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil || len(raw) < aead.NonceSize() {
		return nil, &CredentialError{"The secrets file is damaged"}
	}
	plain, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], []byte(target))
	if err != nil {
		return nil, &CredentialError{"A secret could not be decrypted"}
	}
	return plain, nil
}

func (s *FileStore) Write(target, _ string, secret []byte, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	aead, err := s.aead()
	if err != nil {
		return err
	}
	entries, err := s.load()
	if err != nil {
		return err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	entries[target] = base64.StdEncoding.EncodeToString(aead.Seal(nonce, nonce, secret, []byte(target)))
	return s.save(entries)
}

func (s *FileStore) Delete(target string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.load()
	if err != nil {
		return false
	}
	if _, ok := entries[target]; !ok {
		return false
	}
	delete(entries, target)
	return s.save(entries) == nil
}
