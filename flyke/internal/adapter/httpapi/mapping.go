package httpapi

import (
	"encoding/json"
	"sort"
	"strconv"

	v1 "github.com/flyke-org/flyke/api/rest/v1"
	"github.com/flyke-org/flyke/flyke/internal/app/ports"
	"github.com/flyke-org/flyke/flyke/internal/app/usecase"
	"github.com/flyke-org/flyke/flyke/internal/domain/event"
	"github.com/flyke-org/flyke/flyke/internal/domain/run"
	"github.com/flyke-org/flyke/flyke/internal/domain/secret"
	"github.com/flyke-org/flyke/flyke/internal/domain/task"
	"github.com/flyke-org/flyke/flyke/internal/domain/worker"
	"github.com/flyke-org/flyke/flyke/internal/domain/workflow"
)

func tm(kind string) v1.TypeMeta { return v1.TypeMeta{APIVersion: v1.APIVersion, Kind: kind} }

func failureDTO(f *workflow.Failure) *v1.Failure {
	if f == nil {
		return nil
	}
	return &v1.Failure{Type: f.Type, MRO: f.MRO, Message: f.Message, Stack: f.Stack, NonRetryable: f.NonRetryable}
}

func workflowDTO(wf workflow.Workflow) v1.Workflow {
	m, _ := json.Marshal(wf.Manifest)
	return v1.Workflow{
		TypeMeta: tm("Workflow"),
		Metadata: v1.ObjectMeta{Name: wf.Name, Namespace: wf.Namespace, Labels: wf.Manifest.Labels, CreatedAt: wf.CreatedAt},
		Spec: v1.WorkflowSpec{Manifest: m, Source: v1.Source{
			Entrypoint: wf.Source.Entrypoint, LocalRoot: wf.Source.LocalRoot, BundleDigest: wf.Source.BundleDigest,
		}},
		Status: v1.WorkflowStatus{Version: wf.Version, ActivatedAt: &wf.ActivatedAt},
	}
}

func workflowVersionDTO(v usecase.WorkflowVersion) v1.WorkflowVersion {
	m, _ := json.Marshal(v.Manifest)
	tasks := make([]string, 0, len(v.Manifest.Tasks))
	for n := range v.Manifest.Tasks {
		tasks = append(tasks, n)
	}
	sort.Strings(tasks)
	phases := map[string]int{}
	for p, n := range v.Runs.Phases {
		phases[string(p)] = n
	}
	out := v1.WorkflowVersion{
		TypeMeta: tm("WorkflowVersion"),
		Metadata: v1.ObjectMeta{Name: v.Name, Namespace: v.Namespace, Labels: v.Manifest.Labels, CreatedAt: v.CreatedAt},
		Version:  v.Version, Current: v.Current, ActivatedAt: v.ActivatedAt,
		Entrypoint: v.Source.Entrypoint, Runtime: v.Manifest.Runtime,
		Source: v1.Source{Entrypoint: v.Source.Entrypoint, LocalRoot: v.Source.LocalRoot, BundleDigest: v.Source.BundleDigest},
		Tasks:  tasks, Manifest: m,
		Runs: v1.VersionRuns{Total: v.Runs.Total, Phases: phases, FirstRunAt: v.Runs.FirstRunAt, LastRunAt: v.Runs.LastRunAt},
	}
	if d := v.Source.BundleDigest; d != "" {
		out.Bundle = &v1.BundleInfo{Digest: d, Size: v.BundleSize, UploadedAt: v.BundleUploadedAt}
	}
	return out
}

func runDTO(r *run.Run) v1.Run {
	var retry *v1.RetrySpec
	if p := r.Retry; p != nil {
		retry = &v1.RetrySpec{Of: p.Of, Mode: string(p.Mode), FromRef: p.FromRef, FromTask: p.FromName}
	}
	return v1.Run{
		TypeMeta: tm("WorkflowRun"),
		Metadata: v1.ObjectMeta{
			Name: r.ID, Namespace: r.Namespace, UID: r.ID, Labels: r.Labels,
			ResourceVersion: strconv.FormatInt(r.ResourceVersion, 10), CreatedAt: r.CreatedAt,
		},
		Spec: v1.RunSpec{Workflow: r.Workflow, Version: r.Version, Inputs: r.Inputs, TriggeredBy: r.TriggeredBy, Retry: retry},
		Status: v1.RunStatus{
			Phase: string(r.Phase), Reason: r.Reason, Message: r.Message, ConcurrencyKey: r.ConcurrencyKey,
			Compensation: r.Compensation, Result: r.Result, Failure: failureDTO(r.Failure),
			StartedAt: r.StartedAt, FinishedAt: r.FinishedAt, HistoryLength: r.HistoryLength,
		},
	}
}

func taskDTO(t *task.TaskRun) v1.TaskRun {
	return v1.TaskRun{
		ID: t.ID, Kind: string(t.Kind), Ref: t.Ref, Name: t.Name, Phase: string(t.Phase), Attempt: t.Attempt,
		Worker: t.Worker, IdempotencyKey: t.IdempotencyKey, Args: t.Args, Result: t.Result,
		Failure: failureDTO(t.Failure), CreatedAt: t.CreatedAt, StartedAt: t.StartedAt, FinishedAt: t.FinishedAt,
		ReusedFrom: t.ReusedFrom,
	}
}

