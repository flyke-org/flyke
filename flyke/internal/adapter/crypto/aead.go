// Package crypto implements ports.Cipher with AES-256-GCM.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
)

// KeySize is the size in bytes of an AES-256 key.
const KeySize = 32

const prefix = "v1:"

// AEAD encrypts small values. Ciphertexts are "v1:" + base64(nonce|sealed).
type AEAD struct{ gcm cipher.AEAD }

func New(key []byte) (*AEAD, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("secret key must be %d bytes, got %d", KeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &AEAD{gcm: gcm}, nil
}

// Encrypt seals plain; aad binds the ciphertext to its context (e.g. the
// secret's namespace and key) so it cannot be swapped between rows.
func (a *AEAD) Encrypt(plain, aad []byte) (string, error) {
	nonce := make([]byte, a.gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := a.gcm.Seal(nonce, nonce, plain, aad)
	return prefix + base64.StdEncoding.EncodeToString(sealed), nil
}

func (a *AEAD) Decrypt(ciphertext string, aad []byte) ([]byte, error) {
	raw, ok := strings.CutPrefix(ciphertext, prefix)
	if !ok {
		return nil, errors.New("unknown ciphertext format")
	}
	b, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, err
	}
	n := a.gcm.NonceSize()
	if len(b) < n {
		return nil, errors.New("ciphertext too short")
	}
	plain, err := a.gcm.Open(nil, b[:n], b[n:], aad)
	if err != nil {
		return nil, errors.New("cannot decrypt secret (wrong secret key?)")
	}
	return plain, nil
}

// ParseKey decodes a base64 (std or URL, padded or not) 32-byte key.
func ParseKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if k, err := enc.DecodeString(s); err == nil {
			if len(k) != KeySize {
				return nil, fmt.Errorf("secret key must decode to %d bytes, got %d", KeySize, len(k))
			}
			return k, nil
		}
	}
	return nil, errors.New("secret key is not valid base64")
}

// LoadOrCreateKeyFile reads the base64 key stored at path, creating the file
// (mode 0600) with a fresh random key when it does not exist.
func LoadOrCreateKeyFile(path string) ([]byte, error) {
	if b, err := os.ReadFile(path); err == nil {
		return ParseKey(string(b))
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) { // lost a race with another process
			return LoadOrCreateKeyFile(path)
		}
		return nil, err
	}
	defer f.Close()
	if _, err := f.WriteString(base64.StdEncoding.EncodeToString(key) + "\n"); err != nil {
		return nil, err
	}
	return key, f.Sync()
}
