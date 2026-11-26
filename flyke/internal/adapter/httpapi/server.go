// Package httpapi exposes the REST API (flyke.io/v1) used by the UI, the CLI
// and SDK clients. Handlers decode → call one use case → encode.
package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	v1 "github.com/flyke-org/flyke/api/rest/v1"
	"github.com/flyke-org/flyke/flyke/internal/app/ports"
	"github.com/flyke-org/flyke/flyke/internal/app/usecase"
	"github.com/flyke-org/flyke/flyke/internal/domain/run"
	"github.com/flyke-org/flyke/flyke/internal/domain/schedule"
	"github.com/flyke-org/flyke/flyke/internal/domain/secret"
	"github.com/flyke-org/flyke/flyke/internal/domain/worker"
	"github.com/flyke-org/flyke/flyke/internal/domain/workflow"
)

type API struct {
	svc        *usecase.Service
	adminToken string
	log        *slog.Logger
	pg         *playground // nil unless the playground is enabled
}

// New returns the REST handler. When adminToken is non-empty every request
// must carry "Authorization: Bearer <adminToken>". pg (may be nil) enables the
// UI playground.
func New(svc *usecase.Service, adminToken string, log *slog.Logger, pg *Playground) http.Handler {
	a := &API{svc: svc, adminToken: adminToken, log: log}
	if pg != nil {
		a.pg = &playground{cfg: pg}
	}
	mux := http.NewServeMux()
	const p = "/api/v1"
	nsp := p + "/namespaces/{ns}"

	// Every route goes through a.guard (see auth.go). Namespaced routes are
	// open to that namespace's tenant; cluster routes are admin only.
	tenant := func(pattern string, h http.HandlerFunc) { mux.HandleFunc(pattern, a.guard(tenantAccess, h)) }
	admin := func(pattern string, h http.HandlerFunc) { mux.HandleFunc(pattern, a.guard(adminAccess, h)) }
	anyone := func(pattern string, h http.HandlerFunc) { mux.HandleFunc(pattern, a.guard(anyAccess, h)) }

	anyone("GET "+p+"/whoami", a.whoami)
	anyone("GET "+p+"/namespaces", a.listNamespaces)
	admin("POST "+p+"/namespaces", a.createNamespace)
	tenant("GET "+nsp, a.getNamespace)
	admin("PUT "+nsp, a.putNamespace)
	admin("DELETE "+nsp, a.deleteNamespace)

	tenant("GET "+nsp+"/tokens", a.listAccessTokens)
	tenant("POST "+nsp+"/tokens", a.createAccessToken)
	tenant("DELETE "+nsp+"/tokens/{id}", a.deleteAccessToken)

	tenant("POST "+nsp+"/bundles", a.uploadBundle)
	admin("POST "+p+"/bundles", a.uploadBundle) // usable by every namespace

	tenant("GET "+nsp+"/workflows", a.listWorkflows)
	tenant("GET "+nsp+"/workflows/{name}", a.getWorkflow)
	tenant("PUT "+nsp+"/workflows/{name}", a.putWorkflow)
	tenant("POST "+nsp+"/workflows/{name}/runs", a.startRun)
	tenant("GET "+nsp+"/workflows/{name}/versions", a.listWorkflowVersions)
	tenant("GET "+nsp+"/workflows/{name}/versions/{version}", a.getWorkflowVersion)
	tenant("POST "+nsp+"/workflows/{name}/versions/{version}/activate", a.activateWorkflowVersion)

	tenant("GET "+nsp+"/runs", a.listRuns)
	tenant("GET "+nsp+"/runs/{id}", a.getRun)
	tenant("GET "+nsp+"/runs/{id}/history", a.getHistory)
	tenant("GET "+nsp+"/runs/{id}/tasks", a.getTasks)
	tenant("GET "+nsp+"/runs/{id}/logs", a.getLogs)
	tenant("POST "+nsp+"/runs/{id}/cancel", a.cancelRun)
	tenant("POST "+nsp+"/runs/{id}/resume", a.resumeRun)
	tenant("POST "+nsp+"/runs/{id}/retry", a.retryRun)
	tenant("POST "+nsp+"/runs/{id}/tasks/{ref}/resolve", a.resolveTask)

	tenant("GET "+nsp+"/schedules", a.listSchedules)
	tenant("POST "+nsp+"/schedules/preview", a.previewSchedule)
	tenant("GET "+nsp+"/schedules/{name}", a.getSchedule)
	tenant("PUT "+nsp+"/schedules/{name}", a.putSchedule)
	tenant("DELETE "+nsp+"/schedules/{name}", a.deleteSchedule)
	tenant("POST "+nsp+"/schedules/{name}/pause", a.pauseSchedule(true))
	tenant("POST "+nsp+"/schedules/{name}/resume", a.pauseSchedule(false))
	tenant("POST "+nsp+"/schedules/{name}/trigger", a.triggerSchedule)

	tenant("POST "+nsp+"/events", a.postEvent)
	tenant("GET "+nsp+"/events", a.listEvents)

	// Private secrets per namespace; Shared ones are written by the admin.
	tenant("GET "+nsp+"/secrets", a.listSecrets)
	tenant("POST "+nsp+"/secrets", a.createSecret)
	tenant("GET "+nsp+"/secrets/{key}", a.getSecret)
	tenant("PUT "+nsp+"/secrets/{key}", a.putSecret)
	tenant("DELETE "+nsp+"/secrets/{key}", a.deleteSecret)
	admin("GET "+p+"/secrets", a.listSecrets)
	admin("POST "+p+"/secrets", a.createSecret)
	admin("GET "+p+"/secrets/{key}", a.getSecret)
	admin("PUT "+p+"/secrets/{key}", a.putSecret)
	admin("DELETE "+p+"/secrets/{key}", a.deleteSecret)

	a.routePlayground(mux, p, nsp)

	// Workers and pools: a namespace's own (plus shared ones it may use)
	// under the namespace; all of them, and shared pools, on cluster routes.
	for _, base := range []string{nsp, p} {
		tenant("GET "+base+"/workers", a.listWorkers)
		tenant("GET "+base+"/workers/{name}", a.getWorker)
		tenant("DELETE "+base+"/workers/{name}", a.deleteWorker)
		tenant("POST "+base+"/workers/{name}/cordon", a.workerSchedulable(false, false))
		tenant("POST "+base+"/workers/{name}/uncordon", a.workerSchedulable(true, false))
		tenant("POST "+base+"/workers/{name}/drain", a.workerSchedulable(false, true))

		tenant("GET "+base+"/pools", a.listPools)
		tenant("GET "+base+"/pools/{name}", a.getPool)
		tenant("PUT "+base+"/pools/{name}", a.putPool)
		tenant("DELETE "+base+"/pools/{name}", a.deletePool)
		tenant("POST "+base+"/pools/{name}/join-tokens", a.createToken)
		tenant("GET "+base+"/pools/{name}/join-tokens", a.listTokens)
		tenant("DELETE "+base+"/pools/{name}/join-tokens/{id}", a.deleteToken)
	}

	return a.authenticate(mux)
}

