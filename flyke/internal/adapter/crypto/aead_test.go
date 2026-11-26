package crypto

import (
	"bytes"
	"encoding/base64"
	"path/filepath"
	"testing"
)

func TestRoundTripAndBinding(t *testing.T) {
	key := bytes.Repeat([]byte{7}, KeySize)
	a, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	ct, err := a.Encrypt([]byte("hunter2"), []byte("default/pw"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains([]byte(ct), []byte("hunter2")) {
		t.Fatal("plaintext visible in ciphertext")
	}
	if got, err := a.Decrypt(ct, []byte("default/pw")); err != nil || string(got) != "hunter2" {
		t.Fatalf("decrypt: %q %v", got, err)
	}
	if _, err := a.Decrypt(ct, []byte("other/pw")); err == nil {
		t.Fatal("ciphertext accepted under a different aad")
	}
	other, _ := New(bytes.Repeat([]byte{8}, KeySize))
	if _, err := other.Decrypt(ct, []byte("default/pw")); err == nil {
		t.Fatal("ciphertext accepted under a different key")
	}
}

func TestKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.key")
	k1, err := LoadOrCreateKeyFile(path)
	if err != nil || len(k1) != KeySize {
		t.Fatalf("create: %v", err)
	}
	k2, err := LoadOrCreateKeyFile(path)
	if err != nil || !bytes.Equal(k1, k2) {
		t.Fatalf("reload: %v", err)
	}
	if k, err := ParseKey(base64.RawURLEncoding.EncodeToString(k1)); err != nil || !bytes.Equal(k, k1) {
		t.Fatalf("parse url-encoded: %v", err)
	}
	if _, err := ParseKey("c2hvcnQ="); err == nil {
		t.Fatal("short key accepted")
	}
}
