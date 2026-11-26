// Package run models workflow runs, their event-sourced history and the
// commands a workflow step produces. Transitions are pure functions.
package run

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flyke-org/flyke/flyke/internal/domain/workflow"
)

type Phase string

const (
	Pending      Phase = "Pending"      // created, not admitted yet
	Queued       Phase = "Queued"       // waiting for a concurrency slot
	Running      Phase = "Running"      // tasks or steps in flight
	Waiting      Phase = "Waiting"      // only timers / external events outstanding
	Paused       Phase = "Paused"       // needs a human (non-determinism, unknown outcome)
	Compensating Phase = "Compensating" // running compensations before a terminal phase
	Succeeded    Phase = "Succeeded"
	Failed       Phase = "Failed"
	Cancelled    Phase = "Cancelled"
	TimedOut     Phase = "TimedOut"
	Skipped      Phase = "Skipped"
)

// Terminal phases never change again.
func (p Phase) Terminal() bool {
	switch p {
	case Succeeded, Failed, Cancelled, TimedOut, Skipped:
		return true
	}
	return false
}

// Active phases hold a concurrency slot.
func (p Phase) Active() bool {
	switch p {
	case Running, Waiting, Paused, Compensating:
		return true
	}
	return false
}

// Compensation progress of a run.
const (
	CompensationNone      = ""
	CompensationRunning   = "Running"
	CompensationCompleted = "Completed"
	CompensationFailed    = "Failed"
)

var (
	ErrInvalidTransition = errors.New("invalid run transition")
	ErrConflict          = errors.New("conflict")
)

type Run struct {
	ID             string
	Namespace      string
	Workflow       string
	Version        string
	Inputs         json.RawMessage
	Labels         map[string]string
	Phase          Phase
	Reason         string
	Message        string
	ConcurrencyKey string
	IdempotencyKey string
	TriggeredBy    string
	// Retry is set when this run retries another one, reusing its outcomes.
	Retry   *RetryPlan
	Result  json.RawMessage
	Failure *workflow.Failure
	// NeedsStep is set when history changed and the workflow body must advance.
	NeedsStep bool
	// TerminalCause is the phase to enter once compensation finishes.
	TerminalCause Phase
	Compensation  string
	HistoryLength int
	CreatedAt     time.Time
	StartedAt     *time.Time
	FinishedAt    *time.Time
	DeadlineAt    *time.Time
	// ResourceVersion implements optimistic concurrency in the store.
	ResourceVersion int64
}

// Admit moves a pending/queued run to Running.
func (r *Run) Admit(now time.Time, timeout time.Duration) error {
	if r.Phase != Pending && r.Phase != Queued {
		return fmt.Errorf("%w: admit from %s", ErrInvalidTransition, r.Phase)
	}
	r.Phase = Running
	r.Reason = "Admitted"
	r.Message = ""
	r.NeedsStep = true
	r.StartedAt = &now
	if timeout > 0 {
		d := now.Add(timeout)
		r.DeadlineAt = &d
	}
	return nil
}

func (r *Run) Queue(reason string) error {
	if r.Phase != Pending && r.Phase != Queued {
		return fmt.Errorf("%w: queue from %s", ErrInvalidTransition, r.Phase)
	}
	r.Phase, r.Reason = Queued, "ConcurrencyLimit"
	r.Message = reason
	return nil
}

func (r *Run) Skip(now time.Time, reason string) error {
	if r.Phase != Pending && r.Phase != Queued {
		return fmt.Errorf("%w: skip from %s", ErrInvalidTransition, r.Phase)
	}
	r.Phase, r.Reason, r.Message = Skipped, "ConcurrencyLimit", reason
	r.FinishedAt = &now
	return nil
}

func (r *Run) Succeed(now time.Time, result json.RawMessage) error {
	if r.Phase.Terminal() || r.Phase == Compensating {
		return fmt.Errorf("%w: succeed from %s", ErrInvalidTransition, r.Phase)
	}
	r.Phase, r.Reason, r.Message = Succeeded, "Completed", ""
	r.Result = result
	r.NeedsStep = false
	r.FinishedAt = &now
	return nil
}

// Finish ends the run with a non-success phase. If compensable work exists the
// run first enters Compensating and records the intended terminal phase.
func (r *Run) Finish(now time.Time, phase Phase, reason, message string, failure *workflow.Failure, needsCompensation bool) error {
	if r.Phase.Terminal() {
		return fmt.Errorf("%w: finish from %s", ErrInvalidTransition, r.Phase)
	}
	switch phase {
	case Failed, Cancelled, TimedOut:
	default:
		return fmt.Errorf("%w: finish into %s", ErrInvalidTransition, phase)
	}
	r.Reason, r.Message, r.Failure = reason, message, failure
	r.NeedsStep = false
	if needsCompensation && r.Phase != Compensating {
		r.Phase = Compensating
		r.TerminalCause = phase
		r.Compensation = CompensationRunning
		return nil
	}
	r.Phase = phase
	r.FinishedAt = &now
	return nil
}

// CompleteCompensation leaves Compensating for the recorded terminal phase.
func (r *Run) CompleteCompensation(now time.Time, ok bool) error {
	if r.Phase != Compensating {
		return fmt.Errorf("%w: complete compensation from %s", ErrInvalidTransition, r.Phase)
	}
	r.Phase = r.TerminalCause
	if ok {
		r.Compensation = CompensationCompleted
	} else {
		r.Compensation = CompensationFailed
	}
	r.FinishedAt = &now
	return nil
}

func (r *Run) Pause(reason, message string) error {
	if r.Phase.Terminal() || r.Phase == Compensating {
		return fmt.Errorf("%w: pause from %s", ErrInvalidTransition, r.Phase)
	}
	r.Phase, r.Reason, r.Message = Paused, reason, message
	r.NeedsStep = false
	return nil
}

func (r *Run) Resume() error {
	if r.Phase != Paused {
		return fmt.Errorf("%w: resume from %s", ErrInvalidTransition, r.Phase)
	}
	r.Phase, r.Reason, r.Message = Running, "Resumed", ""
	r.NeedsStep = true
	return nil
}

// Wake marks that history changed and the workflow body must advance.
func (r *Run) Wake() {
	if r.Phase == Running || r.Phase == Waiting {
		r.Phase = Running
		r.NeedsStep = true
	}
}

// Settle sets Running/Waiting depending on outstanding work.
func (r *Run) Settle(activeWork bool) {
	if r.Phase != Running && r.Phase != Waiting {
		return
	}
	if activeWork || r.NeedsStep {
		r.Phase = Running
	} else {
		r.Phase = Waiting
	}
}
