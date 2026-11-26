package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/flyke-org/flyke/flyke/internal/app/ports"
	"github.com/flyke-org/flyke/flyke/internal/domain/run"
	"github.com/flyke-org/flyke/flyke/internal/domain/task"
	"github.com/flyke-org/flyke/flyke/internal/domain/workflow"
)

type StartRunInput struct {
	Namespace      string
	Workflow       string
	Inputs         json.RawMessage
	Labels         map[string]string
	IdempotencyKey string
	TriggeredBy    string
	// Version pins the run to a registered version ("" = the current one).
	Version string
	// Retry links the new run to the run it retries.
	Retry *run.RetryPlan
}

// StartRun creates a run of the current version of a workflow, or of
// in.Version when set.
func (s *Service) StartRun(ctx context.Context, in StartRunInput) (*run.Run, error) {
	var out *run.Run
	err := s.tx(ctx, func(t *txn) error {
		wf, err := t.workflowVersion(in.Namespace, in.Workflow, in.Version)
		if err != nil {
			return err
		}
		out, err = t.startRun(wf, in)
		return err
	})
	return out, err
}

func (t *txn) startRun(wf workflow.Workflow, in StartRunInput) (*run.Run, error) {
	if in.IdempotencyKey != "" {
		existing, err := t.Runs().FindByIdempotencyKey(t.ctx, wf.Namespace, wf.Name, in.IdempotencyKey)
		if err == nil {
			return existing, nil
		}
		if !errors.Is(err, ports.ErrNotFound) {
			return nil, err
		}
	}
	inputs, raw, err := t.s.prepareInputs(wf, in.Inputs)
	if err != nil {
		return nil, err
	}
	labels := map[string]string{}
	for k, v := range wf.Manifest.Labels {
		labels[k] = v
	}
	for k, v := range in.Labels {
		labels[k] = v
	}
	labels["flyke.io/workflow"] = wf.Name
	r := &run.Run{
		ID:             t.s.ids.NewID(strings.ToLower(wf.Name)),
		Namespace:      wf.Namespace,
		Workflow:       wf.Name,
		Version:        wf.Version,
		Inputs:         raw,
		Labels:         labels,
		Phase:          run.Pending,
		Reason:         "Created",
		IdempotencyKey: in.IdempotencyKey,
		TriggeredBy:    in.TriggeredBy,
		Retry:          in.Retry,
		CreatedAt:      t.now,
	}
	if c := wf.Manifest.Concurrency; c != nil {
		key, err := workflow.Render(c.Key, inputs)
		if err != nil {
			return nil, invalid("concurrency key: %v", err)
		}
		r.ConcurrencyKey = key
	}
	if err := t.Runs().Create(t.ctx, r); err != nil {
		return nil, err
	}
	if err := t.appendHistory(r, run.HistoryEvent{Type: run.EvRunStarted, Payload: raw}); err != nil {
		return nil, err
	}
	// appendHistory bumped r.HistoryLength in memory; persist it so the run row
	// agrees with the history table (the reconciler copies it into StepHistoryLength).
	if err := t.Runs().Update(t.ctx, r); err != nil {
		return nil, err
	}
	fields := map[string]any{"version": wf.Version}
	if in.TriggeredBy != "" {
		fields["triggered_by"] = in.TriggeredBy
	}
	if p := in.Retry; p != nil {
		fields["retry_of"] = p.Of
		fields["retry_mode"] = string(p.Mode)
		if p.Mode == run.RetryFrom {
			fields["retry_from"] = fmt.Sprintf("%s (ref %d)", p.FromName, p.FromRef)
		}
	}
	t.syslog(r, "info", "run created", fields, nil)
	t.notify(ports.TopicReconcile)
	return r, nil
}

func (s *Service) GetRun(ctx context.Context, ns, id string) (r *run.Run, err error) {
	err = s.read(ctx, func(rp ports.Repos) error {
		r, err = rp.Runs().Get(ctx, ns, id)
		return err
	})
	return
}

func (s *Service) ListRuns(ctx context.Context, f ports.RunFilter) (out []*run.Run, err error) {
	err = s.read(ctx, func(rp ports.Repos) error {
		out, err = rp.Runs().List(ctx, f)
		return err
	})
	return
}

func (s *Service) History(ctx context.Context, ns, id string) (out []run.HistoryEvent, err error) {
	err = s.read(ctx, func(rp ports.Repos) error {
		if _, err := rp.Runs().Get(ctx, ns, id); err != nil {
			return err
		}
		out, err = rp.History().List(ctx, id)
		return err
	})
	return
}

