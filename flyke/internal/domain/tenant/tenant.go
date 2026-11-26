// Package tenant models multi-tenancy: a namespace is a tenant, and access
// tokens are API credentials bound to exactly one namespace.
package tenant

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/flyke-org/flyke/flyke/internal/domain/worker"
)

// Default always exists and cannot be deleted.
const Default = "default"

var (
	ErrInvalid      = errors.New("invalid namespace")
	ErrInvalidToken = errors.New("invalid or expired access token")
	nameRe          = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
)

// Namespace is a tenant. Everything a team owns (workflows, runs, events,
// secrets, bundles, pools, workers, access tokens) belongs to one namespace.
type Namespace struct {
	Name        string
	Description string
	// SharedWorkers lets the namespace's work run on shared pools (pools
	// that belong to no namespace, managed by the admin). Off by default:
	// the tenant's code then only runs on its own workers.
	SharedWorkers bool
	CreatedAt     time.Time
}

func (n *Namespace) Validate() error {
	if !nameRe.MatchString(n.Name) {
		return fmt.Errorf("%w: name %q must be lowercase letters, digits and '-' (max 63)", ErrInvalid, n.Name)
	}
	if len(n.Description) > 512 {
		return fmt.Errorf("%w: description is longer than 512 characters", ErrInvalid)
	}
	return nil
}

// Stats summarises what a namespace holds.
type Stats struct {
	Workflows  int
	ActiveRuns int
	Workers    int
	Tokens     int
}

const tokenPrefix = "flyke_at_"

// AccessToken authenticates REST calls for one namespace. Only the sha256 of
// its secret is stored.
type AccessToken struct {
	ID         string
	SecretHash string
	Namespace  string
	Name       string
	ExpiresAt  *time.Time // nil = never
	CreatedAt  time.Time
}

// NewAccessToken creates a token and returns it with its one-time plaintext.
func NewAccessToken(namespace, name string, now time.Time, ttl time.Duration) (AccessToken, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 {
		return AccessToken{}, "", fmt.Errorf("%w: a token needs a name (max 100 characters)", ErrInvalid)
	}
	id, secret := worker.RandomSuffix(8), worker.RandomSuffix(32)
	t := AccessToken{ID: id, SecretHash: worker.Hash(secret), Namespace: namespace, Name: name, CreatedAt: now}
	if ttl > 0 {
		exp := now.Add(ttl)
		t.ExpiresAt = &exp
	}
	return t, tokenPrefix + id + "." + secret, nil
}

// IsAccessToken reports whether raw looks like an access token.
func IsAccessToken(raw string) bool { return strings.HasPrefix(raw, tokenPrefix) }

// ParseAccessToken splits a plaintext token into its id and secret.
func ParseAccessToken(raw string) (id, secret string, err error) {
	id, secret, ok := strings.Cut(strings.TrimPrefix(raw, tokenPrefix), ".")
	if !IsAccessToken(raw) || !ok || id == "" || secret == "" {
		return "", "", ErrInvalidToken
	}
	return id, secret, nil
}

// Verify checks the secret (constant time) and the expiry.
func (t *AccessToken) Verify(secret string, now time.Time) error {
	if !worker.SecretMatches(secret, t.SecretHash) || (t.ExpiresAt != nil && now.After(*t.ExpiresAt)) {
		return ErrInvalidToken
	}
	return nil
}
