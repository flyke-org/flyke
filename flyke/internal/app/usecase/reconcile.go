package usecase

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/flyke-org/flyke/flyke/internal/app/ports"
	"github.com/flyke-org/flyke/flyke/internal/domain/run"
	"github.com/flyke-org/flyke/flyke/internal/domain/task"
	"github.com/flyke-org/flyke/flyke/internal/domain/worker"
	"github.com/flyke-org/flyke/flyke/internal/domain/workflow"
)

// Reconcilers are level-triggered: each one reads current state and moves it
// one step towards the desired state. They are idempotent and safe to run at
// any frequency; missed notifications only delay convergence.

// ReconcileAdmission admits pending/queued runs, honouring concurrency keys.
func (s *Service) ReconcileAdmission(ctx context.Context) error {
	var waiting []*run.Run
	if err := s.read(ctx, func(r ports.Repos) error {
		var err error
		waiting, err = r.Runs().List(ctx, ports.RunFilter{Phases: []run.Phase{run.Pending, run.Queued}, Limit: 500})
		return err
	}); err != nil {
		return err
	}
	sort.Slice(waiting, func(i, j int) bool { return waiting[i].CreatedAt.Before(waiting[j].CreatedAt) })
	for _, candidate := range waiting {
		if err := s.tx(ctx, func(t *txn) error { return t.admit(candidate.Namespace, candidate.ID) }); err != nil {
			s.log.Warn("admission failed", "run", candidate.ID, "err", err)
		}
	}
	return nil
}

func (t *txn) admit(ns, id string) error {
	r, err := t.Runs().Get(t.ctx, ns, id)
	if err != nil || (r.Phase != run.Pending && r.Phase != run.Queued) {
		return err
	}
	wf, err := t.Workflows().Get(t.ctx, r.Namespace, r.Workflow, r.Version)
	if err != nil {
		return err
	}
	c := wf.Manifest.Concurrency
	if c == nil {
		return t.admitNow(r, wf)
	}
	peers, err := t.Runs().ListByConcurrencyKey(t.ctx, r.Namespace, r.Workflow, r.ConcurrencyKey)
	if err != nil {
		return err
	}
	active, position := 0, 0
	var holders []*run.Run
	for _, p := range peers {
		switch {
		case p.Phase.Active():
			active++
			holders = append(holders, p)
		case p.ID == r.ID:
		case p.CreatedAt.Before(r.CreatedAt) || (p.CreatedAt.Equal(r.CreatedAt) && p.ID < r.ID):
			position++ // older runs waiting for the same key go first
		}
	}
	if active+position < c.Limit {
		return t.admitNow(r, wf)
	}
	msg := fmt.Sprintf("concurrency key %q is at its limit (%d)", r.ConcurrencyKey, c.Limit)
	switch c.OnConflict {
	case workflow.ConflictSkip:
		if err := r.Skip(t.now, msg); err != nil {
			return err
		}
		t.syslog(r, "warning", "run skipped: "+msg, nil, nil)
	case workflow.ConflictCancelRunning:
		for _, h := range holders {
			if h.Phase == run.Compensating {
				continue
			}
			if err := t.finishRun(h, run.Cancelled, "Superseded", "superseded by run "+r.ID, nil); err != nil {
				return err
			}
		}
		if r.Phase == run.Queued {
			return nil
		}
		if err := r.Queue(msg); err != nil {
			return err
		}
	default:
		if r.Phase == run.Queued {
			return nil
		}
		if err := r.Queue(msg); err != nil {
			return err
		}
		t.syslog(r, "info", "run queued: "+msg, nil, nil)
	}
	return t.saveRun(r)
}

func (t *txn) admitNow(r *run.Run, wf workflow.Workflow) error {
	if err := r.Admit(t.now, wf.Manifest.Timeout()); err != nil {
		return err
	}
	t.syslog(r, "info", "run admitted", nil, nil)
	return t.saveRun(r)
}