func (s *Service) RunTasks(ctx context.Context, ns, id string) (out []*task.TaskRun, err error) {
	err = s.read(ctx, func(rp ports.Repos) error {
		if _, err := rp.Runs().Get(ctx, ns, id); err != nil {
			return err
		}
		out, err = rp.Tasks().ListByRun(ctx, id)
		return err
	})
	return
}

type RetryRunInput struct {
	Namespace string
	ID        string
	Mode      run.RetryMode
	// FromRef or FromTask (first call of that task) select the step to
	// restart from in RetryFrom mode.
	FromRef  int
	FromTask string
	// Inputs replaces the source run's inputs when set.
	Inputs json.RawMessage
	// Version is the code to retry with: "" the current version, "same" the
	// source run's version, or an explicit version.
	Version string
}

// RetryRun starts a new run of the latest version of a finished run's
// workflow. Depending on the mode it reuses the outcomes the source recorded
// (see run.Reuse), so that only the failed part — or everything from a chosen
// step on — executes again. The source run is left untouched.
func (s *Service) RetryRun(ctx context.Context, in RetryRunInput) (*run.Run, error) {
	var out *run.Run
	err := s.tx(ctx, func(t *txn) error {
		src, err := t.Runs().Get(ctx, in.Namespace, in.ID)
		if err != nil {
			return err
		}
		if !src.Phase.Terminal() {
			return precondition("run %s is %s; only finished runs can be retried (cancel it first, or resume it if paused)", src.ID, src.Phase)
		}
		mode := in.Mode
		if mode == "" {
			mode = run.RetryFailed
			if in.FromRef > 0 || in.FromTask != "" {
				mode = run.RetryFrom
			}
		}
		if !mode.Valid() {
			return invalid("retry mode must be all, failed or from")
		}
		plan := &run.RetryPlan{Of: src.ID, Mode: mode}
		if mode == run.RetryFrom {
			history, err := t.History().List(ctx, src.ID)
			if err != nil {
				return err
			}
			for _, ev := range history {
				switch ev.Type {
				case run.EvTaskScheduled, run.EvTimerStarted, run.EvWaitStarted, run.EvEventEmitted:
				default:
					continue
				}
				if (in.FromRef > 0 && ev.Ref == in.FromRef) || (in.FromRef == 0 && ev.Name == in.FromTask) {
					plan.FromRef, plan.FromName = ev.Ref, ev.Name
					break
				}
			}
			if plan.FromRef == 0 {
				if in.FromRef > 0 {
					return invalid("run %s has no step with ref %d", src.ID, in.FromRef)
				}
				if in.FromTask == "" {
					return invalid("retry from needs a step (fromRef or fromTask)")
				}
				return invalid("run %s never called %q", src.ID, in.FromTask)
			}
		}
		version := in.Version
		if version == "same" {
			version = src.Version
		}
		wf, err := t.workflowVersion(src.Namespace, src.Workflow, version)
		if err != nil {
			return err
		}
		inputs := src.Inputs
		if len(in.Inputs) > 0 {
			inputs = in.Inputs
		}
		labels := map[string]string{}
		for k, v := range src.Labels {
			if !strings.HasPrefix(k, "flyke.io/") {
				labels[k] = v
			}
		}
		out, err = t.startRun(wf, StartRunInput{
			Namespace: src.Namespace, Workflow: src.Workflow, Inputs: inputs, Labels: labels,
			TriggeredBy: "retry:" + src.ID, Retry: plan,
		})
		if err != nil {
			return err
		}
		t.syslog(src, "info", "retried as "+out.ID, map[string]any{"mode": string(mode)}, nil)
		return nil
	})
	return out, err
}

// CancelRun stops a run; completed compensable tasks are compensated.
func (s *Service) CancelRun(ctx context.Context, ns, id, reason string) (*run.Run, error) {
	var out *run.Run
	err := s.tx(ctx, func(t *txn) error {
		r, err := t.Runs().Get(ctx, ns, id)
		if err != nil {
			return err
		}
		if r.Phase.Terminal() || r.Phase == run.Compensating {
			return precondition("run is %s", r.Phase)
		}
		if reason == "" {
			reason = "cancelled by user"
		}
		if err := t.appendHistory(r, run.HistoryEvent{Type: run.EvCancelRequested, Name: reason}); err != nil {
			return err
		}
		if err := t.finishRun(r, run.Cancelled, "Cancelled", reason, nil); err != nil {
			return err
		}
		out = r
		return nil
	})
	return out, err
}

