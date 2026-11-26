// Package ports declares the interfaces the application layer depends on.
// Adapters (SQLite, Postgres, in-memory notifier, filesystem blobs…) implement
// them; the application never imports an adapter.
package ports

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/flyke-org/flyke/flyke/internal/domain/event"
	"github.com/flyke-org/flyke/flyke/internal/domain/run"
	"github.com/flyke-org/flyke/flyke/internal/domain/schedule"
	"github.com/flyke-org/flyke/flyke/internal/domain/secret"
	"github.com/flyke-org/flyke/flyke/internal/domain/task"
	"github.com/flyke-org/flyke/flyke/internal/domain/tenant"
	"github.com/flyke-org/flyke/flyke/internal/domain/worker"
	"github.com/flyke-org/flyke/flyke/internal/domain/workflow"
)

var (
	ErrNotFound      = errors.New("not found")
	ErrAlreadyExists = errors.New("already exists")
	// ErrConflict signals an optimistic-concurrency failure (stale resourceVersion).
	ErrConflict = errors.New("conflict")
)

// Store gives transactional access to all repositories.
type Store interface {
	// Tx runs fn in a transaction; returning an error rolls it back.
	Tx(ctx context.Context, fn func(Repos) error) error
	// Read runs fn outside an explicit transaction (read-only use).
	Read(ctx context.Context, fn func(Repos) error) error
	Close() error
}

type Repos interface {
	Workflows() WorkflowRepo
	Runs() RunRepo
	History() HistoryRepo
	Tasks() TaskRepo
	Waits() WaitRepo
	Events() EventRepo
	Workers() WorkerRepo
	Pools() PoolRepo
	JoinTokens() JoinTokenRepo
	Logs() LogRepo
	Schedules() ScheduleRepo
	Secrets() SecretRepo
	Namespaces() NamespaceRepo
	AccessTokens() AccessTokenRepo
	Bundles() BundleRepo
}

type NamespaceRepo interface {
	// Create fails with ErrAlreadyExists if the name is taken.
	Create(ctx context.Context, n tenant.Namespace) error
	Get(ctx context.Context, name string) (tenant.Namespace, error)
	List(ctx context.Context) ([]tenant.Namespace, error)
	Update(ctx context.Context, n tenant.Namespace) error
	// Delete removes the namespace and everything it owns: workflows, schedules,
	// runs (with history, tasks, waits, logs), events, secrets, access tokens,
	// bundle ownership, pools (with join tokens) and workers.
	Delete(ctx context.Context, name string) error
	Stats(ctx context.Context, name string) (tenant.Stats, error)
}

type AccessTokenRepo interface {
	Create(ctx context.Context, t tenant.AccessToken) error
	Get(ctx context.Context, id string) (tenant.AccessToken, error)
	List(ctx context.Context, ns string) ([]tenant.AccessToken, error)
	Delete(ctx context.Context, ns, id string) error
}

// BundleRepo records which namespaces may use a content-addressed bundle.
type BundleRepo interface {
	// AddOwner is idempotent. ns "" makes the bundle usable by any namespace.
	AddOwner(ctx context.Context, ns, digest string, now time.Time) error
	// Usable reports whether ns owns the bundle or it is usable by any.
	Usable(ctx context.Context, ns, digest string) (bool, error)
	// UploadedAt is when ns (or the admin) first uploaded the bundle; nil if never.
	UploadedAt(ctx context.Context, ns, digest string) (*time.Time, error)
}

type WorkflowRepo interface {
	Put(ctx context.Context, wf workflow.Workflow) error
	Get(ctx context.Context, ns, name, version string) (workflow.Workflow, error)
	GetLatest(ctx context.Context, ns, name string) (workflow.Workflow, error)
	// ListLatest returns the latest version of every workflow ("" = all namespaces).
	ListLatest(ctx context.Context, ns string) ([]workflow.Workflow, error)
	// ListVersions returns every version of a workflow, current first, then
	// in order of their last activation.
	ListVersions(ctx context.Context, ns, name string) ([]workflow.Workflow, error)
}

