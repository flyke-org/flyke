// Package task models work items: workflow steps, task attempts and
// compensations, all scheduled onto workers under a lease.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flyke-org/flyke/flyke/internal/domain/workflow"
)

type Kind string

const (
	KindStep         Kind = "step"
	KindTask         Kind = "task"
	KindCompensation Kind = "compensation"
)

type Phase string

const (
	Pending   Phase = "Pending"   // waiting for a worker (respecting NotBefore)
	Scheduled Phase = "Scheduled" // bound to a worker, not acknowledged yet
	Running   Phase = "Running"   // user code may be executing
	Succeeded Phase = "Succeeded"
	Failed    Phase = "Failed"
	Unknown   Phase = "Unknown" // lease lost while Running; needs a decision
	Cancelled Phase = "Cancelled"
)

func (p Phase) Active() bool { return p == Pending || p == Scheduled || p == Running }
func (p Phase) Terminal() bool {
	return p == Succeeded || p == Failed || p == Cancelled
}

var (
	ErrInvalidTransition = errors.New("invalid task transition")
	ErrStaleLease        = errors.New("stale or unknown lease")
)

// WorkerLost is the failure recorded when a lease expires mid-attempt.
var WorkerLost = workflow.Failure{Type: "flyke.WorkerLost", MRO: []string{"flyke.WorkerLost"}, Message: "worker lost while the attempt was running"}

// Timeout is the failure recorded when an attempt exceeds its deadline.
var Timeout = workflow.Failure{Type: "flyke.TaskTimeout", MRO: []string{"flyke.TaskTimeout", "builtins.TimeoutError"}, Message: "attempt exceeded its timeout"}

type TaskRun struct {
	ID        string
	RunID     string
	Namespace string
	Kind      Kind
	// Ref is the command ref (task) or the compensated task's ref (compensation).
	Ref  int
	Name string
	// CompensatedTask is the task being undone (compensations only).
	CompensatedTask string
	Args            json.RawMessage
	// Result: task result, or the compensated task's result for compensations.
	Result         json.RawMessage
	Attempt        int
	Phase          Phase
	Worker         string
	LeaseID        string
	LeaseExpiresAt *time.Time
	NotBefore      time.Time
	Deadline       *time.Time
	IdempotencyKey string
	Guarantee      workflow.Guarantee
	Requests       workflow.Resources
	Pool           string
	// StepHistoryLength is the history length a step was dispatched with.
	StepHistoryLength int
	// ReusedFrom is the run whose recorded result this task reused (retries).
	ReusedFrom string
	Failure    *workflow.Failure
	CreatedAt  time.Time
	StartedAt  *time.Time
	FinishedAt *time.Time
}

// Bind assigns the item to a worker under a new lease.
func (t *TaskRun) Bind(worker, leaseID string, now time.Time, ackTTL time.Duration, timeout time.Duration) error {
	if t.Phase != Pending {
		return fmt.Errorf("%w: bind from %s", ErrInvalidTransition, t.Phase)
	}
	t.Worker, t.LeaseID = worker, leaseID
	exp := now.Add(ackTTL)
	t.LeaseExpiresAt = &exp
	if timeout > 0 {
		d := now.Add(timeout)
		t.Deadline = &d
	} else {
		t.Deadline = nil
	}
	if t.Kind == KindStep {
		// Steps are pure replays: no ack needed, they are safe to re-run.
		t.Phase = Running
		t.StartedAt = &now
		return nil
	}
	t.Phase = Scheduled
	return nil
}

// Ack marks that the worker is about to run user code.
func (t *TaskRun) Ack(leaseID string, now time.Time, leaseTTL time.Duration) error {
	if t.LeaseID != leaseID || t.Phase != Scheduled {
		return ErrStaleLease
	}
	t.Phase = Running
	t.StartedAt = &now
	exp := now.Add(leaseTTL)
	t.LeaseExpiresAt = &exp
	return nil
}

// CheckLease is the fencing check for every worker report.
func (t *TaskRun) CheckLease(leaseID, worker string) error {
	if t.LeaseID != leaseID || t.Worker != worker || !t.Phase.Active() || t.Phase == Pending {
		return ErrStaleLease
	}
	return nil
}

func (t *TaskRun) Succeed(now time.Time, result json.RawMessage) {
	t.Phase = Succeeded
	if t.Kind != KindCompensation {
		t.Result = result
	}
	t.Failure = nil
	t.FinishedAt = &now
	t.LeaseExpiresAt = nil
}

// Fail ends the item permanently.
func (t *TaskRun) Fail(now time.Time, f workflow.Failure) {
	t.Phase = Failed
	t.Failure = &f
	t.FinishedAt = &now
	t.LeaseExpiresAt = nil
}

// Retry schedules a new attempt after delay.
func (t *TaskRun) Retry(now time.Time, f workflow.Failure, delay time.Duration) {
	t.Phase = Pending
	t.Failure = &f
	t.Attempt++
	t.Worker, t.LeaseID = "", ""
	t.LeaseExpiresAt, t.Deadline, t.StartedAt = nil, nil, nil
	t.NotBefore = now.Add(delay)
}

// Requeue returns an item to Pending without consuming an attempt
// (used when the worker never started it, or for steps).
func (t *TaskRun) Requeue(now time.Time) {
	t.Phase = Pending
	t.Worker, t.LeaseID = "", ""
	t.LeaseExpiresAt, t.Deadline, t.StartedAt = nil, nil, nil
	t.NotBefore = now
}

func (t *TaskRun) MarkUnknown(now time.Time) {
	t.Phase = Unknown
	f := WorkerLost
	t.Failure = &f
	t.LeaseExpiresAt = nil
}

func (t *TaskRun) Cancel(now time.Time) {
	if t.Phase.Terminal() {
		return
	}
	t.Phase = Cancelled
	t.FinishedAt = &now
	t.LeaseExpiresAt = nil
}

// LostOutcome is the decision taken when a running attempt's lease expires.
type LostOutcome int

const (
	LostRetry LostOutcome = iota // treat as a retryable failure
	LostPause                    // keep Unknown, pause the run for a human
	LostFail                     // fail permanently
)

// OnLeaseLost applies the delivery guarantee to an unknown outcome.
func OnLeaseLost(spec workflow.TaskSpec) LostOutcome {
	if spec.Guarantee != workflow.AtMostOnce {
		return LostRetry
	}
	switch spec.OnUnknown {
	case workflow.OnUnknownPause:
		return LostPause
	default:
		return LostFail
	}
}
