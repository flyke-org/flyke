// Package worker models the data plane: workers, pools and the secrets used
// to bootstrap and authenticate them.
package worker

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flyke-org/flyke/flyke/internal/domain/workflow"
)

// PoolLabel is set on every worker to the pool its join token belongs to.
const PoolLabel = "flyke.io/pool"

type Phase string

const (
	Ready    Phase = "Ready"
	NotReady Phase = "NotReady"
)

type Worker struct {
	Name string
	// Namespace is the tenant the worker belongs to (its pool's namespace).
	// "" = a shared worker, which serves namespaces allowed to use them.
	Namespace     string
	Pool          string
	Hostname      string
	Labels        map[string]string
	Runtimes      []string
	Slots         int
	FreeSlots     int
	Capacity      workflow.Resources
	AgentVersion  string
	Phase         Phase
	Unschedulable bool
	Draining      bool
	CredentialID  string
	CredentialSum string // sha256 of the credential secret
	LastHeartbeat time.Time
	CreatedAt     time.Time
}

// Schedulable reports whether new work may be bound to the worker.
func (w *Worker) Schedulable() bool {
	return w.Phase == Ready && !w.Unschedulable && !w.Draining
}

// SupportsRuntime reports whether the worker can execute code for runtime
// (e.g. "python"). Matching is on the language prefix of "python/3.12".
func (w *Worker) SupportsRuntime(runtime string) bool {
	if runtime == "" {
		return true
	}
	lang := strings.SplitN(runtime, "/", 2)[0]
	for _, r := range w.Runtimes {
		if strings.SplitN(r, "/", 2)[0] == lang {
			return true
		}
	}
	return false
}

// Selector is an equality-based label selector (matchLabels).
type Selector map[string]string

func (s Selector) Matches(labels map[string]string) bool {
	for k, v := range s {
		if labels[k] != v {
			return false
		}
	}
	return true
}

type Pool struct {
	Name string
	// Namespace owns the pool; "" = a shared pool managed by the admin.
	Namespace          string
	Selector           Selector
	MaxConcurrentTasks int
	ResourceProfiles   map[string]workflow.Resources
	CreatedAt          time.Time
}

// EffectiveSelector defaults to the pool label.
func (p *Pool) EffectiveSelector() Selector {
	if len(p.Selector) == 0 {
		return Selector{PoolLabel: p.Name}
	}
	return p.Selector
}

// ------------------------------------------------------------- secrets ----

const (
	joinTokenPrefix  = "flyke_jt_"
	credentialPrefix = "flyke_wk_"
)

var (
	ErrInvalidToken = errors.New("invalid or expired token")
	ErrLabelDenied  = errors.New("label not allowed by join token")
)

// Secret is an "<id>.<secret>" pair; only the sha256 of the secret is stored.
type Secret struct {
	ID     string
	Secret string
}

func newSecret() (Secret, error) {
	id, err := randomString(6)
	if err != nil {
		return Secret{}, err
	}
	sec, err := randomString(24)
	if err != nil {
		return Secret{}, err
	}
	return Secret{ID: id, Secret: sec}, nil
}

func randomString(n int) (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b), nil
}

// RandomSuffix returns a short random lowercase string for resource names.
func RandomSuffix(n int) string {
	s, err := randomString(n)
	if err != nil {
		panic(err)
	}
	return s
}

func Hash(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

func parse(prefix, raw string) (Secret, error) {
	if !strings.HasPrefix(raw, prefix) {
		return Secret{}, ErrInvalidToken
	}
	id, sec, ok := strings.Cut(strings.TrimPrefix(raw, prefix), ".")
	if !ok || id == "" || sec == "" {
		return Secret{}, ErrInvalidToken
	}
	return Secret{ID: id, Secret: sec}, nil
}

// SecretMatches compares in constant time.
func SecretMatches(secret, storedHash string) bool {
	return subtle.ConstantTimeCompare([]byte(Hash(secret)), []byte(storedHash)) == 1
}

// JoinToken lets a host register itself as a worker of a pool.
type JoinToken struct {
	ID            string
	SecretHash    string
	Pool          string
	ExpiresAt     time.Time
	MaxUses       int // 0 = unlimited
	Uses          int
	AllowedLabels map[string]string // label -> allowed value ("*" = any)
	CreatedAt     time.Time
}

// NewJoinToken creates a token and returns it with its one-time plaintext.
func NewJoinToken(pool string, now time.Time, ttl time.Duration, maxUses int, allowed map[string]string) (JoinToken, string, error) {
	s, err := newSecret()
	if err != nil {
		return JoinToken{}, "", err
	}
	t := JoinToken{
		ID: s.ID, SecretHash: Hash(s.Secret), Pool: pool,
		ExpiresAt: now.Add(ttl), MaxUses: maxUses, AllowedLabels: allowed, CreatedAt: now,
	}
	return t, joinTokenPrefix + s.ID + "." + s.Secret, nil
}

func ParseJoinToken(raw string) (Secret, error) { return parse(joinTokenPrefix, raw) }

// Redeem validates the token and the requested labels, consuming one use.
func (t *JoinToken) Redeem(secret string, now time.Time, labels map[string]string) error {
	if !SecretMatches(secret, t.SecretHash) || now.After(t.ExpiresAt) || (t.MaxUses > 0 && t.Uses >= t.MaxUses) {
		return ErrInvalidToken
	}
	for k, v := range labels {
		if k == PoolLabel {
			if v != t.Pool {
				return fmt.Errorf("%w: %s=%s", ErrLabelDenied, k, v)
			}
			continue
		}
		if t.AllowedLabels == nil {
			continue // no restriction configured
		}
		allowed, ok := t.AllowedLabels[k]
		if !ok || (allowed != "*" && allowed != v) {
			return fmt.Errorf("%w: %s=%s", ErrLabelDenied, k, v)
		}
	}
	t.Uses++
	return nil
}

// NewCredential creates a worker credential and returns its plaintext.
func NewCredential() (Secret, string, error) {
	s, err := newSecret()
	if err != nil {
		return Secret{}, "", err
	}
	return s, credentialPrefix + s.ID + "." + s.Secret, nil
}

func ParseCredential(raw string) (Secret, error) { return parse(credentialPrefix, raw) }
