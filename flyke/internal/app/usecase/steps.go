package usecase

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/flyke-org/flyke/flyke/internal/app/ports"
	"github.com/flyke-org/flyke/flyke/internal/domain/run"
	"github.com/flyke-org/flyke/flyke/internal/domain/task"
	"github.com/flyke-org/flyke/flyke/internal/domain/worker"
	"github.com/flyke-org/flyke/flyke/internal/domain/workflow"
)

// newItem builds a work item with its placement requirements resolved.
func (t *txn) newItem(r *run.Run, wf workflow.Workflow, kind task.Kind, ref int, name string) (*task.TaskRun, error) {
	item := &task.TaskRun{
		ID:        t.s.ids.NewID(string(kind)),
		RunID:     r.ID,
		Namespace: r.Namespace,
		Kind:      kind,
		Ref:       ref,
		Name:      name,
		Attempt:   1,
		Phase:     task.Pending,
		NotBefore: t.now,
		CreatedAt: t.now,
		Guarantee: workflow.AtLeastOnce,
	}
	var res *workflow.Resources
	pool := wf.Manifest.Pool
	if kind == task.KindStep {
		res = wf.Manifest.Resources
	} else {
		spec, ok := wf.Manifest.Task(name)
		if !ok {
			return nil, fmt.Errorf("unknown task %q", name)
		}
		item.Guarantee = spec.Guarantee
		if spec.Resources != nil {
			res = spec.Resources
		}
		if spec.Pool != "" {
			pool = spec.Pool
		}
	}
	item.Pool = pool
	if res != nil {
		requests, poolFromProfile, err := t.resolveResources(r.Namespace, *res, pool)
		if err != nil {
			return nil, err
		}
		item.Requests = requests
		if item.Pool == "" {
			item.Pool = poolFromProfile
		}
	}
	return item, nil
}

// resolveResources expands a named profile using pool definitions.
// A profile defined by no pool requests nothing (logged by the caller).
// Only pools the run's namespace may use are considered.
func (t *txn) resolveResources(ns string, res workflow.Resources, pool string) (workflow.Resources, string, error) {
	if res.Profile == "" {
		return workflow.Resources{CPU: res.CPU, MemoryBytes: res.MemoryBytes}, "", nil
	}
	pools, err := t.Pools().List(t.ctx)
	if err != nil {
		return workflow.Resources{}, "", err
	}
	sort.Slice(pools, func(i, j int) bool { return pools[i].Name < pools[j].Name })
	var candidates []*worker.Pool
	for _, p := range pools {
		if pool != "" && p.Name != pool {
			continue
		}
		if ok, err := t.s.poolVisible(t.ctx, t, ns, p); err != nil {
			return workflow.Resources{}, "", err
		} else if !ok {
			continue
		}
		if _, ok := p.ResourceProfiles[res.Profile]; ok {
			candidates = append(candidates, p)
		}
	}
	if len(candidates) == 0 {
		return workflow.Resources{}, "", nil
	}
	p := candidates[0]
	return p.ResourceProfiles[res.Profile], p.Name, nil
}

// CompleteStep applies the commands produced by a workflow step.
func (s *Service) CompleteStep(ctx context.Context, w *worker.Worker, leaseID string, cmds []run.Command) error {
	return s.tx(ctx, func(t *txn) error {
		item, r, err := t.leasedItem(w, leaseID, task.KindStep)
		if err != nil {
			return err
		}
		item.Succeed(t.now, nil)
		if err := t.saveTask(item); err != nil {
			return err
		}
		if r.Phase != run.Running && r.Phase != run.Waiting {
			return nil // run moved on (cancelled, paused…): drop the step
		}
		wf, err := t.Workflows().Get(ctx, r.Namespace, r.Workflow, r.Version)
		if err != nil {
			return err
		}
		history, err := t.History().List(ctx, r.ID)
		if err != nil {
			return err
		}
		known := map[int]bool{}
		for _, ev := range history {
			switch ev.Type {
			case run.EvTaskScheduled, run.EvTimerStarted, run.EvWaitStarted, run.EvEventEmitted:
				known[ev.Ref] = true
			}
		}
		reuse := t.reuser(r)
		for _, c := range cmds {
			if c.Kind != run.CmdCompleteRun && c.Kind != run.CmdFailRun && known[c.Ref] {
				continue // duplicate of an already-applied command: idempotent
			}
			if ok, err := reuse(wf, c); err != nil || ok {
				if err != nil {
					return err
				}
				continue
			}
			done, err := t.applyCommand(r, wf, c)
			if err != nil {
				return err
			}
			if done {
				return nil
			}
		}
		if err := t.settle(r); err != nil {
			return err
		}
		return t.saveRun(r)
	})
}