// ResumeRun continues a paused run (after fixing code or resolving tasks).
func (s *Service) ResumeRun(ctx context.Context, ns, id string) (*run.Run, error) {
	var out *run.Run
	err := s.tx(ctx, func(t *txn) error {
		r, err := t.Runs().Get(ctx, ns, id)
		if err != nil {
			return err
		}
		items, err := t.Tasks().ListByRun(ctx, id)
		if err != nil {
			return err
		}
		for _, it := range items {
			if it.Phase == task.Unknown {
				return precondition("task %s (ref %d) has an unknown outcome; resolve it first", it.Name, it.Ref)
			}
		}
		if err := r.Resume(); err != nil {
			return precondition("%v", err)
		}
		t.syslog(r, "info", "run resumed", nil, nil)
		out = r
		return t.saveRun(r)
	})
	return out, err
}

// ResolveTask settles a task whose outcome is unknown.
func (s *Service) ResolveTask(ctx context.Context, ns, id string, ref int, outcome string, result json.RawMessage, message string) error {
	return s.tx(ctx, func(t *txn) error {
		r, err := t.Runs().Get(ctx, ns, id)
		if err != nil {
			return err
		}
		items, err := t.Tasks().ListByRun(ctx, id)
		if err != nil {
			return err
		}
		var item *task.TaskRun
		for _, it := range items {
			if it.Kind == task.KindTask && it.Ref == ref && it.Phase == task.Unknown {
				item = it
			}
		}
		if item == nil {
			return precondition("no task with ref %d awaiting resolution", ref)
		}
		switch outcome {
		case "succeeded":
			if len(result) == 0 {
				result = json.RawMessage("null")
			}
			item.Succeed(t.now, result)
			if err := t.appendHistory(r, run.HistoryEvent{Type: run.EvTaskCompleted, Ref: ref, Name: item.Name, Payload: result}); err != nil {
				return err
			}
		case "failed":
			if message == "" {
				message = "resolved as failed by an operator"
			}
			f := workflow.Failure{Type: "flyke.ResolvedAsFailed", Message: message, NonRetryable: true}
			item.Fail(t.now, f)
			if err := t.appendHistory(r, run.HistoryEvent{Type: run.EvTaskFailed, Ref: ref, Name: item.Name, Failure: &f}); err != nil {
				return err
			}
		case "retry":
			item.Retry(t.now, task.WorkerLost, 0)
		default:
			return invalid("outcome must be succeeded, failed or retry")
		}
		if err := t.saveTask(item); err != nil {
			return err
		}
		t.syslog(r, "info", fmt.Sprintf("task %s resolved as %s", item.Name, outcome), nil, item)
		if r.Phase == run.Paused {
			if err := r.Resume(); err != nil {
				return err
			}
		}
		if outcome == "retry" {
			r.NeedsStep = false
		}
		return t.saveRun(r)
	})
}

// finishRun ends a run with a non-success phase, cancelling outstanding work
// and starting compensation when completed tasks have compensators.
func (t *txn) finishRun(r *run.Run, phase run.Phase, reason, message string, f *workflow.Failure) error {
	items, err := t.Tasks().ListByRun(t.ctx, r.ID)
	if err != nil {
		return err
	}
	wf, err := t.Workflows().Get(t.ctx, r.Namespace, r.Workflow, r.Version)
	if err != nil {
		return err
	}
	needsComp := false
	for _, it := range items {
		if it.Kind != task.KindCompensation && it.Phase.Active() || it.Phase == task.Unknown {
			it.Cancel(t.now)
			if err := t.saveTask(it); err != nil {
				return err
			}
		}
		if it.Kind == task.KindTask && it.Phase == task.Succeeded {
			if spec, ok := wf.Manifest.Tasks[it.Name]; ok && spec.Compensation != "" {
				needsComp = true
			}
		}
	}
	if err := t.Waits().CloseAll(t.ctx, r.ID); err != nil {
		return err
	}
	if f != nil && phase == run.Failed {
		if err := t.appendHistory(r, run.HistoryEvent{Type: run.EvRunFailed, Failure: f}); err != nil {
			return err
		}
	}
	if err := r.Finish(t.now, phase, reason, message, f, needsComp); err != nil {
		return err
	}
	if needsComp {
		t.syslog(r, "warning", fmt.Sprintf("run %s: %s — compensating completed tasks", strings.ToLower(string(phase)), message), nil, nil)
	} else {
		t.syslog(r, "error", fmt.Sprintf("run %s: %s", strings.ToLower(string(phase)), message), nil, nil)
	}
	return t.saveRun(r)
}

// settle recomputes Running vs Waiting from outstanding work.
func (t *txn) settle(r *run.Run) error {
	items, err := t.Tasks().ListByRun(t.ctx, r.ID)
	if err != nil {
		return err
	}
	active := false
	for _, it := range items {
		if it.Phase.Active() {
			active = true
			break
		}
	}
	r.Settle(active)
	return nil
}