// ReconcileSteps creates a workflow-step work item for every run whose
// history changed, ensuring at most one step in flight per run.
func (s *Service) ReconcileSteps(ctx context.Context) error {
	needs := true
	var runs []*run.Run
	if err := s.read(ctx, func(r ports.Repos) error {
		var err error
		runs, err = r.Runs().List(ctx, ports.RunFilter{Phases: []run.Phase{run.Running, run.Waiting}, NeedsStep: &needs, Limit: 500})
		return err
	}); err != nil {
		return err
	}
	for _, candidate := range runs {
		err := s.tx(ctx, func(t *txn) error {
			r, err := t.Runs().GetByID(ctx, candidate.ID)
			if err != nil || !r.NeedsStep || (r.Phase != run.Running && r.Phase != run.Waiting) {
				return err
			}
			items, err := t.Tasks().ListByRun(ctx, r.ID)
			if err != nil {
				return err
			}
			for _, it := range items {
				if it.Kind == task.KindStep && it.Phase.Active() {
					return nil // one step at a time; NeedsStep stays set
				}
			}
			wf, err := t.Workflows().Get(ctx, r.Namespace, r.Workflow, r.Version)
			if err != nil {
				return err
			}
			step, err := t.newItem(r, wf, task.KindStep, 0, "workflow")
			if err != nil {
				return err
			}
			step.StepHistoryLength = r.HistoryLength
			if err := t.Tasks().Create(ctx, step); err != nil {
				return err
			}
			r.NeedsStep = false
			r.Phase = run.Running
			t.notify(ports.TopicWork)
			return t.saveRun(r)
		})
		if err != nil {
			s.log.Warn("step dispatch failed", "run", candidate.ID, "err", err)
		}
	}
	return nil
}

// ReconcileLeases handles expired leases (unacknowledged or silent workers)
// and attempts that ran past their deadline.
func (s *Service) ReconcileLeases(ctx context.Context) error {
	now := s.clock.Now().UTC()
	var expired, overdue []*task.TaskRun
	if err := s.read(ctx, func(r ports.Repos) error {
		var err error
		if expired, err = r.Tasks().ListExpiredLeases(ctx, now); err != nil {
			return err
		}
		overdue, err = r.Tasks().ListPastDeadline(ctx, now.Add(-5*time.Second))
		return err
	}); err != nil {
		return err
	}
	handle := func(id string, timedOut bool) {
		err := s.tx(ctx, func(t *txn) error {
			item, err := t.Tasks().Get(ctx, id)
			if err != nil || !(item.Phase == task.Scheduled || item.Phase == task.Running) {
				return err
			}
			r, err := t.Runs().GetByID(ctx, item.RunID)
			if err != nil {
				return err
			}
			switch {
			case item.Phase == task.Scheduled || item.Kind == task.KindStep:
				// Never started (or a pure replay): safe to hand out again.
				t.syslog(r, "debug", fmt.Sprintf("%s %s requeued (lease on %s expired)", item.Kind, item.Name, item.Worker), nil, item)
				item.Requeue(t.now)
				return t.saveTask(item)
			case timedOut:
				return t.failAttempt(item, r, task.Timeout, false)
			default:
				return t.leaseLost(item, r)
			}
		})
		if err != nil {
			s.log.Warn("lease reconcile failed", "item", id, "err", err)
		}
	}
	for _, it := range expired {
		handle(it.ID, false)
	}
	for _, it := range overdue {
		handle(it.ID, true)
	}
	return nil
}

// ReconcileWaits fires due timers and times out event waits.
func (s *Service) ReconcileWaits(ctx context.Context) error {
	var due []run.Wait
	if err := s.read(ctx, func(r ports.Repos) error {
		var err error
		due, err = r.Waits().ListDue(ctx, s.clock.Now().UTC())
		return err
	}); err != nil {
		return err
	}
	for _, w := range due {
		err := s.tx(ctx, func(t *txn) error {
			r, err := t.Runs().GetByID(ctx, w.RunID)
			if err != nil {
				return err
			}
			if err := t.Waits().MarkDone(ctx, w.RunID, w.Ref); err != nil {
				return err
			}
			if r.Phase != run.Running && r.Phase != run.Waiting {
				return nil
			}
			ev := run.HistoryEvent{Type: run.EvTimerFired, Ref: w.Ref, Name: w.Name}
			if w.Kind == "event" {
				ev.Type = run.EvWaitTimedOut
				t.syslog(r, "warning", "wait for event "+w.Name+" timed out", nil, nil)
			}
			if err := t.appendHistory(r, ev); err != nil {
				return err
			}
			r.Wake()
			return t.saveRun(r)
		})
		if err != nil {
			s.log.Warn("wait reconcile failed", "run", w.RunID, "err", err)
		}
	}
	return nil
}