// applyCommand applies one command; done=true when the run reached an end.
func (t *txn) applyCommand(r *run.Run, wf workflow.Workflow, c run.Command) (bool, error) {
	switch c.Kind {
	case run.CmdScheduleTask:
		return false, t.scheduleTask(r, wf, c)

	case run.CmdStartTimer:
		deadline := t.now.Add(c.Duration)
		if err := t.appendHistory(r, run.HistoryEvent{Type: run.EvTimerStarted, Ref: c.Ref, Name: c.Name}); err != nil {
			return false, err
		}
		return false, t.Waits().Create(t.ctx, run.Wait{RunID: r.ID, Namespace: r.Namespace, Ref: c.Ref, Kind: "timer", Name: c.Name, Deadline: &deadline})

	case run.CmdWaitEvent:
		where, _ := json.Marshal(c.Where)
		if err := t.appendHistory(r, run.HistoryEvent{Type: run.EvWaitStarted, Ref: c.Ref, Name: c.Name, Payload: where}); err != nil {
			return false, err
		}
		wt := run.Wait{RunID: r.ID, Namespace: r.Namespace, Ref: c.Ref, Kind: "event", Name: c.Name, Where: c.Where}
		msg := "waiting for event " + c.Name
		if c.Duration > 0 {
			d := t.now.Add(c.Duration)
			wt.Deadline = &d
			msg += fmt.Sprintf(" (timeout %s)", c.Duration)
		}
		t.syslog(r, "info", msg, map[string]any{"where": c.Where}, nil)
		return false, t.Waits().Create(t.ctx, wt)

	case run.CmdEmitEvent:
		if err := t.appendHistory(r, run.HistoryEvent{Type: run.EvEventEmitted, Ref: c.Ref, Name: c.Name, Payload: c.Payload}); err != nil {
			return false, err
		}
		_, _, err := t.postEvent(r.Namespace, c.Name, c.Payload, "workflow:"+r.ID)
		return false, err

	case run.CmdCompleteRun:
		if err := t.cancelLeftovers(r); err != nil {
			return false, err
		}
		if err := t.appendHistory(r, run.HistoryEvent{Type: run.EvRunCompleted, Payload: c.Payload}); err != nil {
			return false, err
		}
		if err := r.Succeed(t.now, c.Payload); err != nil {
			return false, err
		}
		t.syslog(r, "info", "run succeeded", nil, nil)
		return true, t.saveRun(r)

	case run.CmdFailRun:
		f := c.Failure
		if f == nil {
			f = &workflow.Failure{Type: "flyke.WorkflowFailed", Message: "workflow failed"}
		}
		return true, t.finishRun(r, run.Failed, "WorkflowFailed", f.String(), f)
	}
	return false, invalid("unknown command kind %q", c.Kind)
}

func (t *txn) scheduleTask(r *run.Run, wf workflow.Workflow, c run.Command) error {
	spec, ok := wf.Manifest.Tasks[c.Name]
	if !ok {
		return invalid("workflow %s has no task %q", wf.Name, c.Name)
	}
	if err := t.appendHistory(r, run.HistoryEvent{Type: run.EvTaskScheduled, Ref: c.Ref, Name: c.Name, Payload: c.Payload}); err != nil {
		return err
	}
	// Effectively-once: reuse the result of a completed task with the same key.
	if spec.Guarantee == workflow.EffectivelyOnce && c.IdempotencyKey != "" {
		prev, err := t.Tasks().FindSucceededByIdempotencyKey(t.ctx, r.Namespace, c.IdempotencyKey)
		if err == nil {
			// The latest effect under this key was undone by a compensation:
			// there is nothing to deduplicate against any more.
			if undone, cerr := t.compensated(prev); cerr != nil {
				return cerr
			} else if undone {
				err = ports.ErrNotFound
			}
		}
		if err == nil {
			if err := t.appendHistory(r, run.HistoryEvent{Type: run.EvTaskCompleted, Ref: c.Ref, Name: c.Name, Payload: prev.Result}); err != nil {
				return err
			}
			t.syslog(r, "info", fmt.Sprintf("task %s deduplicated by idempotency key", c.Name), map[string]any{"key": c.IdempotencyKey, "from_run": prev.RunID}, nil)
			r.NeedsStep = true
			return nil
		}
	}
	item, err := t.newItem(r, wf, task.KindTask, c.Ref, c.Name)
	if err != nil {
		return err
	}
	item.Args = c.Payload
	item.IdempotencyKey = c.IdempotencyKey
	if err := t.Tasks().Create(t.ctx, item); err != nil {
		return err
	}
	t.notify(ports.TopicWork)
	t.syslog(r, "debug", fmt.Sprintf("task %s scheduled", c.Name), nil, item)
	return nil
}