// ------------------------------------------------------------- plumbing ----

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeStatus(w http.ResponseWriter, code int, reason, msg string) {
	writeJSON(w, code, v1.Status{TypeMeta: tm("Status"), Code: code, Reason: reason, Message: msg})
}

func (a *API) fail(w http.ResponseWriter, err error) {
	code, reason := http.StatusInternalServerError, "InternalError"
	switch {
	case errors.Is(err, usecase.ErrNotFound):
		code, reason = http.StatusNotFound, "NotFound"
	case errors.Is(err, usecase.ErrAlreadyExists):
		code, reason = http.StatusConflict, "AlreadyExists"
	case errors.Is(err, ports.ErrConflict):
		code, reason = http.StatusConflict, "Conflict"
	case errors.Is(err, usecase.ErrInvalidArgument):
		code, reason = http.StatusUnprocessableEntity, "Invalid"
	case errors.Is(err, usecase.ErrFailedPrecondition):
		code, reason = http.StatusConflict, "FailedPrecondition"
	case errors.Is(err, usecase.ErrUnauthenticated):
		code, reason = http.StatusUnauthorized, "Unauthorized"
	case errors.Is(err, usecase.ErrPermissionDenied):
		code, reason = http.StatusForbidden, "Forbidden"
	}
	if code == http.StatusInternalServerError {
		a.log.Error("request failed", "err", err)
	}
	writeStatus(w, code, reason, err.Error())
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 8<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: request body: %v", usecase.ErrInvalidArgument, err)
	}
	return nil
}

