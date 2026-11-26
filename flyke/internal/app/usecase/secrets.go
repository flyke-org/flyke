package usecase

import (
	"context"
	"errors"
	"sort"

	"github.com/flyke-org/flyke/flyke/internal/app/ports"
	"github.com/flyke-org/flyke/flyke/internal/domain/secret"
	"github.com/flyke-org/flyke/flyke/internal/domain/task"
	"github.com/flyke-org/flyke/flyke/internal/domain/worker"
)

// SecretWrite is a create/update request. For updates, a nil Value keeps the
// stored value and a nil Sensitive keeps the stored sensitivity.
type SecretWrite struct {
	Namespace string
	Key       string
	Type      secret.SecretType
	Value     *string
	Sensitive *bool
}

// secretAAD binds a ciphertext to the row it belongs to.
func secretAAD(nsp, key string) []byte { return []byte(nsp + "\x00" + key) }

func (s *Service) seal(sec *secret.Secret) error {
	if !sec.Sensitive {
		return nil
	}
	if s.cipher == nil {
		return precondition("sensitive secrets need an encryption key, and the control plane has none configured")
	}
	ct, err := s.cipher.Encrypt([]byte(sec.Value), secretAAD(sec.Namespace, sec.Key))
	if err != nil {
		return err
	}
	sec.Value = ct
	return nil
}

func (s *Service) open(sec secret.Secret) (secret.Secret, error) {
	if !sec.Sensitive {
		return sec, nil
	}
	if s.cipher == nil {
		return sec, precondition("cannot decrypt secret %q: no encryption key configured", sec.Key)
	}
	plain, err := s.cipher.Decrypt(sec.Value, secretAAD(sec.Namespace, sec.Key))
	if err != nil {
		return sec, err
	}
	sec.Value = string(plain)
	return sec, nil
}

func normalize(sec *secret.Secret) error {
	if err := sec.Normalize(); err != nil {
		return invalid("%v", errors.Unwrap(err))
	}
	return nil
}

// CreateSecret stores a new secret; it fails with ErrAlreadyExists when the
// key is already set in its scope. The returned secret is redacted.
func (s *Service) CreateSecret(ctx context.Context, in SecretWrite) (secret.Secret, error) {
	sec := secret.Secret{Namespace: in.Namespace, Key: in.Key, Type: in.Type}
	if in.Value != nil {
		sec.Value = *in.Value
	}
	if in.Sensitive != nil {
		sec.Sensitive = *in.Sensitive
	}
	if err := normalize(&sec); err != nil {
		return secret.Secret{}, err
	}
	if err := s.seal(&sec); err != nil {
		return secret.Secret{}, err
	}
	sec.ID = s.ids.NewID("sec")
	err := s.tx(ctx, func(t *txn) error {
		sec.CreatedAt, sec.UpdatedAt = t.now, t.now
		return t.Secrets().Create(ctx, sec)
	})
	return sec.Redacted(), err
}

// PutSecret creates the secret or updates an existing one in place.
func (s *Service) PutSecret(ctx context.Context, in SecretWrite) (secret.Secret, error) {
	sec := secret.Secret{Namespace: in.Namespace, Key: in.Key, Type: in.Type}
	if in.Value != nil {
		sec.Value = *in.Value
	}
	if err := normalize(&sec); err != nil {
		return secret.Secret{}, err
	}
	err := s.tx(ctx, func(t *txn) error {
		existing, err := t.Secrets().Get(ctx, sec.Namespace, sec.Key)
		found := err == nil
		if err != nil && !errors.Is(err, ports.ErrNotFound) {
			return err
		}
		sec.Sensitive = found && existing.Sensitive
		if in.Sensitive != nil {
			sec.Sensitive = *in.Sensitive
		}
		switch {
		case in.Value != nil:
			if err := s.seal(&sec); err != nil {
				return err
			}
		case !found:
			return invalid("value is required to create secret %q", sec.Key)
		case existing.Sensitive != sec.Sensitive:
			return invalid("a new value is required to change the sensitivity of secret %q", sec.Key)
		default:
			sec.Value = existing.Value // already in its stored form
		}
		if found {
			sec.ID, sec.CreatedAt = existing.ID, existing.CreatedAt
		} else {
			sec.ID, sec.CreatedAt = s.ids.NewID("sec"), t.now
		}
		sec.UpdatedAt = t.now
		return t.Secrets().Put(ctx, sec)
	})
	return sec.Redacted(), err
}

