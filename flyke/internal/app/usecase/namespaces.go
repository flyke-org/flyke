package usecase

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flyke-org/flyke/flyke/internal/app/ports"
	"github.com/flyke-org/flyke/flyke/internal/domain/tenant"
	"github.com/flyke-org/flyke/flyke/internal/domain/worker"
)

// Multi-tenancy. A namespace is a tenant. Isolation rests on four rules,
// enforced here and in the scheduler rather than in the adapters:
//
//  1. every tenant-owned row carries its namespace, and use cases only read
//     and write rows of the namespace they are called for;
//  2. a worker belongs to its pool's namespace and only runs that
//     namespace's work; shared workers (namespace "") only serve namespaces
//     the admin opted in (Namespace.SharedWorkers);
//  3. a workflow can only reference a bundle its namespace uploaded;
//  4. an access token authenticates exactly one namespace.

type NamespaceInfo struct {
	tenant.Namespace
	Stats tenant.Stats
}

func (s *Service) CreateNamespace(ctx context.Context, n tenant.Namespace) (tenant.Namespace, error) {
	if err := n.Validate(); err != nil {
		return n, invalid("%v", err)
	}
	err := s.tx(ctx, func(t *txn) error {
		n.CreatedAt = t.now
		if err := t.Namespaces().Create(ctx, n); err != nil {
			if errors.Is(err, ports.ErrAlreadyExists) {
				return fmt.Errorf("%w: namespace %s", ErrAlreadyExists, n.Name)
			}
			return err
		}
		s.log.Info("namespace created", "namespace", n.Name, "shared_workers", n.SharedWorkers)
		return nil
	})
	return n, err
}

func (s *Service) GetNamespace(ctx context.Context, name string) (out NamespaceInfo, err error) {
	err = s.read(ctx, func(r ports.Repos) error {
		if out.Namespace, err = r.Namespaces().Get(ctx, name); err != nil {
			return err
		}
		out.Stats, err = r.Namespaces().Stats(ctx, name)
		return err
	})
	return
}

// NamespaceExists is the cheap check done on every namespaced API call.
func (s *Service) NamespaceExists(ctx context.Context, name string) (bool, error) {
	err := s.read(ctx, func(r ports.Repos) error {
		_, err := r.Namespaces().Get(ctx, name)
		return err
	})
	if errors.Is(err, ports.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (s *Service) ListNamespaces(ctx context.Context) (out []NamespaceInfo, err error) {
	err = s.read(ctx, func(r ports.Repos) error {
		all, err := r.Namespaces().List(ctx)
		if err != nil {
			return err
		}
		for _, n := range all {
			st, err := r.Namespaces().Stats(ctx, n.Name)
			if err != nil {
				return err
			}
			out = append(out, NamespaceInfo{Namespace: n, Stats: st})
		}
		return nil
	})
	return
}

// UpdateNamespace changes the description and the shared-workers setting.
func (s *Service) UpdateNamespace(ctx context.Context, n tenant.Namespace) (tenant.Namespace, error) {
	if err := n.Validate(); err != nil {
		return n, invalid("%v", err)
	}
	err := s.tx(ctx, func(t *txn) error {
		cur, err := t.Namespaces().Get(ctx, n.Name)
		if err != nil {
			return err
		}
		cur.Description, cur.SharedWorkers = n.Description, n.SharedWorkers
		n = cur
		t.notify(ports.TopicWork)
		return t.Namespaces().Update(ctx, cur)
	})
	return n, err
}

// DeleteNamespace removes a tenant and everything it owns. Its workers lose
// their credential and exit on their next heartbeat.
func (s *Service) DeleteNamespace(ctx context.Context, name string) error {
	if name == tenant.Default {
		return fmt.Errorf("%w: the %q namespace cannot be deleted", ErrFailedPrecondition, tenant.Default)
	}
	return s.tx(ctx, func(t *txn) error {
		if err := t.Namespaces().Delete(ctx, name); err != nil {
			return err
		}
		s.log.Info("namespace deleted", "namespace", name)
		t.notify(ports.TopicReconcile)
		return nil
	})
}

// --------------------------------------------------------- access tokens ----

func (s *Service) CreateAccessToken(ctx context.Context, ns, name string, ttl time.Duration) (tenant.AccessToken, string, error) {
	var tok tenant.AccessToken
	var plain string
	err := s.tx(ctx, func(t *txn) error {
		if _, err := t.Namespaces().Get(ctx, ns); err != nil {
			return err
		}
		var err error
		if tok, plain, err = tenant.NewAccessToken(ns, name, t.now, ttl); err != nil {
			return invalid("%v", err)
		}
		return t.AccessTokens().Create(ctx, tok)
	})
	return tok, plain, err
}

func (s *Service) ListAccessTokens(ctx context.Context, ns string) (out []tenant.AccessToken, err error) {
	err = s.read(ctx, func(r ports.Repos) error {
		out, err = r.AccessTokens().List(ctx, ns)
		return err
	})
	return
}

func (s *Service) DeleteAccessToken(ctx context.Context, ns, id string) error {
	return s.tx(ctx, func(t *txn) error { return t.AccessTokens().Delete(ctx, ns, id) })
}

// AuthenticateAccessToken resolves the token a REST caller presented.
func (s *Service) AuthenticateAccessToken(ctx context.Context, raw string) (tenant.AccessToken, error) {
	id, secret, err := tenant.ParseAccessToken(raw)
	if err != nil {
		return tenant.AccessToken{}, ErrUnauthenticated
	}
	var tok tenant.AccessToken
	err = s.read(ctx, func(r ports.Repos) error {
		tok, err = r.AccessTokens().Get(ctx, id)
		return err
	})
	if err != nil || tok.Verify(secret, s.clock.Now()) != nil {
		return tenant.AccessToken{}, ErrUnauthenticated
	}
	return tok, nil
}

// ------------------------------------------------------------ visibility ----

// usesSharedWorkers reports whether ns may run on shared pools. On a dev
// control plane every namespace may: there is one local worker.
func (s *Service) usesSharedWorkers(ctx context.Context, r ports.Repos, ns string) (bool, error) {
	if s.cfg.Dev {
		return true, nil
	}
	n, err := r.Namespaces().Get(ctx, ns)
	if errors.Is(err, ports.ErrNotFound) {
		return false, nil
	}
	return n.SharedWorkers, err
}

// poolVisible reports whether ns may target pool p. ns "" is the admin view.
func (s *Service) poolVisible(ctx context.Context, r ports.Repos, ns string, p *worker.Pool) (bool, error) {
	switch {
	case ns == "" || p.Namespace == ns:
		return true, nil
	case p.Namespace != "":
		return false, nil
	}
	return s.usesSharedWorkers(ctx, r, ns)
}

// servedNamespaces lists the namespaces whose work w may run (nil = all).
func (s *Service) servedNamespaces(ctx context.Context, r ports.Repos, w *worker.Worker) ([]string, error) {
	if w.Namespace != "" {
		return []string{w.Namespace}, nil
	}
	if s.cfg.Dev {
		return nil, nil
	}
	all, err := r.Namespaces().List(ctx)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, n := range all {
		if n.SharedWorkers {
			out = append(out, n.Name)
		}
	}
	return out, nil
}