func list[T any](kind string, items []T) v1.List[T] {
	if items == nil {
		items = []T{}
	}
	return v1.List[T]{TypeMeta: tm(kind + "List"), Items: items}
}

// ------------------------------------------------------------ workflows ----

func (a *API) uploadBundle(w http.ResponseWriter, r *http.Request) {
	digest, size, err := a.svc.UploadBundle(r.Context(), scope(r), r.Body)
	if err != nil {
		a.fail(w, fmt.Errorf("%w: %v", usecase.ErrInvalidArgument, err))
		return
	}
	writeJSON(w, http.StatusCreated, v1.BundleUploadResponse{Digest: digest, Size: size})
}

func (a *API) listWorkflows(w http.ResponseWriter, r *http.Request) {
	wfs, err := a.svc.ListWorkflows(r.Context(), r.PathValue("ns"))
	if err != nil {
		a.fail(w, err)
		return
	}
	out := make([]v1.Workflow, len(wfs))
	for i, wf := range wfs {
		out[i] = workflowDTO(wf)
	}
	writeJSON(w, http.StatusOK, list("Workflow", out))
}

func (a *API) getWorkflow(w http.ResponseWriter, r *http.Request) {
	wf, err := a.svc.GetWorkflow(r.Context(), r.PathValue("ns"), r.PathValue("name"))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, workflowDTO(wf))
}

func (a *API) putWorkflow(w http.ResponseWriter, r *http.Request) {
	var body v1.WorkflowSpec
	if err := decode(r, &body); err != nil {
		a.fail(w, err)
		return
	}
	src := workflow.Source{Entrypoint: body.Source.Entrypoint, LocalRoot: body.Source.LocalRoot, BundleDigest: body.Source.BundleDigest}
	if src.LocalRoot != "" && !caller(r).admin {
		// A local root is a path on the worker's disk: tenants ship bundles.
		writeStatus(w, http.StatusForbidden, "Forbidden", "source.localRoot needs the admin token; upload a bundle instead")
		return
	}
	wf, err := a.svc.RegisterWorkflow(r.Context(), r.PathValue("ns"), r.PathValue("name"), body.Manifest, src)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, workflowDTO(wf))
}

func (a *API) listWorkflowVersions(w http.ResponseWriter, r *http.Request) {
	vs, err := a.svc.ListWorkflowVersions(r.Context(), r.PathValue("ns"), r.PathValue("name"))
	if err != nil {
		a.fail(w, err)
		return
	}
	out := make([]v1.WorkflowVersion, len(vs))
	for i, v := range vs {
		out[i] = workflowVersionDTO(v)
	}
	writeJSON(w, http.StatusOK, list("WorkflowVersion", out))
}

func (a *API) getWorkflowVersion(w http.ResponseWriter, r *http.Request) {
	v, err := a.svc.GetWorkflowVersion(r.Context(), r.PathValue("ns"), r.PathValue("name"), r.PathValue("version"))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, workflowVersionDTO(v))
}