// VersionRunStats summarizes the runs of one workflow version.
type VersionRunStats struct {
	Total      int
	Phases     map[run.Phase]int
	FirstRunAt *time.Time
	LastRunAt  *time.Time
}

type RunFilter struct {
	Namespace string
	Workflow  string
	// Version restricts the list to runs of one workflow version.
	Version string
	Phases  []run.Phase
	// RetryOf lists the retries of a run.
	RetryOf   string
	NeedsStep *bool
	Limit     int
}

type RunRepo interface {
	Create(ctx context.Context, r *run.Run) error
	Get(ctx context.Context, ns, id string) (*run.Run, error)
	// GetByID looks up a run in any namespace (worker-facing paths).
	GetByID(ctx context.Context, id string) (*run.Run, error)
	// Update persists r if r.ResourceVersion matches, then increments it.
	Update(ctx context.Context, r *run.Run) error
	List(ctx context.Context, f RunFilter) ([]*run.Run, error)
	FindByIdempotencyKey(ctx context.Context, ns, workflow, key string) (*run.Run, error)
	// ListByConcurrencyKey returns non-terminal runs for a key, oldest first.
	ListByConcurrencyKey(ctx context.Context, ns, workflow, key string) ([]*run.Run, error)
	// ListPastDeadline returns active runs whose deadline is before now.
	ListPastDeadline(ctx context.Context, now time.Time) ([]*run.Run, error)
	// VersionStats counts the runs of a workflow per version.
	VersionStats(ctx context.Context, ns, workflow string) (map[string]*VersionRunStats, error)
}

type HistoryRepo interface {
	// Append assigns consecutive ids after the current last event.
	Append(ctx context.Context, runID string, evs ...run.HistoryEvent) ([]run.HistoryEvent, error)
	List(ctx context.Context, runID string) ([]run.HistoryEvent, error)
}

type TaskRepo interface {
	Create(ctx context.Context, t *task.TaskRun) error
	Get(ctx context.Context, id string) (*task.TaskRun, error)
	GetByLease(ctx context.Context, leaseID string) (*task.TaskRun, error)
	Update(ctx context.Context, t *task.TaskRun) error
	ListByRun(ctx context.Context, runID string) ([]*task.TaskRun, error)
	// ListPending returns runnable items (Pending, NotBefore <= now), oldest
	// first. namespaces restricts the result (nil = every namespace), so one
	// tenant's backlog never hides another tenant's work from a worker.
	ListPending(ctx context.Context, now time.Time, limit int, namespaces []string) ([]*task.TaskRun, error)
	// ListExpiredLeases returns Scheduled/Running items whose lease expired.
	ListExpiredLeases(ctx context.Context, now time.Time) ([]*task.TaskRun, error)
	// ListPastDeadline returns Running items past their attempt deadline.
	ListPastDeadline(ctx context.Context, now time.Time) ([]*task.TaskRun, error)
	ListActiveByWorker(ctx context.Context, worker string) ([]*task.TaskRun, error)
	FindSucceededByIdempotencyKey(ctx context.Context, ns, key string) (*task.TaskRun, error)
	CountActiveInPool(ctx context.Context, pool string) (int, error)
}

type WaitRepo interface {
	Create(ctx context.Context, w run.Wait) error
	ListOpenEvent(ctx context.Context, ns, name string) ([]run.Wait, error)
	// ListDue returns open waits whose deadline is before now.
	ListDue(ctx context.Context, now time.Time) ([]run.Wait, error)
	MarkDone(ctx context.Context, runID string, ref int) error
	CloseAll(ctx context.Context, runID string) error
	CountOpen(ctx context.Context, runID string) (int, error)
}

type EventRepo interface {
	Create(ctx context.Context, e event.Event) error
	List(ctx context.Context, ns, name string, limit int) ([]event.Event, error)
}

type WorkerRepo interface {
	Create(ctx context.Context, w *worker.Worker) error
	Get(ctx context.Context, name string) (*worker.Worker, error)
	GetByCredentialID(ctx context.Context, id string) (*worker.Worker, error)
	Update(ctx context.Context, w *worker.Worker) error
	List(ctx context.Context) ([]*worker.Worker, error)
	Delete(ctx context.Context, name string) error
}