// reuser returns a function that, for a retry run, replays the source run's
// recorded outcome of a command instead of executing it. It reports whether
// the command was satisfied that way. The source is loaded lazily, once.
func (t *txn) reuser(r *run.Run) func(workflow.Workflow, run.Command) (bool, error) {
	if r.Retry == nil || r.Retry.Mode == run.RetryAll {
		return func(workflow.Workflow, run.Command) (bool, error) { return false, nil }
	}
	var (
		loaded      bool
		src         []run.HistoryEvent
		compensated map[int]bool
	)
	return func(wf workflow.Workflow, c run.Command) (bool, error) {
		switch c.Kind {
		case run.CmdScheduleTask, run.CmdStartTimer, run.CmdWaitEvent, run.CmdEmitEvent:
		default:
			return false, nil
		}
		if !r.Retry.Covers(c.Ref) {
			return false, nil
		}
		if !loaded {
			loaded = true
			var err error
			if src, err = t.History().List(t.ctx, r.Retry.Of); err != nil {
				return false, err
			}
			items, err := t.Tasks().ListByRun(t.ctx, r.Retry.Of)
			if err != nil {
				return false, err
			}
			compensated = map[int]bool{}
			for _, it := range items {
				if it.Kind == task.KindCompensation {
					compensated[it.Ref] = true
				}
			}
		}
		evs, ok := run.Reuse(src, compensated, r.Retry, c)
		if !ok {
			return false, nil
		}
		if c.Kind == run.CmdScheduleTask {
			if _, known := wf.Manifest.Tasks[c.Name]; !known {
				return false, nil // let scheduleTask report the error
			}
			item, err := t.newItem(r, wf, task.KindTask, c.Ref, c.Name)
			if err != nil {
				return false, err
			}
			item.Args, item.Result, item.IdempotencyKey = c.Payload, evs[1].Payload, c.IdempotencyKey
			item.Phase, item.ReusedFrom = task.Succeeded, r.Retry.Of
			item.StartedAt, item.FinishedAt = ptrTime(t.now), ptrTime(t.now)
			if err := t.Tasks().Create(t.ctx, item); err != nil {
				return false, err
			}
			t.syslog(r, "info", fmt.Sprintf("task %s reused the result of %s", c.Name, r.Retry.Of), nil, item)
		} else {
			what := map[run.CommandKind]string{run.CmdStartTimer: "timer", run.CmdWaitEvent: "wait for event", run.CmdEmitEvent: "emitted event"}[c.Kind]
			t.syslog(r, "info", fmt.Sprintf("%s %s reused from %s", what, c.Name, r.Retry.Of), nil, nil)
		}
		if err := t.appendHistory(r, evs...); err != nil {
			return false, err
		}
		r.NeedsStep = true
		return true, nil
	}
}

// compensated reports whether a succeeded task was later compensated.
func (t *txn) compensated(item *task.TaskRun) (bool, error) {
	items, err := t.Tasks().ListByRun(t.ctx, item.RunID)
	if err != nil {
		return false, err
	}
	for _, it := range items {
		if it.Kind == task.KindCompensation && it.Ref == item.Ref {
			return true, nil
		}
	}
	return false, nil
}

func (t *txn) cancelLeftovers(r *run.Run) error {
	items, err := t.Tasks().ListByRun(t.ctx, r.ID)
	if err != nil {
		return err
	}
	for _, it := range items {
		if it.Kind == task.KindTask && it.Phase.Active() {
			it.Cancel(t.now)
			if err := t.saveTask(it); err != nil {
				return err
			}
		}
	}
	return t.Waits().CloseAll(t.ctx, r.ID)
}

// FailStep handles a workflow step that could not run (non-determinism,
// import error…). The run is paused so that code can be fixed and resumed.
func (s *Service) FailStep(ctx context.Context, w *worker.Worker, leaseID string, f workflow.Failure) error {
	return s.tx(ctx, func(t *txn) error {
		item, r, err := t.leasedItem(w, leaseID, task.KindStep)
		if err != nil {
			return err
		}
		item.Fail(t.now, f)
		if err := t.saveTask(item); err != nil {
			return err
		}
		if r.Phase.Terminal() || r.Phase == run.Compensating {
			return nil
		}
		reason := "StepFailed"
		if f.Type == "flyke.NonDeterminismError" {
			reason = "NonDeterminism"
		}
		if err := r.Pause(reason, f.String()); err != nil {
			return err
		}
		t.syslog(r, "error", "workflow step failed, run paused: "+f.String(), map[string]any{"hint": "fix the code, then `flyke resume " + r.ID + "`"}, nil)
		if f.Stack != "" {
			t.syslog(r, "error", f.Stack, nil, nil)
		}
		return t.saveRun(r)
	})
}

// leasedItem loads an item and its run after the fencing check.
func (t *txn) leasedItem(w *worker.Worker, leaseID string, kinds ...task.Kind) (*task.TaskRun, *run.Run, error) {
	item, err := t.Tasks().GetByLease(t.ctx, leaseID)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrFailedPrecondition, task.ErrStaleLease)
	}
	if err := item.CheckLease(leaseID, w.Name); err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrFailedPrecondition, err)
	}
	okKind := len(kinds) == 0
	for _, k := range kinds {
		okKind = okKind || item.Kind == k
	}
	if !okKind {
		return nil, nil, invalid("lease %s is a %s, not a %v", leaseID, item.Kind, kinds)
	}
	r, err := t.Runs().GetByID(t.ctx, item.RunID)
	if err != nil {
		return nil, nil, err
	}
	return item, r, nil
}