// activateWorkflowVersion makes an existing version current (rollback).
func (a *API) activateWorkflowVersion(w http.ResponseWriter, r *http.Request) {
	wf, err := a.svc.ActivateWorkflowVersion(r.Context(), r.PathValue("ns"), r.PathValue("name"), r.PathValue("version"))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, workflowDTO(wf))
}

// ----------------------------------------------------------------- runs ----

func (a *API) startRun(w http.ResponseWriter, r *http.Request) {
	var body v1.StartRunRequest
	if r.ContentLength != 0 {
		if err := decode(r, &body); err != nil {
			a.fail(w, err)
			return
		}
	}
	key := body.IdempotencyKey
	if h := r.Header.Get("Idempotency-Key"); h != "" {
		key = h
	}
	x, err := a.svc.StartRun(r.Context(), usecase.StartRunInput{
		Namespace: r.PathValue("ns"), Workflow: r.PathValue("name"), Inputs: body.Inputs,
		Labels: body.Labels, IdempotencyKey: key, TriggeredBy: "api", Version: body.Version,
	})
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, runDTO(x))
}

func (a *API) listRuns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := ports.RunFilter{Namespace: r.PathValue("ns"), Workflow: q.Get("workflow"), Version: q.Get("version"), RetryOf: q.Get("retryOf")}
	if ph := q.Get("phase"); ph != "" {
		for _, p := range strings.Split(ph, ",") {
			f.Phases = append(f.Phases, run.Phase(p))
		}
	}
	f.Limit, _ = strconv.Atoi(q.Get("limit"))
	runs, err := a.svc.ListRuns(r.Context(), f)
	if err != nil {
		a.fail(w, err)
		return
	}
	out := make([]v1.Run, len(runs))
	for i, x := range runs {
		out[i] = runDTO(x)
	}
	writeJSON(w, http.StatusOK, list("WorkflowRun", out))
}

func (a *API) getRun(w http.ResponseWriter, r *http.Request) {
	x, err := a.svc.GetRun(r.Context(), r.PathValue("ns"), r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, runDTO(x))
}

