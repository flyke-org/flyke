package server_test

// End-to-end tests of the control plane through its public protocols only:
// the REST API (as the CLI uses it) and the worker protocol via the generated
// Go Connect client (as a Go/Rust worker would).

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"connectrpc.com/connect"

	workerv1 "github.com/flyke-org/flyke/api/gen/flyke/worker/v1"
	"github.com/flyke-org/flyke/api/gen/flyke/worker/v1/workerv1connect"
	v1 "github.com/flyke-org/flyke/api/rest/v1"
	"github.com/flyke-org/flyke/flyke/server"
)

const manifest = `{
  "name": "Pipeline", "entrypoint": "p.py:Pipeline", "runtime": "python/3.12",
  "inputs_schema": {"type":"object","properties":{"key":{"type":"string"},"n":{"type":"integer","default":2}},"required":["key"],"additionalProperties":false},
  "concurrency": {"key": "{key}", "limit": 1, "on_conflict": "queue"},
  "tasks": {"work": {"retry": {"attempts": 2, "backoff": "fixed", "delay_ms": 10}}}
}`

type harness struct {
	t      *testing.T
	srv    *server.Server
	worker workerv1connect.WorkerServiceClient
	cred   string
}

func start(t *testing.T) *harness {
	t.Helper()
	srv, err := server.New(server.Config{
		Addr: "127.0.0.1:0", InMemory: true, ResyncInterval: 50 * time.Millisecond,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	h := &harness{t: t, srv: srv, worker: workerv1connect.NewWorkerServiceClient(http.DefaultClient, srv.URL())}

	token, err := srv.CreateJoinToken(context.Background(), "default", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := h.worker.Register(context.Background(), connect.NewRequest(&workerv1.RegisterRequest{
		JoinToken: token, Hostname: "test-host", Runtimes: []string{"python/3.12"},
		Capacity: &workerv1.Capacity{Slots: 4}, ProtocolVersion: "v1",
	}))
	if err != nil {
		t.Fatal(err)
	}
	h.cred = reg.Msg.Credential
	for _, ns := range []string{"team-a", "team-b", "other"} {
		h.rest(http.MethodPost, "/api/v1/namespaces", v1.CreateNamespaceRequest{Name: ns}, nil)
	}
	h.rest(http.MethodPut, "/api/v1/namespaces/default/workflows/Pipeline",
		v1.WorkflowSpec{Manifest: json.RawMessage(manifest), Source: v1.Source{LocalRoot: "/code"}}, nil)
	return h
}

func (h *harness) rest(method, path string, body, out any) int {
	h.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, h.srv.URL()+path, r)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func auth[T any](h *harness, msg *T) *connect.Request[T] {
	req := connect.NewRequest(msg)
	req.Header().Set("Authorization", "Bearer "+h.cred)
	return req
}

func (h *harness) poll() *workerv1.Assignment {
	h.t.Helper()
	resp, err := h.worker.Poll(context.Background(), auth(h, &workerv1.PollRequest{MaxItems: 1, WaitSeconds: 5}))
	if err != nil {
		h.t.Fatal(err)
	}
	if len(resp.Msg.Assignments) != 1 {
		h.t.Fatalf("expected 1 assignment, got %d", len(resp.Msg.Assignments))
	}
	return resp.Msg.Assignments[0]
}

func payload(v any) *workerv1.Payload {
	b, _ := json.Marshal(v)
	return &workerv1.Payload{Encoding: "json/plain", Data: b}
}

func (h *harness) run(id string) v1.Run {
	var r v1.Run
	h.rest(http.MethodGet, "/api/v1/namespaces/default/runs/"+id, nil, &r)
	return r
}

func (h *harness) waitPhase(id, phase string) v1.Run {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if r := h.run(id); r.Status.Phase == phase {
			return r
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.t.Fatalf("run %s never reached %s (now %s)", id, phase, h.run(id).Status.Phase)
	return v1.Run{}
}

func TestWorkflowLifecycleOverTheWorkerProtocol(t *testing.T) {
	h := start(t)
	ctx := context.Background()

	// Invalid inputs are rejected by the schema.
	if code := h.rest(http.MethodPost, "/api/v1/namespaces/default/workflows/Pipeline/runs", v1.StartRunRequest{Inputs: json.RawMessage(`{"nope":1}`)}, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for bad inputs, got %d", code)
	}
	var created v1.Run
	h.rest(http.MethodPost, "/api/v1/namespaces/default/workflows/Pipeline/runs", v1.StartRunRequest{Inputs: json.RawMessage(`{"key":"a"}`)}, &created)
	if created.Status.ConcurrencyKey != "a" {
		t.Fatalf("concurrency key not rendered: %+v", created.Status)
	}

	// Step 1: the worker replays the body and asks for a task.
	step := h.poll()
	if step.GetStep() == nil || len(step.GetStep().History) != 1 {
		t.Fatalf("expected a first workflow step with RunStarted, got %+v", step)
	}
	var inputs map[string]any
	_ = json.Unmarshal(step.Inputs.Data, &inputs)
	if inputs["n"] != float64(2) {
		t.Fatalf("schema default not applied: %v", inputs)
	}
	_, err := h.worker.CompleteStep(ctx, auth(h, &workerv1.CompleteStepRequest{LeaseId: step.LeaseId, Commands: []*workerv1.Command{
		{Ref: 1, Kind: &workerv1.Command_ScheduleTask{ScheduleTask: &workerv1.ScheduleTask{Name: "work", Args: payload([]any{1})}}},
	}}))
	if err != nil {
		t.Fatal(err)
	}

	// The task fails once (retryable) and is retried with attempt 2.
	a1 := h.poll()
	if a1.GetTask().GetName() != "work" || a1.GetTask().GetAttempt() != 1 {
		t.Fatalf("unexpected assignment %+v", a1)
	}
	if ack, _ := h.worker.AckTask(ctx, auth(h, &workerv1.AckTaskRequest{LeaseId: a1.LeaseId})); !ack.Msg.Proceed {
		t.Fatal("ack refused")
	}
	if _, err := h.worker.FailTask(ctx, auth(h, &workerv1.FailTaskRequest{LeaseId: a1.LeaseId, Failure: &workerv1.Failure{Type: "builtins.OSError", Message: "flake"}})); err != nil {
		t.Fatal(err)
	}
	a2 := h.poll()
	if a2.GetTask().GetAttempt() != 2 || a2.LeaseId == a1.LeaseId {
		t.Fatalf("expected attempt 2 under a new lease, got %+v", a2)
	}

	// Fencing: a report on the old lease is rejected.
	_, err = h.worker.CompleteTask(ctx, auth(h, &workerv1.CompleteTaskRequest{LeaseId: a1.LeaseId, Result: payload("stale")}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("stale lease must be fenced, got %v", err)
	}
	h.worker.AckTask(ctx, auth(h, &workerv1.AckTaskRequest{LeaseId: a2.LeaseId}))
	if _, err := h.worker.CompleteTask(ctx, auth(h, &workerv1.CompleteTaskRequest{LeaseId: a2.LeaseId, Result: payload(42)})); err != nil {
		t.Fatal(err)
	}

	// While the first run holds key "a", a second run with the same key queues.
	var second v1.Run
	h.rest(http.MethodPost, "/api/v1/namespaces/default/workflows/Pipeline/runs", v1.StartRunRequest{Inputs: json.RawMessage(`{"key":"a"}`)}, &second)
	h.waitPhase(second.Metadata.Name, "Queued")

	// Step 2: history now has the task result; the body completes the run.
	step2 := h.poll()
	hist := step2.GetStep().GetHistory()
	last := hist[len(hist)-1]
	if last.Type != workerv1.EventType_EVENT_TYPE_TASK_COMPLETED || string(last.Payload.Data) != "42" {
		t.Fatalf("expected TaskCompleted(42) last in history, got %v", last)
	}
	if _, err := h.worker.CompleteStep(ctx, auth(h, &workerv1.CompleteStepRequest{LeaseId: step2.LeaseId, Commands: []*workerv1.Command{
		{Ref: 2, Kind: &workerv1.Command_CompleteRun{CompleteRun: &workerv1.CompleteRun{Result: payload(map[string]int{"total": 42})}}},
	}})); err != nil {
		t.Fatal(err)
	}
	done := h.waitPhase(created.Metadata.Name, "Succeeded")
	var result map[string]int
	if err := json.Unmarshal(done.Status.Result, &result); err != nil || result["total"] != 42 {
		t.Fatalf("unexpected result %s", done.Status.Result)
	}
	// The queued run is admitted once the slot frees up.
	h.waitPhase(second.Metadata.Name, "Running")
}

func TestWaitForEventAndInvalidCredential(t *testing.T) {
	h := start(t)
	ctx := context.Background()

	_, err := h.worker.Heartbeat(ctx, connect.NewRequest(&workerv1.HeartbeatRequest{}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("missing credential must be rejected, got %v", err)
	}

	var r v1.Run
	h.rest(http.MethodPost, "/api/v1/namespaces/default/workflows/Pipeline/runs", v1.StartRunRequest{Inputs: json.RawMessage(`{"key":"b"}`)}, &r)
	step := h.poll()
	h.worker.CompleteStep(ctx, auth(h, &workerv1.CompleteStepRequest{LeaseId: step.LeaseId, Commands: []*workerv1.Command{
		{Ref: 1, Kind: &workerv1.Command_WaitEvent{WaitEvent: &workerv1.WaitEvent{Name: "approved", Where: payload(map[string]string{"key": "b"})}}},
	}}))
	h.waitPhase(r.Metadata.Name, "Waiting")

	var ev v1.Event
	h.rest(http.MethodPost, "/api/v1/namespaces/default/events", v1.EmitEventRequest{Name: "approved", Payload: json.RawMessage(`{"key":"other"}`)}, &ev)
	if len(ev.Matched) != 0 {
		t.Fatal("non-matching event must not resume the run")
	}
	h.rest(http.MethodPost, "/api/v1/namespaces/default/events", v1.EmitEventRequest{Name: "approved", Payload: json.RawMessage(`{"key":"b","by":"alice"}`)}, &ev)
	if len(ev.Matched) != 1 || ev.Matched[0] != r.Metadata.Name {
		t.Fatalf("expected the waiting run to match, got %v", ev.Matched)
	}
	step2 := h.poll()
	hist := step2.GetStep().GetHistory()
	if last := hist[len(hist)-1]; last.Type != workerv1.EventType_EVENT_TYPE_EVENT_RECEIVED || last.Ref != 1 {
		t.Fatalf("expected EventReceived for ref 1, got %v", last)
	}
}

func TestServesUIWithSPAFallback(t *testing.T) {
	ui := fstest.MapFS{
		"index.html":     {Data: []byte("<html>flyke ui</html>")},
		"assets/app.js": {Data: []byte("console.log(1)")},
	}
	srv, err := server.New(server.Config{Addr: "127.0.0.1:0", InMemory: true, UI: ui, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer srv.Shutdown(context.Background())
	get := func(path string) (int, string, string) {
		resp, err := http.Get(srv.URL() + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), resp.Header.Get("Cache-Control")
	}
	if code, body, _ := get("/runs/some-run"); code != 200 || body != "<html>flyke ui</html>" {
		t.Fatalf("client route must fall back to index.html: %d %q", code, body)
	}
	if _, body, cache := get("/assets/app.js"); body != "console.log(1)" || !strings.Contains(cache, "immutable") {
		t.Fatalf("assets must be served with long cache: %q %q", body, cache)
	}
	if code, _, _ := get("/api/v1/pools"); code != 200 {
		t.Fatalf("API must still win over the UI, got %d", code)
	}
}

// fakeWorker drives runs through the worker protocol: steps are answered by
// body(history), tasks by exec(ref, args). It records the tasks it executed.
type fakeWorker struct {
	h        *harness
	body     func(hist []*workerv1.HistoryEvent) []*workerv1.Command
	exec     func(ref int32, args string) (result any, fail *workerv1.Failure)
	executed []string // "<run>:<ref>"
}

func (f *fakeWorker) until(id string, phases ...string) v1.Run {
	f.h.t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		r := f.h.run(id)
		for _, p := range phases {
			if r.Status.Phase == p {
				return r
			}
		}
		resp, err := f.h.worker.Poll(ctx, auth(f.h, &workerv1.PollRequest{MaxItems: 1, WaitSeconds: 1}))
		if err != nil {
			f.h.t.Fatal(err)
		}
		for _, a := range resp.Msg.Assignments {
			if st := a.GetStep(); st != nil {
				if _, err := f.h.worker.CompleteStep(ctx, auth(f.h, &workerv1.CompleteStepRequest{LeaseId: a.LeaseId, Commands: f.body(st.History)})); err != nil {
					f.h.t.Fatal(err)
				}
				continue
			}
			tk := a.GetTask()
			f.h.worker.AckTask(ctx, auth(f.h, &workerv1.AckTaskRequest{LeaseId: a.LeaseId}))
			f.executed = append(f.executed, a.RunId+":"+string(rune('0'+tk.Ref)))
			res, fail := f.exec(tk.Ref, string(tk.Args.GetData()))
			if fail != nil {
				_, err = f.h.worker.FailTask(ctx, auth(f.h, &workerv1.FailTaskRequest{LeaseId: a.LeaseId, Failure: fail}))
			} else {
				_, err = f.h.worker.CompleteTask(ctx, auth(f.h, &workerv1.CompleteTaskRequest{LeaseId: a.LeaseId, Result: payload(res)}))
			}
			if err != nil {
				f.h.t.Fatal(err)
			}
		}
	}
	f.h.t.Fatalf("run %s never reached %v (now %s)", id, phases, f.h.run(id).Status.Phase)
	return v1.Run{}
}

func TestRetryReusesRecordedOutcomes(t *testing.T) {
	h := start(t)
	broken := true
	// Body: work(1) then work(2), sequentially; fails the run if a task failed.
	body := func(hist []*workerv1.HistoryEvent) []*workerv1.Command {
		done := map[int32]bool{}
		scheduled := map[int32]bool{}
		for _, ev := range hist {
			switch ev.Type {
			case workerv1.EventType_EVENT_TYPE_TASK_SCHEDULED:
				scheduled[ev.Ref] = true
			case workerv1.EventType_EVENT_TYPE_TASK_COMPLETED:
				done[ev.Ref] = true
			case workerv1.EventType_EVENT_TYPE_TASK_FAILED:
				return []*workerv1.Command{{Ref: 3, Kind: &workerv1.Command_FailRun{FailRun: &workerv1.FailRun{Failure: ev.Failure}}}}
			}
		}
		schedule := func(ref int32) []*workerv1.Command {
			return []*workerv1.Command{{Ref: ref, Kind: &workerv1.Command_ScheduleTask{ScheduleTask: &workerv1.ScheduleTask{Name: "work", Args: payload([]any{ref})}}}}
		}
		switch {
		case !scheduled[1]:
			return schedule(1)
		case done[1] && !scheduled[2]:
			return schedule(2)
		case done[2]:
			return []*workerv1.Command{{Ref: 3, Kind: &workerv1.Command_CompleteRun{CompleteRun: &workerv1.CompleteRun{Result: payload("ok")}}}}
		}
		return nil
	}
	exec := func(ref int32, _ string) (any, *workerv1.Failure) {
		if ref == 2 && broken {
			return nil, &workerv1.Failure{Type: "builtins.ZeroDivisionError", Message: "division by zero", NonRetryable: true}
		}
		return ref * 10, nil
	}
	f := &fakeWorker{h: h, body: body, exec: exec}

	var src v1.Run
	h.rest(http.MethodPost, "/api/v1/namespaces/default/workflows/Pipeline/runs", v1.StartRunRequest{Inputs: json.RawMessage(`{"key":"r"}`)}, &src)
	f.until(src.Metadata.Name, "Failed")
	id := src.Metadata.Name

	// A live run cannot be retried.
	var live v1.Run
	h.rest(http.MethodPost, "/api/v1/namespaces/default/workflows/Pipeline/runs", v1.StartRunRequest{Inputs: json.RawMessage(`{"key":"live"}`)}, &live)
	if code := h.rest(http.MethodPost, "/api/v1/namespaces/default/runs/"+live.Metadata.Name+"/retry", nil, nil); code == http.StatusCreated {
		t.Fatal("retrying a live run must be refused")
	}
	h.rest(http.MethodPost, "/api/v1/namespaces/default/runs/"+live.Metadata.Name+"/cancel", nil, nil)

	// The bug is fixed; "failed" mode re-runs only work(2).
	broken = false
	var retry v1.Run
	if code := h.rest(http.MethodPost, "/api/v1/namespaces/default/runs/"+id+"/retry", v1.RetryRunRequest{}, &retry); code != http.StatusCreated {
		t.Fatalf("retry: %d", code)
	}
	if retry.Spec.Retry == nil || retry.Spec.Retry.Of != id || retry.Spec.Retry.Mode != "failed" {
		t.Fatalf("retry not linked: %+v", retry.Spec.Retry)
	}
	f.executed = nil
	f.until(retry.Metadata.Name, "Succeeded")
	if len(f.executed) != 1 || f.executed[0] != retry.Metadata.Name+":2" {
		t.Fatalf("only work(2) should execute, got %v", f.executed)
	}
	var tasks v1.List[v1.TaskRun]
	h.rest(http.MethodGet, "/api/v1/namespaces/default/runs/"+retry.Metadata.Name+"/tasks", nil, &tasks)
	var reused *v1.TaskRun
	for i, tk := range tasks.Items {
		if tk.Kind == "task" && tk.Ref == 1 {
			reused = &tasks.Items[i]
		}
	}
	if reused == nil || reused.ReusedFrom != id || reused.Phase != "Succeeded" || string(reused.Result) != "10" {
		t.Fatalf("work(1) should be reused from %s, got %+v", id, reused)
	}

	// "from" mode re-runs the chosen step and everything after it.
	var from v1.Run
	h.rest(http.MethodPost, "/api/v1/namespaces/default/runs/"+id+"/retry", v1.RetryRunRequest{Mode: "from", FromRef: 1}, &from)
	f.executed = nil
	f.until(from.Metadata.Name, "Succeeded")
	if len(f.executed) != 2 {
		t.Fatalf("retry from ref 1 must execute both tasks, got %v", f.executed)
	}
	if code := h.rest(http.MethodPost, "/api/v1/namespaces/default/runs/"+id+"/retry", v1.RetryRunRequest{Mode: "from", FromTask: "nope"}, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown step must be rejected with 422, got %d", code)
	}

	// The source run lists its retries.
	var retries v1.List[v1.Run]
	h.rest(http.MethodGet, "/api/v1/namespaces/default/runs?retryOf="+id, nil, &retries)
	if len(retries.Items) != 2 {
		t.Fatalf("expected 2 retries of %s, got %d", id, len(retries.Items))
	}
}

// A compensated effectively-once task must not be deduplicated by its
// idempotency key: its effect was undone.
func TestCompensatedTaskIsNotDeduplicated(t *testing.T) {
	h := start(t)
	saga := `{"name": "Saga", "entrypoint": "s.py:Saga", "runtime": "python/3.12",
	  "tasks": {"prov": {"guarantee": "effectively_once", "compensation": "unprov"}},
	  "compensations": {"unprov": {}}}`
	h.rest(http.MethodPut, "/api/v1/namespaces/default/workflows/Saga",
		v1.WorkflowSpec{Manifest: json.RawMessage(saga), Source: v1.Source{LocalRoot: "/code"}}, nil)
	fail := true
	body := func(hist []*workerv1.HistoryEvent) []*workerv1.Command {
		for _, ev := range hist {
			if ev.Type == workerv1.EventType_EVENT_TYPE_TASK_COMPLETED {
				if fail {
					return []*workerv1.Command{{Ref: 2, Kind: &workerv1.Command_FailRun{FailRun: &workerv1.FailRun{Failure: &workerv1.Failure{Type: "x.Boom", Message: "boom"}}}}}
				}
				return []*workerv1.Command{{Ref: 2, Kind: &workerv1.Command_CompleteRun{CompleteRun: &workerv1.CompleteRun{Result: payload("ok")}}}}
			}
		}
		return []*workerv1.Command{{Ref: 1, Kind: &workerv1.Command_ScheduleTask{ScheduleTask: &workerv1.ScheduleTask{Name: "prov", Args: payload([]any{}), IdempotencyKey: "res-1"}}}}
	}
	f := &fakeWorker{h: h, body: body, exec: func(int32, string) (any, *workerv1.Failure) { return "created", nil }}
	var r1 v1.Run
	h.rest(http.MethodPost, "/api/v1/namespaces/default/workflows/Saga/runs", v1.StartRunRequest{}, &r1)
	if got := f.until(r1.Metadata.Name, "Failed"); got.Status.Compensation != "Completed" {
		t.Fatalf("expected the run to compensate, got %+v", got.Status)
	}
	fail = false
	f.executed = nil
	var r2 v1.Run
	h.rest(http.MethodPost, "/api/v1/namespaces/default/runs/"+r1.Metadata.Name+"/retry", v1.RetryRunRequest{}, &r2)
	f.until(r2.Metadata.Name, "Succeeded")
	if len(f.executed) != 1 {
		t.Fatalf("prov was compensated, so it must run again (neither reused nor deduplicated): executed %v", f.executed)
	}
}