func scopeNS(typ secret.SecretType, nsp string) string {
	if typ == secret.Shared {
		return secret.SharedNamespace
	}
	return nsp
}

// GetSecret returns one secret of the given scope, redacted.
func (s *Service) GetSecret(ctx context.Context, typ secret.SecretType, nsp, key string) (sec secret.Secret, err error) {
	err = s.read(ctx, func(r ports.Repos) error {
		sec, err = r.Secrets().Get(ctx, scopeNS(typ, nsp), key)
		return err
	})
	return sec.Redacted(), err
}

// ResolveSecret returns the secret a run in nsp sees for key: the
// namespace's Private secret, else the Shared one. It is redacted.
func (s *Service) ResolveSecret(ctx context.Context, nsp, key string) (sec secret.Secret, err error) {
	err = s.read(ctx, func(r ports.Repos) error {
		sec, err = resolveSecret(ctx, r, nsp, key)
		return err
	})
	return sec.Redacted(), err
}

func resolveSecret(ctx context.Context, r ports.Repos, nsp, key string) (secret.Secret, error) {
	if nsp != secret.SharedNamespace {
		sec, err := r.Secrets().Get(ctx, nsp, key)
		if !errors.Is(err, ports.ErrNotFound) {
			return sec, err
		}
	}
	return r.Secrets().Get(ctx, secret.SharedNamespace, key)
}

// ListSecrets returns, redacted and ordered by key, the Shared secrets plus
// (when nsp is not empty) the Private secrets of nsp.
func (s *Service) ListSecrets(ctx context.Context, nsp string) (out []secret.Secret, err error) {
	err = s.read(ctx, func(r ports.Repos) error {
		shared, err := r.Secrets().List(ctx, secret.SharedNamespace)
		if err != nil {
			return err
		}
		out = shared
		if nsp != secret.SharedNamespace {
			private, err := r.Secrets().List(ctx, nsp)
			if err != nil {
				return err
			}
			out = append(private, shared...)
		}
		return nil
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	for i := range out {
		out[i] = out[i].Redacted()
	}
	return
}

func (s *Service) DeleteSecret(ctx context.Context, typ secret.SecretType, nsp, key string) error {
	return s.tx(ctx, func(t *txn) error {
		return t.Secrets().Delete(ctx, scopeNS(typ, nsp), key)
	})
}

// SecretForTask returns the decrypted secret visible to the task that w is
// executing under leaseID. Only running tasks and compensations may read
// secrets, and only from the namespace of their run (or Shared ones).
func (s *Service) SecretForTask(ctx context.Context, w *worker.Worker, leaseID, key string) (sec secret.Secret, err error) {
	if !secret.ValidKey(key) {
		return sec, invalid("invalid secret key %q", key)
	}
	err = s.read(ctx, func(r ports.Repos) error {
		item, err := r.Tasks().GetByLease(ctx, leaseID)
		if err != nil {
			return precondition("%v", task.ErrStaleLease)
		}
		if err := item.CheckLease(leaseID, w.Name); err != nil {
			return precondition("%v", err)
		}
		if item.Kind != task.KindTask && item.Kind != task.KindCompensation {
			return invalid("secrets can only be read from tasks, not from the workflow body")
		}
		sec, err = resolveSecret(ctx, r, item.Namespace, key)
		return err
	})
	if err != nil {
		return sec, err
	}
	return s.open(sec)
}