func (a *API) getHistory(w http.ResponseWriter, r *http.Request) {
	evs, err := a.svc.History(r.Context(), r.PathValue("ns"), r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	out := make([]v1.HistoryEvent, len(evs))
	for i, ev := range evs {
		out[i] = historyDTO(ev)
	}
	writeJSON(w, http.StatusOK, list("HistoryEvent", out))
}

func (a *API) getTasks(w http.ResponseWriter, r *http.Request) {
	items, err := a.svc.RunTasks(r.Context(), r.PathValue("ns"), r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	var out []v1.TaskRun
	for _, it := range items {
		if it.Kind == "step" && r.URL.Query().Get("steps") != "true" {
			continue
		}
		out = append(out, taskDTO(it))
	}
	writeJSON(w, http.StatusOK, list("TaskRun", out))
}

// getLogs returns logs as JSON, or streams them as Server-Sent Events with
// ?follow=true (resumable with Last-Event-ID). The stream ends when the run
// reaches a terminal phase.
func (a *API) getLogs(w http.ResponseWriter, r *http.Request) {
	ns, id := r.PathValue("ns"), r.PathValue("id")
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	if le := r.Header.Get("Last-Event-ID"); le != "" {
		after, _ = strconv.ParseInt(le, 10, 64)
	}
	if r.URL.Query().Get("follow") != "true" {
		entries, err := a.svc.ListLogs(r.Context(), ns, id, after, 5000)
		if err != nil {
			a.fail(w, err)
			return
		}
		out := make([]v1.LogEntry, len(entries))
		for i, e := range entries {
			out[i] = logDTO(e)
		}
		writeJSON(w, http.StatusOK, list("LogEntry", out))
		return
	}
	if _, err := a.svc.GetRun(r.Context(), ns, id); err != nil {
		a.fail(w, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeStatus(w, http.StatusInternalServerError, "InternalError", "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	err := a.svc.FollowLogs(r.Context(), ns, id, after, func(e ports.LogEntry) error {
		b, _ := json.Marshal(logDTO(e))
		if _, err := fmt.Fprintf(w, "id: %d\nevent: log\ndata: %s\n\n", e.ID, b); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}, func() error {
		select {
		case <-keepalive.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return err
			}
			flusher.Flush()
		default:
		}
		return nil
	})
	if err != nil && r.Context().Err() == nil {
		fmt.Fprintf(w, "event: error\ndata: %q\n\n", err.Error())
	}
	if x, err := a.svc.GetRun(r.Context(), ns, id); err == nil {
		b, _ := json.Marshal(runDTO(x))
		fmt.Fprintf(w, "event: end\ndata: %s\n\n", b)
	}
	flusher.Flush()
}

func (a *API) cancelRun(w http.ResponseWriter, r *http.Request) {
	x, err := a.svc.CancelRun(r.Context(), r.PathValue("ns"), r.PathValue("id"), r.URL.Query().Get("reason"))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, runDTO(x))
}

func (a *API) resumeRun(w http.ResponseWriter, r *http.Request) {
	x, err := a.svc.ResumeRun(r.Context(), r.PathValue("ns"), r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, runDTO(x))
}

func (a *API) retryRun(w http.ResponseWriter, r *http.Request) {
	var body v1.RetryRunRequest
	if r.ContentLength != 0 {
		if err := decode(r, &body); err != nil {
			a.fail(w, err)
			return
		}
	}
	x, err := a.svc.RetryRun(r.Context(), usecase.RetryRunInput{
		Namespace: r.PathValue("ns"), ID: r.PathValue("id"), Mode: run.RetryMode(body.Mode),
		FromRef: body.FromRef, FromTask: body.FromTask, Inputs: body.Inputs, Version: body.Version,
	})
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, runDTO(x))
}

func (a *API) resolveTask(w http.ResponseWriter, r *http.Request) {
	ref, err := strconv.Atoi(r.PathValue("ref"))
	if err != nil {
		a.fail(w, fmt.Errorf("%w: ref must be an integer", usecase.ErrInvalidArgument))
		return
	}
	var body v1.ResolveTaskRequest
	if err := decode(r, &body); err != nil {
		a.fail(w, err)
		return
	}
	ns, id := r.PathValue("ns"), r.PathValue("id")
	if err := a.svc.ResolveTask(r.Context(), ns, id, ref, body.Outcome, body.Result, body.Message); err != nil {
		a.fail(w, err)
		return
	}
	a.getRun(w, r)
}

// --------------------------------------------------------------- events ----

func (a *API) postEvent(w http.ResponseWriter, r *http.Request) {
	var body v1.EmitEventRequest
	if err := decode(r, &body); err != nil {
		a.fail(w, err)
		return
	}
	ev, matched, err := a.svc.PostEvent(r.Context(), r.PathValue("ns"), body.Name, body.Payload)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, eventDTO(ev, matched))
}

func (a *API) listEvents(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	evs, err := a.svc.ListEvents(r.Context(), r.PathValue("ns"), r.URL.Query().Get("name"), limit)
	if err != nil {
		a.fail(w, err)
		return
	}
	out := make([]v1.Event, len(evs))
	for i, e := range evs {
		out[i] = eventDTO(e, nil)
	}
	writeJSON(w, http.StatusOK, list("Event", out))
}

// ------------------------------------------------------------ schedules ----

func (a *API) listSchedules(w http.ResponseWriter, r *http.Request) {
	scs, err := a.svc.ListSchedules(r.Context(), r.PathValue("ns"), r.URL.Query().Get("workflow"))
	if err != nil {
		a.fail(w, err)
		return
	}
	out := make([]v1.Schedule, len(scs))
	for i, sc := range scs {
		out[i] = scheduleDTO(sc)
	}
	writeJSON(w, http.StatusOK, list("Schedule", out))
}

