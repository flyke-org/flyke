// Package usecase implements the control plane's application logic. Every
// public method is one use case; it orchestrates domain objects and ports
// inside a single transaction. Adapters (REST, worker protocol, CLI) are thin
// translators on top of this package.
package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/flyke-org/flyke/flyke/internal/app/ports"
	"github.com/flyke-org/flyke/flyke/internal/app/scheduler"
	"github.com/flyke-org/flyke/flyke/internal/domain/run"
	"github.com/flyke-org/flyke/flyke/internal/domain/task"
)

// Application errors. Adapters map them to HTTP / RPC status codes.
var (
	ErrNotFound           = ports.ErrNotFound
	ErrAlreadyExists      = ports.ErrAlreadyExists
	ErrInvalidArgument    = errors.New("invalid argument")
	ErrFailedPrecondition = errors.New("failed precondition")
	ErrUnauthenticated    = errors.New("unauthenticated")
	ErrPermissionDenied   = errors.New("permission denied")
)

type Config struct {
	// Dev relaxes scheduling (unknown resource profiles are ignored).
	Dev bool
	// LeaseTTL is how long a running lease survives without a heartbeat.
	LeaseTTL time.Duration
	// AckTTL is how long a worker has to acknowledge an assignment.
	AckTTL time.Duration
	// HeartbeatInterval is advertised to workers.
	HeartbeatInterval time.Duration
	// WorkerNotReadyAfter marks a silent worker NotReady.
	WorkerNotReadyAfter time.Duration
	// MaxPollWait caps long-poll duration.
	MaxPollWait time.Duration
	// DefaultJoinTokenTTL applies when a token request has no TTL.
	DefaultJoinTokenTTL time.Duration
}

func (c *Config) withDefaults() {
	if c.LeaseTTL == 0 {
		c.LeaseTTL = 30 * time.Second
	}
	if c.AckTTL == 0 {
		c.AckTTL = 15 * time.Second
	}
	if c.HeartbeatInterval == 0 {
		c.HeartbeatInterval = 5 * time.Second
	}
	if c.WorkerNotReadyAfter == 0 {
		c.WorkerNotReadyAfter = 3 * c.HeartbeatInterval
	}
	if c.MaxPollWait == 0 {
		c.MaxPollWait = 30 * time.Second
	}
	if c.DefaultJoinTokenTTL == 0 {
		c.DefaultJoinTokenTTL = time.Hour
	}
}

type Service struct {
	store     ports.Store
	blobs     ports.BlobStore
	notifier  ports.Notifier
	clock     ports.Clock
	ids       ports.IDGenerator
	cipher    ports.Cipher
	scheduler *scheduler.Scheduler
	cfg       Config
	log       *slog.Logger

	schemas sync.Map // workflow version -> *compiledSchema
}

type Deps struct {
	Store    ports.Store
	Blobs    ports.BlobStore
	Notifier ports.Notifier
	Clock    ports.Clock
	IDs      ports.IDGenerator
	// Cipher encrypts sensitive secrets. Without one, creating a sensitive
	// secret fails.
	Cipher ports.Cipher
	Logger *slog.Logger
}

func New(d Deps, cfg Config) *Service {
	cfg.withDefaults()
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Service{
		store: d.Store, blobs: d.Blobs, notifier: d.Notifier, clock: d.Clock, ids: d.IDs, cipher: d.Cipher,
		scheduler: scheduler.New(scheduler.DefaultFilters(cfg.Dev)...),
		cfg:       cfg, log: d.Logger,
	}
}

func (s *Service) Config() Config { return s.cfg }

// txn wraps a store transaction and defers notifications until commit, so
// that woken readers always observe committed state.
type txn struct {
	ports.Repos
	s      *Service
	ctx    context.Context
	topics map[string]struct{}
	now    time.Time
}

func (t *txn) notify(topics ...string) {
	for _, tp := range topics {
		t.topics[tp] = struct{}{}
	}
}

func (s *Service) tx(ctx context.Context, fn func(t *txn) error) error {
	var topics map[string]struct{}
	err := s.store.Tx(ctx, func(r ports.Repos) error {
		t := &txn{Repos: r, s: s, ctx: ctx, topics: map[string]struct{}{}, now: s.clock.Now().UTC()}
		if err := fn(t); err != nil {
			return err
		}
		topics = t.topics
		return nil
	})
	if err != nil {
		return err
	}
	for tp := range topics {
		s.notifier.Notify(tp)
	}
	return nil
}

func (s *Service) read(ctx context.Context, fn func(r ports.Repos) error) error {
	return s.store.Read(ctx, fn)
}

// ------------------------------------------------------------- helpers ----

// syslog records a control-plane log line in a run's log stream.
func (t *txn) syslog(r *run.Run, level, msg string, fields map[string]any, item *task.TaskRun) {
	e := ports.LogEntry{
		Namespace: r.Namespace, RunID: r.ID, Time: t.now, Level: level,
		Source: "system", Message: msg,
	}
	if len(fields) > 0 {
		e.Fields, _ = json.Marshal(fields)
	}
	if item != nil {
		e.WorkID, e.Task, e.Ref, e.Attempt = item.ID, item.Name, item.Ref, item.Attempt
	}
	if _, err := t.Logs().Append(t.ctx, e); err != nil {
		t.s.log.Warn("system log append failed", "run", r.ID, "err", err)
	}
	t.notify(ports.TopicRun(r.ID))
}

// appendHistory appends events and keeps the run's history length in sync.
func (t *txn) appendHistory(r *run.Run, evs ...run.HistoryEvent) error {
	for i := range evs {
		if evs[i].Time.IsZero() {
			evs[i].Time = t.now
		}
	}
	stored, err := t.History().Append(t.ctx, r.ID, evs...)
	if err != nil {
		return err
	}
	if len(stored) > 0 {
		r.HistoryLength = stored[len(stored)-1].ID
	}
	return nil
}

// saveRun persists a run and wakes interested parties.
func (t *txn) saveRun(r *run.Run) error {
	if err := t.Runs().Update(t.ctx, r); err != nil {
		return err
	}
	t.notify(ports.TopicRun(r.ID), ports.TopicReconcile)
	return nil
}

func (t *txn) saveTask(item *task.TaskRun) error {
	if err := t.Tasks().Update(t.ctx, item); err != nil {
		return err
	}
	if item.Phase == task.Pending {
		t.notify(ports.TopicWork)
	}
	t.notify(ports.TopicRun(item.RunID))
	return nil
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidArgument, fmt.Sprintf(format, args...))
}

func precondition(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrFailedPrecondition, fmt.Sprintf(format, args...))
}

func ptrTime(t time.Time) *time.Time { return &t }

// Notifier exposes the notifier to the controller manager.
func (s *Service) Notifier() ports.Notifier { return s.notifier }
