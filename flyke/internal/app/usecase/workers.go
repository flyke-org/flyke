package usecase

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/flyke-org/flyke/flyke/internal/app/ports"
	"github.com/flyke-org/flyke/flyke/internal/domain/task"
	"github.com/flyke-org/flyke/flyke/internal/domain/worker"
	"github.com/flyke-org/flyke/flyke/internal/domain/workflow"
)

var nameRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)

type RegisterInput struct {
	JoinToken    string
	Name         string
	Hostname     string
	Labels       map[string]string
	Slots        int
	Capacity     workflow.Resources
	Runtimes     []string
	AgentVersion string
}

type Registration struct {
	Worker     *worker.Worker
	Credential string
}

// RegisterWorker redeems a join token and creates a worker with its own
// credential. The token is never usable as a worker identity.
func (s *Service) RegisterWorker(ctx context.Context, in RegisterInput) (Registration, error) {
	sec, err := worker.ParseJoinToken(in.JoinToken)
	if err != nil {
		return Registration{}, fmt.Errorf("%w: %v", ErrUnauthenticated, err)
	}
	var out Registration
	err = s.tx(ctx, func(t *txn) error {
		tok, err := t.JoinTokens().Get(ctx, sec.ID)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrUnauthenticated, worker.ErrInvalidToken)
		}
		labels := map[string]string{}
		for k, v := range in.Labels {
			labels[k] = v
		}
		if err := tok.Redeem(sec.Secret, t.now, labels); err != nil {
			if errors.Is(err, worker.ErrLabelDenied) {
				return fmt.Errorf("%w: %v", ErrPermissionDenied, err)
			}
			return fmt.Errorf("%w: %v", ErrUnauthenticated, err)
		}
		if err := t.JoinTokens().Update(ctx, tok); err != nil {
			return err
		}
		labels[worker.PoolLabel] = tok.Pool

		name := sanitizeName(in.Name)
		if name == "" {
			name = sanitizeName(in.Hostname)
			if name == "" {
				name = "worker"
			}
			name = name + "-" + worker.RandomSuffix(5)
		}
		if !nameRe.MatchString(name) {
			return invalid("invalid worker name %q", name)
		}
		// The worker belongs to the tenant that owns the token's pool.
		namespace := ""
		if p, err := t.Pools().Get(ctx, tok.Pool); err == nil {
			namespace = p.Namespace
		} else if !errors.Is(err, ports.ErrNotFound) {
			return err
		}
		if existing, err := t.Workers().Get(ctx, name); err == nil {
			if existing.Namespace != namespace {
				// Never let one tenant replace (or learn about) another's worker.
				if in.Name != "" {
					return fmt.Errorf("%w: worker name %q is not available", ErrPermissionDenied, name)
				}
				return fmt.Errorf("%w: worker name collision, retry", ErrFailedPrecondition)
			}
			// Re-registering under an existing name replaces it (host reinstall).
			if err := t.releaseWorker(existing); err != nil {
				return err
			}
			if err := t.Workers().Delete(ctx, name); err != nil {
				return err
			}
		}
		cred, plain, err := worker.NewCredential()
		if err != nil {
			return err
		}
		slots := in.Slots
		if slots <= 0 {
			slots = 4
		}
		w := &worker.Worker{
			Name: name, Namespace: namespace, Pool: tok.Pool, Hostname: in.Hostname, Labels: labels,
			Runtimes: in.Runtimes, Slots: slots, FreeSlots: slots, Capacity: in.Capacity,
			AgentVersion: in.AgentVersion, Phase: worker.Ready,
			CredentialID: cred.ID, CredentialSum: worker.Hash(cred.Secret),
			LastHeartbeat: t.now, CreatedAt: t.now,
		}
		if err := t.Workers().Create(ctx, w); err != nil {
			return err
		}
		s.log.Info("worker registered", "worker", name, "namespace", namespace, "pool", tok.Pool, "hostname", in.Hostname)
		out = Registration{Worker: w, Credential: plain}
		return nil
	})
	return out, err
}

func sanitizeName(s string) string {
	s = strings.ToLower(s)
	s = regexp.MustCompile(`[^a-z0-9-]+`).ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 50 {
		s = s[:50]
	}
	return s
}

// AuthenticateWorker resolves a worker from its bearer credential.
func (s *Service) AuthenticateWorker(ctx context.Context, credential string) (*worker.Worker, error) {
	sec, err := worker.ParseCredential(credential)
	if err != nil {
		return nil, ErrUnauthenticated
	}
	var w *worker.Worker
	err = s.read(ctx, func(r ports.Repos) error {
		w, err = r.Workers().GetByCredentialID(ctx, sec.ID)
		return err
	})
	if err != nil || !worker.SecretMatches(sec.Secret, w.CredentialSum) {
		return nil, ErrUnauthenticated
	}
	return w, nil
}

type HeartbeatResult struct {
	CancelLeases []string
	Drain        bool
}