func (a *API) getSchedule(w http.ResponseWriter, r *http.Request) {
	sc, err := a.svc.GetSchedule(r.Context(), r.PathValue("ns"), r.PathValue("name"))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, scheduleDTO(sc))
}

func (a *API) putSchedule(w http.ResponseWriter, r *http.Request) {
	var body v1.ScheduleSpec
	if err := decode(r, &body); err != nil {
		a.fail(w, err)
		return
	}
	sc, created, err := a.svc.PutSchedule(r.Context(), schedule.Schedule{
		Namespace: r.PathValue("ns"), Name: r.PathValue("name"), Workflow: body.Workflow, Cron: body.Cron, TZ: body.TZ,
		Inputs: body.Inputs, Missed: schedule.MissedPolicy(body.Missed), Paused: body.Paused,
	})
	if err != nil {
		a.fail(w, err)
		return
	}
	code := http.StatusOK
	if created {
		code = http.StatusCreated
	}
	writeJSON(w, code, scheduleDTO(sc))
}

func (a *API) deleteSchedule(w http.ResponseWriter, r *http.Request) {
	if err := a.svc.DeleteSchedule(r.Context(), r.PathValue("ns"), r.PathValue("name")); err != nil {
		a.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) pauseSchedule(paused bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sc, err := a.svc.SetSchedulePaused(r.Context(), r.PathValue("ns"), r.PathValue("name"), paused)
		if err != nil {
			a.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, scheduleDTO(sc))
	}
}

func (a *API) triggerSchedule(w http.ResponseWriter, r *http.Request) {
	x, err := a.svc.TriggerSchedule(r.Context(), r.PathValue("ns"), r.PathValue("name"))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, runDTO(x))
}

func (a *API) previewSchedule(w http.ResponseWriter, r *http.Request) {
	var body v1.SchedulePreviewRequest
	if err := decode(r, &body); err != nil {
		a.fail(w, err)
		return
	}
	p, err := a.svc.PreviewSchedule(body.Cron, body.TZ, 5)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v1.SchedulePreview{Description: p.Description, Upcoming: p.Upcoming})
}

// -------------------------------------------------------------- workers ----

func (a *API) listWorkers(w http.ResponseWriter, r *http.Request) {
	ws, err := a.svc.ListWorkers(r.Context(), scope(r))
	if err != nil {
		a.fail(w, err)
		return
	}
	sel := parseSelector(r.URL.Query().Get("labelSelector"))
	var out []v1.Worker
	for _, x := range ws {
		if sel.Matches(x.Labels) {
			out = append(out, workerDTO(x, x.Slots-x.FreeSlots))
		}
	}
	writeJSON(w, http.StatusOK, list("Worker", out))
}

func parseSelector(s string) worker.Selector {
	sel := worker.Selector{}
	for _, part := range strings.Split(s, ",") {
		if k, v, ok := strings.Cut(strings.TrimSpace(part), "="); ok {
			sel[k] = v
		}
	}
	return sel
}

func (a *API) getWorker(w http.ResponseWriter, r *http.Request) {
	x, active, err := a.svc.GetWorker(r.Context(), scope(r), r.PathValue("name"))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, workerDTO(x, active))
}

func (a *API) deleteWorker(w http.ResponseWriter, r *http.Request) {
	if err := a.svc.DeleteWorker(r.Context(), scope(r), r.PathValue("name")); err != nil {
		a.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) workerSchedulable(schedulable, drain bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		x, err := a.svc.SetWorkerSchedulable(r.Context(), scope(r), r.PathValue("name"), schedulable, drain)
		if err != nil {
			a.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, workerDTO(x, x.Slots-x.FreeSlots))
	}
}

// ---------------------------------------------------------------- pools ----