func historyDTO(ev run.HistoryEvent) v1.HistoryEvent {
	return v1.HistoryEvent{ID: ev.ID, Type: string(ev.Type), Time: ev.Time, Ref: ev.Ref, Name: ev.Name, Payload: ev.Payload, Failure: failureDTO(ev.Failure)}
}

func logDTO(e ports.LogEntry) v1.LogEntry {
	return v1.LogEntry{
		ID: e.ID, RunID: e.RunID, Time: e.Time, Level: e.Level, Source: e.Source, Stream: e.Stream,
		Message: e.Message, Fields: e.Fields, Task: e.Task, Ref: e.Ref, Attempt: e.Attempt, Worker: e.Worker,
	}
}

func eventDTO(e event.Event, matched []string) v1.Event {
	return v1.Event{
		TypeMeta: tm("Event"),
		Metadata: v1.ObjectMeta{Name: e.ID, Namespace: e.Namespace, UID: e.ID, CreatedAt: e.CreatedAt},
		Name:     e.Name, Payload: e.Payload, Matched: matched,
	}
}

func scheduleDTO(s usecase.ScheduleInfo) v1.Schedule {
	return v1.Schedule{
		TypeMeta: tm("Schedule"),
		Metadata: v1.ObjectMeta{Name: s.Name, Namespace: s.Namespace, CreatedAt: s.CreatedAt},
		Spec: v1.ScheduleSpec{
			Workflow: s.Workflow, Cron: s.Cron, TZ: s.TZ, Inputs: s.Inputs, Missed: string(s.Missed), Paused: s.Paused,
		},
		Status: v1.ScheduleStatus{
			Description: s.Description, Declared: s.Declared, NextRunAt: s.NextRunAt, Upcoming: s.Upcoming,
			LastRunAt: s.LastRunAt, LastRun: s.LastRunID, LastRunPhase: string(s.LastRunPhase), LastError: s.LastError,
		},
	}
}

func resourcesDTO(r workflow.Resources) v1.Resources {
	return v1.Resources{CPU: r.CPU, MemoryBytes: r.MemoryBytes}
}

func workerDTO(w *worker.Worker, active int) v1.Worker {
	hb := w.LastHeartbeat
	return v1.Worker{
		TypeMeta: tm("Worker"),
		Metadata: v1.ObjectMeta{Name: w.Name, Namespace: w.Namespace, Labels: w.Labels, CreatedAt: w.CreatedAt},
		Spec:     v1.WorkerSpec{Unschedulable: w.Unschedulable, Draining: w.Draining},
		Status: v1.WorkerStatus{
			Phase: string(w.Phase), Hostname: w.Hostname, Pool: w.Pool, Runtimes: w.Runtimes,
			Slots: w.Slots, FreeSlots: w.FreeSlots, Capacity: resourcesDTO(w.Capacity),
			ActiveTasks: active, AgentVersion: w.AgentVersion, LastHeartbeat: &hb,
		},
	}
}

func poolDTO(p *worker.Pool, workers []*worker.Worker) v1.Pool {
	out := v1.Pool{
		TypeMeta: tm("Pool"),
		Metadata: v1.ObjectMeta{Name: p.Name, Namespace: p.Namespace, CreatedAt: p.CreatedAt},
		Spec:     v1.PoolSpec{Selector: p.EffectiveSelector(), MaxConcurrentTasks: p.MaxConcurrentTasks},
	}
	if len(p.ResourceProfiles) > 0 {
		out.Spec.ResourceProfiles = map[string]v1.Resources{}
		for k, r := range p.ResourceProfiles {
			out.Spec.ResourceProfiles[k] = resourcesDTO(r)
		}
	}
	sel := p.EffectiveSelector()
	for _, w := range workers {
		// A tenant's pool holds its own workers and shared ones, never
		// another tenant's, whatever the selector says.
		if !sel.Matches(w.Labels) || (w.Namespace != "" && w.Namespace != p.Namespace) {
			continue
		}
		out.Status.Workers++
		if w.Phase == worker.Ready {
			out.Status.ReadyWorkers++
			if w.Schedulable() {
				out.Status.FreeSlots += w.FreeSlots
			}
		}
	}
	return out
}

func secretDTO(sec secret.Secret) v1.Secret {
	return v1.Secret{
		ID:        sec.ID,
		Namespace: sec.Namespace,
		Key:       sec.Key,
		Value:     sec.Redacted().Value,
		Sensitive: sec.Sensitive,
		Type:      string(sec.Type),
		CreatedAt: sec.CreatedAt,
		UpdatedAt: sec.UpdatedAt,
	}
}

func tokenDTO(t worker.JoinToken, plain string) v1.JoinToken {
	return v1.JoinToken{ID: t.ID, Pool: t.Pool, ExpiresAt: t.ExpiresAt, MaxUses: t.MaxUses, Uses: t.Uses, Token: plain}
}