// Heartbeat renews the worker's liveness and the leases it reports.
func (s *Service) Heartbeat(ctx context.Context, w *worker.Worker, freeSlots int, leases []string) (HeartbeatResult, error) {
	var res HeartbeatResult
	err := s.tx(ctx, func(t *txn) error {
		cur, err := t.Workers().Get(ctx, w.Name)
		if err != nil {
			return ErrUnauthenticated
		}
		cur.LastHeartbeat = t.now
		cur.Phase = worker.Ready
		cur.FreeSlots = freeSlots
		if err := t.Workers().Update(ctx, cur); err != nil {
			return err
		}
		res.Drain = cur.Draining
		for _, l := range leases {
			item, err := t.Tasks().GetByLease(ctx, l)
			if err != nil || item.Worker != cur.Name || !(item.Phase == task.Scheduled || item.Phase == task.Running) {
				res.CancelLeases = append(res.CancelLeases, l)
				continue
			}
			if item.Phase == task.Running {
				exp := t.now.Add(s.cfg.LeaseTTL)
				item.LeaseExpiresAt = &exp
				if err := t.Tasks().Update(ctx, item); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return res, err
}

// DeregisterWorker is called by a worker leaving gracefully.
func (s *Service) DeregisterWorker(ctx context.Context, w *worker.Worker) error {
	return s.DeleteWorker(ctx, "", w.Name)
}

// ownedWorker loads a worker the caller may manage: ns "" is the admin (any
// worker); a tenant only manages its own workers, and others do not exist
// as far as it can tell.
func (t *txn) ownedWorker(ns, name string) (*worker.Worker, error) {
	w, err := t.Workers().Get(t.ctx, name)
	if err != nil {
		return nil, err
	}
	if ns != "" && w.Namespace != ns {
		if w.Namespace == "" {
			return nil, fmt.Errorf("%w: %s is a shared worker; only the admin manages it", ErrPermissionDenied, name)
		}
		return nil, ports.ErrNotFound
	}
	return w, nil
}

// DeleteWorker removes a worker: its credential stops working immediately and
// its in-flight work is treated as lost.
func (s *Service) DeleteWorker(ctx context.Context, ns, name string) error {
	return s.tx(ctx, func(t *txn) error {
		w, err := t.ownedWorker(ns, name)
		if err != nil {
			return err
		}
		if err := t.releaseWorker(w); err != nil {
			return err
		}
		s.log.Info("worker removed", "worker", name)
		return t.Workers().Delete(ctx, name)
	})
}

// releaseWorker expires all leases held by a worker so the lease controller
// applies delivery guarantees on its next pass.
func (t *txn) releaseWorker(w *worker.Worker) error {
	items, err := t.Tasks().ListActiveByWorker(t.ctx, w.Name)
	if err != nil {
		return err
	}
	past := t.now.Add(-time.Second)
	for _, it := range items {
		it.LeaseExpiresAt = &past
		if err := t.Tasks().Update(t.ctx, it); err != nil {
			return err
		}
	}
	t.notify(ports.TopicReconcile)
	return nil
}

// workerVisible: a tenant sees its own workers, plus shared ones when it may
// run on them.
func (s *Service) workerVisible(ctx context.Context, r ports.Repos, ns string, w *worker.Worker) (bool, error) {
	switch {
	case ns == "" || w.Namespace == ns:
		return true, nil
	case w.Namespace != "":
		return false, nil
	}
	return s.usesSharedWorkers(ctx, r, ns)
}

func (s *Service) GetWorker(ctx context.Context, ns, name string) (w *worker.Worker, active int, err error) {
	err = s.read(ctx, func(r ports.Repos) error {
		w, err = r.Workers().Get(ctx, name)
		if err != nil {
			return err
		}
		if ok, err := s.workerVisible(ctx, r, ns, w); err != nil {
			return err
		} else if !ok {
			return ports.ErrNotFound
		}
		items, err := r.Tasks().ListActiveByWorker(ctx, name)
		active = len(items)
		return err
	})
	return
}

// ListWorkers returns the workers ns can see (ns "" = all, for the admin).
func (s *Service) ListWorkers(ctx context.Context, ns string) (out []*worker.Worker, err error) {
	err = s.read(ctx, func(r ports.Repos) error {
		all, err := r.Workers().List(ctx)
		if err != nil || ns == "" {
			out = all
			return err
		}
		shared, err := s.usesSharedWorkers(ctx, r, ns)
		if err != nil {
			return err
		}
		for _, w := range all {
			if w.Namespace == ns || (w.Namespace == "" && shared) {
				out = append(out, w)
			}
		}
		return nil
	})
	return
}

// SetWorkerSchedulable cordons / uncordons a worker; drain also asks it to exit.
func (s *Service) SetWorkerSchedulable(ctx context.Context, ns, name string, schedulable, drain bool) (*worker.Worker, error) {
	var out *worker.Worker
	err := s.tx(ctx, func(t *txn) error {
		w, err := t.ownedWorker(ns, name)
		if err != nil {
			return err
		}
		w.Unschedulable = !schedulable
		w.Draining = drain
		out = w
		return t.Workers().Update(ctx, w)
	})
	return out, err
}

// --------------------------------------------------------------- pools ----
//
// Pool names are unique across the control plane. A pool belongs to the
// namespace it was created in; pools created by the admin outside any
// namespace (ns "") are shared.

// ownedPool loads a pool ns may change (ns "" = the admin: any pool).
func (t *txn) ownedPool(ns, name string) (*worker.Pool, error) {
	p, err := t.Pools().Get(t.ctx, name)
	if err != nil {
		return nil, err
	}
	if ns != "" && p.Namespace != ns {
		if p.Namespace == "" {
			return nil, fmt.Errorf("%w: %s is a shared pool; only the admin manages it", ErrPermissionDenied, name)
		}
		return nil, fmt.Errorf("%w: pool name %q is not available", ErrAlreadyExists, name)
	}
	return p, nil
}

func (s *Service) PutPool(ctx context.Context, ns string, p *worker.Pool) error {
	if !nameRe.MatchString(p.Name) {
		return invalid("invalid pool name %q", p.Name)
	}
	return s.tx(ctx, func(t *txn) error {
		existing, err := t.ownedPool(ns, p.Name)
		switch {
		case err == nil:
			p.CreatedAt, p.Namespace = existing.CreatedAt, existing.Namespace
		case errors.Is(err, ports.ErrNotFound):
			p.CreatedAt, p.Namespace = t.now, ns
		default:
			return err
		}
		t.notify(ports.TopicWork)
		return t.Pools().Put(ctx, p)
	})
}

func (s *Service) GetPool(ctx context.Context, ns, name string) (p *worker.Pool, err error) {
	err = s.read(ctx, func(r ports.Repos) error {
		p, err = r.Pools().Get(ctx, name)
		if err != nil {
			return err
		}
		if ok, err := s.poolVisible(ctx, r, ns, p); err != nil {
			return err
		} else if !ok {
			return ports.ErrNotFound
		}
		return nil
	})
	return
}

// ListPools returns the pools ns can use (ns "" = all, for the admin).
func (s *Service) ListPools(ctx context.Context, ns string) (out []*worker.Pool, err error) {
	err = s.read(ctx, func(r ports.Repos) error {
		all, err := r.Pools().List(ctx)
		if err != nil {
			return err
		}
		for _, p := range all {
			ok, err := s.poolVisible(ctx, r, ns, p)
			if err != nil {
				return err
			}
			if ok {
				out = append(out, p)
			}
		}
		return nil
	})
	return
}

func (s *Service) DeletePool(ctx context.Context, ns, name string) error {
	return s.tx(ctx, func(t *txn) error {
		if _, err := t.ownedPool(ns, name); err != nil {
			if errors.Is(err, ErrAlreadyExists) {
				return ports.ErrNotFound
			}
			return err
		}
		return t.Pools().Delete(ctx, name)
	})
}

// CreateJoinToken issues a pool-scoped bootstrap token. The pool is created
// in ns if it does not exist; workers that redeem the token belong to the
// pool's namespace.
func (s *Service) CreateJoinToken(ctx context.Context, ns, pool string, ttl time.Duration, maxUses int, allowed map[string]string) (worker.JoinToken, string, error) {
	if !nameRe.MatchString(pool) {
		return worker.JoinToken{}, "", invalid("invalid pool name %q", pool)
	}
	if ttl <= 0 {
		ttl = s.cfg.DefaultJoinTokenTTL
	}
	var tok worker.JoinToken
	var plain string
	err := s.tx(ctx, func(t *txn) error {
		if _, err := t.ownedPool(ns, pool); errors.Is(err, ports.ErrNotFound) {
			if err := t.Pools().Put(ctx, &worker.Pool{Name: pool, Namespace: ns, CreatedAt: t.now}); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		var err error
		tok, plain, err = worker.NewJoinToken(pool, t.now, ttl, maxUses, allowed)
		if err != nil {
			return err
		}
		return t.JoinTokens().Create(ctx, tok)
	})
	return tok, plain, err
}

func (s *Service) ListJoinTokens(ctx context.Context, ns, pool string) (out []worker.JoinToken, err error) {
	err = s.tx(ctx, func(t *txn) error {
		if _, err := t.ownedPool(ns, pool); err != nil {
			if errors.Is(err, ErrAlreadyExists) {
				return ports.ErrNotFound
			}
			return err
		}
		out, err = t.JoinTokens().List(ctx, pool)
		return err
	})
	return
}

func (s *Service) DeleteJoinToken(ctx context.Context, ns, pool, id string) error {
	return s.tx(ctx, func(t *txn) error {
		tok, err := t.JoinTokens().Get(ctx, id)
		if err != nil || tok.Pool != pool {
			return ports.ErrNotFound
		}
		if _, err := t.ownedPool(ns, pool); err != nil {
			if errors.Is(err, ErrAlreadyExists) {
				return ports.ErrNotFound
			}
			return err
		}
		return t.JoinTokens().Delete(ctx, id)
	})
}