func (a *API) listPools(w http.ResponseWriter, r *http.Request) {
	ps, err := a.svc.ListPools(r.Context(), scope(r))
	if err != nil {
		a.fail(w, err)
		return
	}
	ws, err := a.svc.ListWorkers(r.Context(), scope(r))
	if err != nil {
		a.fail(w, err)
		return
	}
	out := make([]v1.Pool, len(ps))
	for i, p := range ps {
		out[i] = poolDTO(p, ws)
	}
	writeJSON(w, http.StatusOK, list("Pool", out))
}

func (a *API) getPool(w http.ResponseWriter, r *http.Request) {
	p, err := a.svc.GetPool(r.Context(), scope(r), r.PathValue("name"))
	if err != nil {
		a.fail(w, err)
		return
	}
	ws, _ := a.svc.ListWorkers(r.Context(), scope(r))
	writeJSON(w, http.StatusOK, poolDTO(p, ws))
}

func (a *API) putPool(w http.ResponseWriter, r *http.Request) {
	var spec v1.PoolSpec
	if r.ContentLength != 0 {
		if err := decode(r, &spec); err != nil {
			a.fail(w, err)
			return
		}
	}
	p := &worker.Pool{Name: r.PathValue("name"), Selector: spec.Selector, MaxConcurrentTasks: spec.MaxConcurrentTasks}
	if len(spec.ResourceProfiles) > 0 {
		p.ResourceProfiles = map[string]workflow.Resources{}
		for k, res := range spec.ResourceProfiles {
			p.ResourceProfiles[k] = workflow.Resources{CPU: res.CPU, MemoryBytes: res.MemoryBytes}
		}
	}
	if err := a.svc.PutPool(r.Context(), scope(r), p); err != nil {
		a.fail(w, err)
		return
	}
	a.getPool(w, r)
}

func (a *API) deletePool(w http.ResponseWriter, r *http.Request) {
	if err := a.svc.DeletePool(r.Context(), scope(r), r.PathValue("name")); err != nil {
		a.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) createToken(w http.ResponseWriter, r *http.Request) {
	var body v1.CreateJoinTokenRequest
	if r.ContentLength != 0 {
		if err := decode(r, &body); err != nil {
			a.fail(w, err)
			return
		}
	}
	ttl := time.Duration(body.TTLSeconds) * time.Second
	tok, plain, err := a.svc.CreateJoinToken(r.Context(), scope(r), r.PathValue("name"), ttl, body.MaxUses, body.AllowedLabels)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, tokenDTO(tok, plain))
}

func (a *API) listTokens(w http.ResponseWriter, r *http.Request) {
	toks, err := a.svc.ListJoinTokens(r.Context(), scope(r), r.PathValue("name"))
	if err != nil {
		a.fail(w, err)
		return
	}
	out := make([]v1.JoinToken, len(toks))
	for i, t := range toks {
		out[i] = tokenDTO(t, "")
	}
	writeJSON(w, http.StatusOK, list("JoinToken", out))
}