type PoolRepo interface {
	Put(ctx context.Context, p *worker.Pool) error
	Get(ctx context.Context, name string) (*worker.Pool, error)
	List(ctx context.Context) ([]*worker.Pool, error)
	Delete(ctx context.Context, name string) error
}

type JoinTokenRepo interface {
	Create(ctx context.Context, t worker.JoinToken) error
	Get(ctx context.Context, id string) (worker.JoinToken, error)
	Update(ctx context.Context, t worker.JoinToken) error
	List(ctx context.Context, pool string) ([]worker.JoinToken, error)
	Delete(ctx context.Context, id string) error
}

// SecretRepo stores secrets keyed by (namespace, key).
type SecretRepo interface {
	// Create fails with ErrAlreadyExists if the key is already set.
	Create(ctx context.Context, s secret.Secret) error
	// Put inserts or replaces the secret.
	Put(ctx context.Context, s secret.Secret) error
	Get(ctx context.Context, ns, key string) (secret.Secret, error)
	// List returns the secrets stored in exactly ns, ordered by key
	// (ns secret.SharedNamespace lists Shared secrets).
	List(ctx context.Context, ns string) ([]secret.Secret, error)
	Delete(ctx context.Context, ns, key string) error
}

type LogEntry struct {
	ID        int64
	Namespace string
	RunID     string
	WorkID    string
	Task      string
	Ref       int
	Attempt   int
	Worker    string
	Seq       int64
	Time      time.Time
	Level     string
	Source    string
	Stream    string
	Message   string
	Fields    json.RawMessage
}

type LogRepo interface {
	// Append stores entries, ignoring duplicates of (WorkID, Attempt, Seq).
	Append(ctx context.Context, entries ...LogEntry) (int, error)
	List(ctx context.Context, runID string, afterID int64, limit int) ([]LogEntry, error)
}

type ScheduleRepo interface {
	// Put inserts or replaces a schedule.
	Put(ctx context.Context, s *schedule.Schedule) error
	Get(ctx context.Context, ns, name string) (*schedule.Schedule, error)
	// List returns a namespace's schedules by name ("" workflow = all of them).
	List(ctx context.Context, ns, workflow string) ([]*schedule.Schedule, error)
	Delete(ctx context.Context, ns, name string) error
	// ListDue returns schedules whose next tick is at or before now,
	// earliest first. Paused schedules have no next tick.
	ListDue(ctx context.Context, now time.Time, limit int) ([]*schedule.Schedule, error)
	// NextDue returns the earliest next tick of all schedules (zero = none).
	NextDue(ctx context.Context) (time.Time, error)
}

// BlobStore stores content-addressed code bundles.
type BlobStore interface {
	Put(ctx context.Context, r io.Reader) (digest string, size int64, err error)
	Get(ctx context.Context, digest string) (io.ReadCloser, error)
}

// Notifier wakes up long-polls, log followers and controllers.
type Notifier interface {
	Notify(topic string)
	// Subscribe returns a channel that receives a value on each notification
	// (coalesced) and a cancel func.
	Subscribe(topic string) (<-chan struct{}, func())
}

// Topics used across the application.
const (
	TopicWork      = "work"
	TopicReconcile = "reconcile"
	// TopicSchedules wakes the schedules controller when a schedule changes.
	TopicSchedules = "schedules"
)

func TopicRun(runID string) string { return "run:" + runID }

type Clock interface{ Now() time.Time }

// Cipher encrypts sensitive values at rest. aad binds a ciphertext to its
// context and must be identical for Encrypt and Decrypt.
type Cipher interface {
	Encrypt(plain, aad []byte) (string, error)
	Decrypt(ciphertext string, aad []byte) ([]byte, error)
}

type IDGenerator interface {
	// NewID returns a short, unique, URL-safe identifier with the given prefix.
	NewID(prefix string) string
}
