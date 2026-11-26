// Package secret models key/value secrets that workflows read at run time.
//
// A secret is either Private (visible to one namespace) or Shared (stored
// once and visible to every namespace). When both exist for a key, the
// namespace's Private secret wins.
//
// A Sensitive secret (password, API key…) is encrypted at rest and its value
// is never returned by the REST API: only workers executing a task can read
// it. A non-sensitive secret is a plain configuration variable (username,
// endpoint…) whose value is visible to API users.
package secret

import (
	"errors"
	"fmt"
	"regexp"
	"time"
)

type SecretType string

const (
	Shared  SecretType = "Shared"
	Private SecretType = "Private"
)

// SharedNamespace is the storage namespace of Shared secrets.
const SharedNamespace = ""

// MaxValueBytes caps the size of a single secret value.
const MaxValueBytes = 64 << 10

var ErrInvalid = errors.New("invalid secret")

// keyRe accepts environment-variable style keys (e.g. "infoblox_password")
// as well as dotted / dashed names.
var keyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,252}$`)

type Secret struct {
	ID string
	// Namespace is SharedNamespace for Shared secrets.
	Namespace string
	Key       string
	// Value is the plain value in the domain; stores hold the ciphertext
	// when Sensitive is set.
	Value     string
	Sensitive bool
	Type      SecretType
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ValidKey reports whether k is an acceptable secret key.
func ValidKey(k string) bool { return keyRe.MatchString(k) }

// Normalize validates the secret and fills defaults: Type defaults to
// Private, and Namespace is cleared for Shared secrets.
func (s *Secret) Normalize() error {
	if !ValidKey(s.Key) {
		return fmt.Errorf("%w: invalid key %q (letters, digits, '_', '.', '-'; must not start with a digit)", ErrInvalid, s.Key)
	}
	if len(s.Value) > MaxValueBytes {
		return fmt.Errorf("%w: value exceeds %d bytes", ErrInvalid, MaxValueBytes)
	}
	switch s.Type {
	case "":
		s.Type = Private
	case Shared, Private:
	default:
		return fmt.Errorf("%w: unknown type %q (want %q or %q)", ErrInvalid, s.Type, Shared, Private)
	}
	if s.Type == Shared {
		s.Namespace = SharedNamespace
	} else if s.Namespace == "" {
		return fmt.Errorf("%w: a Private secret needs a namespace", ErrInvalid)
	}
	return nil
}

// Redacted returns a copy without the value when the secret is sensitive.
func (s Secret) Redacted() Secret {
	if s.Sensitive {
		s.Value = ""
	}
	return s
}