func (a *API) deleteToken(w http.ResponseWriter, r *http.Request) {
	if err := a.svc.DeleteJoinToken(r.Context(), scope(r), r.PathValue("name"), r.PathValue("id")); err != nil {
		a.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --------------------------------------------------------------- secrets ----
//
// /api/v1/secrets manages Shared secrets; /api/v1/namespaces/{ns}/secrets
// lists what a namespace sees (its Private secrets plus Shared ones), resolves
// keys the way runs do, and writes Private secrets (or Shared ones when the
// body says type "Shared").

// secretScope returns the type a request addresses: Shared on the cluster
// routes, otherwise the body/query type (default Private).
// sharedWriteAllowed: Shared secrets are visible to every namespace, so only
// the admin writes them.
func sharedWriteAllowed(w http.ResponseWriter, r *http.Request, typ secret.SecretType) bool {
	if typ == secret.Shared && !caller(r).admin {
		writeStatus(w, http.StatusForbidden, "Forbidden", "Shared secrets are managed by the admin")
		return false
	}
	return true
}

func secretScope(r *http.Request, bodyType string) (secret.SecretType, error) {
	shared := r.PathValue("ns") == ""
	typ := secret.SecretType(bodyType)
	if typ == "" {
		typ = secret.SecretType(r.URL.Query().Get("type"))
	}
	switch {
	case shared && (typ == "" || typ == secret.Shared):
		return secret.Shared, nil
	case shared:
		return "", fmt.Errorf("%w: /api/v1/secrets only holds Shared secrets; use /api/v1/namespaces/{ns}/secrets for type %q", usecase.ErrInvalidArgument, typ)
	case typ == "":
		return secret.Private, nil
	case typ == secret.Shared || typ == secret.Private:
		return typ, nil
	}
	return "", fmt.Errorf("%w: unknown secret type %q (want Private or Shared)", usecase.ErrInvalidArgument, typ)
}

func (a *API) listSecrets(w http.ResponseWriter, r *http.Request) {
	secrets, err := a.svc.ListSecrets(r.Context(), r.PathValue("ns"))
	if err != nil {
		a.fail(w, err)
		return
	}
	out := make([]v1.Secret, len(secrets))
	for i, s := range secrets {
		out[i] = secretDTO(s)
	}
	writeJSON(w, http.StatusOK, list("Secret", out))
}

func (a *API) getSecret(w http.ResponseWriter, r *http.Request) {
	var sec secret.Secret
	var err error
	nsp, key := r.PathValue("ns"), r.PathValue("key")
	if nsp != "" && r.URL.Query().Get("type") == "" {
		sec, err = a.svc.ResolveSecret(r.Context(), nsp, key)
	} else {
		var typ secret.SecretType
		if typ, err = secretScope(r, ""); err == nil {
			sec, err = a.svc.GetSecret(r.Context(), typ, nsp, key)
		}
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, secretDTO(sec))
}

func (a *API) createSecret(w http.ResponseWriter, r *http.Request) {
	var body v1.SecretSpec
	if err := decode(r, &body); err != nil {
		a.fail(w, err)
		return
	}
	typ, err := secretScope(r, body.Type)
	if err != nil {
		a.fail(w, err)
		return
	}
	if !sharedWriteAllowed(w, r, typ) {
		return
	}
	sec, err := a.svc.CreateSecret(r.Context(), usecase.SecretWrite{
		Namespace: r.PathValue("ns"), Key: body.Key, Type: typ, Value: body.Value, Sensitive: body.Sensitive,
	})
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, secretDTO(sec))
}

func (a *API) putSecret(w http.ResponseWriter, r *http.Request) {
	var body v1.SecretSpec
	if err := decode(r, &body); err != nil {
		a.fail(w, err)
		return
	}
	key := r.PathValue("key")
	if body.Key != "" && body.Key != key {
		a.fail(w, fmt.Errorf("%w: body key %q does not match path key %q", usecase.ErrInvalidArgument, body.Key, key))
		return
	}
	typ, err := secretScope(r, body.Type)
	if err != nil {
		a.fail(w, err)
		return
	}
	if !sharedWriteAllowed(w, r, typ) {
		return
	}
	sec, err := a.svc.PutSecret(r.Context(), usecase.SecretWrite{
		Namespace: r.PathValue("ns"), Key: key, Type: typ, Value: body.Value, Sensitive: body.Sensitive,
	})
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, secretDTO(sec))
}

func (a *API) deleteSecret(w http.ResponseWriter, r *http.Request) {
	typ, err := secretScope(r, "")
	if err != nil {
		a.fail(w, err)
		return
	}
	if !sharedWriteAllowed(w, r, typ) {
		return
	}
	if err := a.svc.DeleteSecret(r.Context(), typ, r.PathValue("ns"), r.PathValue("key")); err != nil {
		a.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
