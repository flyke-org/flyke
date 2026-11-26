package usecase

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flyke-org/flyke/flyke/internal/app/ports"
	"github.com/flyke-org/flyke/flyke/internal/domain/run"
	"github.com/flyke-org/flyke/flyke/internal/domain/task"
	"github.com/flyke-org/flyke/flyke/internal/domain/worker"
	"github.com/flyke-org/flyke/flyke/internal/domain/workflow"
)

// AckTask confirms a worker is about to execute user code for a lease.
// proceed=false means the worker must not run it.
func (s *Service) AckTask(ctx context.Context, w *worker.Worker, leaseID string) (bool, error) {
	proceed := false
	err := s.tx(ctx, func(t *txn) error {
		item, err := t.Tasks().GetByLease(ctx, leaseID)
		if err != nil || item.Worker != w.Name {
			return nil
		}
		if err := item.Ack(leaseID, t.now, s.cfg.LeaseTTL); err != nil {
			return nil
		}
		r, err := t.Runs().GetByID(ctx, item.RunID)
		if err != nil {
			return err
		}
		if r.Phase.Terminal() {
			item.Cancel(t.now)
			return t.saveTask(item)
		}
		proceed = true
		verb := "started"
		if item.Kind == task.KindCompensation {
			verb = "compensation started"
		}
		t.syslog(r, "debug", fmt.Sprintf("task %s %s on %s (attempt %d)", item.Name, verb, w.Name, item.Attempt), nil, item)
		return t.saveTask(item)
	})
	return proceed, err
}

func (s *Service) CompleteTask(ctx context.Context, w *worker.Worker, leaseID string, result json.RawMessage) error {
	if len(result) == 0 {
		result = json.RawMessage("null")
	}
	return s.tx(ctx, func(t *txn) error {
		item, r, err := t.leasedItem(w, leaseID, task.KindTask, task.KindCompensation)
		if err != nil {
			return err
		}
		item.Succeed(t.now, result)
		if err := t.saveTask(item); err != nil {
			return err
		}
		if item.Kind == task.KindCompensation {
			t.syslog(r, "info", fmt.Sprintf("compensation %s succeeded", item.Name), nil, item)
			t.notify(ports.TopicReconcile)
			return nil
		}
		t.syslog(r, "info", fmt.Sprintf("task %s succeeded", item.Name), map[string]any{"attempt": item.Attempt}, item)
		if r.Phase.Terminal() || r.Phase == run.Compensating {
			return nil
		}
		if err := t.appendHistory(r, run.HistoryEvent{Type: run.EvTaskCompleted, Ref: item.Ref, Name: item.Name, Payload: result}); err != nil {
			return err
		}
		r.Wake()
		return t.saveRun(r)
	})
}

func (s *Service) FailTask(ctx context.Context, w *worker.Worker, leaseID string, f workflow.Failure) error {
	return s.tx(ctx, func(t *txn) error {
		item, r, err := t.leasedItem(w, leaseID, task.KindTask, task.KindCompensation)
		if err != nil {
			return err
		}
		return t.failAttempt(item, r, f, false)
	})
}

// failAttempt applies the retry policy to a failed (or lost) attempt.
func (t *txn) failAttempt(item *task.TaskRun, r *run.Run, f workflow.Failure, lost bool) error {
	wf, err := t.Workflows().Get(t.ctx, r.Namespace, r.Workflow, r.Version)
	if err != nil {
		return err
	}
	spec, ok := wf.Manifest.Task(item.Name)
	if !ok {
		spec = workflow.TaskSpec{Retry: workflow.RetryPolicy{Attempts: 1}}
	}
	var d workflow.Decision
	if lost {
		d = spec.Retry.DecideLost(item.Attempt)
	} else {
		d = spec.Retry.Decide(f, item.Attempt)
	}
	runOver := r.Phase.Terminal() || (r.Phase == run.Compensating && item.Kind == task.KindTask)
	if d.Retry && !runOver {
		t.syslog(r, "warning", fmt.Sprintf("task %s attempt %d failed: %s — retrying in %s", item.Name, item.Attempt, f, d.Delay), nil, item)
		item.Retry(t.now, f, d.Delay)
		return t.saveTask(item)
	}
	item.Fail(t.now, f)
	if err := t.saveTask(item); err != nil {
		return err
	}
	if runOver {
		return nil
	}
	t.syslog(r, "error", fmt.Sprintf("task %s failed: %s (%s)", item.Name, f, d.Reason), nil, item)
	if f.Stack != "" {
		t.syslog(r, "debug", f.Stack, nil, item)
	}
	if item.Kind == task.KindCompensation {
		t.notify(ports.TopicReconcile)
		return nil
	}
	if err := t.appendHistory(r, run.HistoryEvent{Type: run.EvTaskFailed, Ref: item.Ref, Name: item.Name, Failure: &f}); err != nil {
		return err
	}
	r.Wake()
	return t.saveRun(r)
}

// leaseLost applies the delivery guarantee when a running attempt vanished.
func (t *txn) leaseLost(item *task.TaskRun, r *run.Run) error {
	wf, err := t.Workflows().Get(t.ctx, r.Namespace, r.Workflow, r.Version)
	if err != nil {
		return err
	}
	spec, _ := wf.Manifest.Task(item.Name)
	switch task.OnLeaseLost(spec) {
	case task.LostPause:
		item.MarkUnknown(t.now)
		if err := t.saveTask(item); err != nil {
			return err
		}
		if r.Phase.Terminal() || r.Phase == run.Compensating {
			return nil
		}
		msg := fmt.Sprintf("task %s (ref %d) outcome unknown: worker %s lost mid-attempt", item.Name, item.Ref, item.Worker)
		if err := r.Pause("TaskOutcomeUnknown", msg); err != nil {
			return err
		}
		t.syslog(r, "error", msg+" — run paused", map[string]any{
			"hint": fmt.Sprintf("flyke task resolve %s %d --outcome=succeeded|failed|retry", r.ID, item.Ref),
		}, item)
		return t.saveRun(r)
	case task.LostFail:
		f := task.WorkerLost
		f.NonRetryable = true
		return t.failAttempt(item, r, f, false)
	default:
		return t.failAttempt(item, r, task.WorkerLost, true)
	}
}