// ReconcileRunDeadlines times out runs that exceeded the workflow timeout.
func (s *Service) ReconcileRunDeadlines(ctx context.Context) error {
	var late []*run.Run
	if err := s.read(ctx, func(r ports.Repos) error {
		var err error
		late, err = r.Runs().ListPastDeadline(ctx, s.clock.Now().UTC())
		return err
	}); err != nil {
		return err
	}
	for _, lr := range late {
		err := s.tx(ctx, func(t *txn) error {
			r, err := t.Runs().GetByID(ctx, lr.ID)
			if err != nil || !r.Phase.Active() || r.Phase == run.Compensating {
				return err
			}
			f := &workflow.Failure{Type: "flyke.WorkflowTimeout", Message: "workflow exceeded its timeout", NonRetryable: true}
			return t.finishRun(r, run.TimedOut, "Timeout", f.Message, f)
		})
		if err != nil {
			s.log.Warn("deadline reconcile failed", "run", lr.ID, "err", err)
		}
	}
	return nil
}

// ReconcileCompensation runs compensations one at a time, newest first.
func (s *Service) ReconcileCompensation(ctx context.Context) error {
	var runs []*run.Run
	if err := s.read(ctx, func(r ports.Repos) error {
		var err error
		runs, err = r.Runs().List(ctx, ports.RunFilter{Phases: []run.Phase{run.Compensating}, Limit: 500})
		return err
	}); err != nil {
		return err
	}
	for _, cr := range runs {
		err := s.tx(ctx, func(t *txn) error {
			r, err := t.Runs().GetByID(ctx, cr.ID)
			if err != nil || r.Phase != run.Compensating {
				return err
			}
			items, err := t.Tasks().ListByRun(ctx, r.ID)
			if err != nil {
				return err
			}
			compensated := map[int]bool{}
			for _, it := range items {
				if it.Kind != task.KindCompensation {
					continue
				}
				switch {
				case it.Phase.Active():
					return nil // wait for the in-flight compensation
				case it.Phase == task.Failed:
					if err := r.CompleteCompensation(t.now, false); err != nil {
						return err
					}
					t.syslog(r, "error", "compensation failed; manual cleanup required", nil, it)
					return t.saveRun(r)
				case it.Phase == task.Succeeded:
					compensated[it.Ref] = true
				}
			}
			wf, err := t.Workflows().Get(ctx, r.Namespace, r.Workflow, r.Version)
			if err != nil {
				return err
			}
			var todo []*task.TaskRun
			for _, it := range items {
				if it.Kind != task.KindTask || it.Phase != task.Succeeded || compensated[it.Ref] {
					continue
				}
				if spec := wf.Manifest.Tasks[it.Name]; spec.Compensation != "" {
					todo = append(todo, it)
				}
			}
			if len(todo) == 0 {
				if err := r.CompleteCompensation(t.now, true); err != nil {
					return err
				}
				t.syslog(r, "info", fmt.Sprintf("compensation completed; run %s", r.Phase), nil, nil)
				return t.saveRun(r)
			}
			// Reverse completion order (saga semantics).
			sort.Slice(todo, func(i, j int) bool { return todo[i].FinishedAt.After(*todo[j].FinishedAt) })
			orig := todo[0]
			name := wf.Manifest.Tasks[orig.Name].Compensation
			comp, err := t.newItem(r, wf, task.KindCompensation, orig.Ref, name)
			if err != nil {
				return err
			}
			comp.CompensatedTask = orig.Name
			comp.Args = orig.Args
			comp.Result = orig.Result
			if err := t.Tasks().Create(ctx, comp); err != nil {
				return err
			}
			t.syslog(r, "info", fmt.Sprintf("compensating %s with %s", orig.Name, name), nil, comp)
			t.notify(ports.TopicWork)
			return nil
		})
		if err != nil {
			s.log.Warn("compensation reconcile failed", "run", cr.ID, "err", err)
		}
	}
	return nil
}

// ReconcileWorkers marks silent workers NotReady.
func (s *Service) ReconcileWorkers(ctx context.Context) error {
	return s.tx(ctx, func(t *txn) error {
		ws, err := t.Workers().List(ctx)
		if err != nil {
			return err
		}
		for _, w := range ws {
			if w.Phase == worker.Ready && t.now.Sub(w.LastHeartbeat) > s.cfg.WorkerNotReadyAfter {
				w.Phase = worker.NotReady
				s.log.Warn("worker not ready", "worker", w.Name, "last_heartbeat", w.LastHeartbeat)
				if err := t.Workers().Update(ctx, w); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
