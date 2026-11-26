// Package workerapi exposes the worker protocol (flyke.worker.v1) over
// Connect, which serves gRPC, gRPC-Web and HTTP/1.1+JSON from one handler.
// It only translates between protobuf messages and use cases.
package workerapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	workerv1 "github.com/flyke-org/flyke/api/gen/flyke/worker/v1"
	"github.com/flyke-org/flyke/api/gen/flyke/worker/v1/workerv1connect"
	"github.com/flyke-org/flyke/flyke/internal/app/usecase"
	"github.com/flyke-org/flyke/flyke/internal/domain/run"
	"github.com/flyke-org/flyke/flyke/internal/domain/task"
	"github.com/flyke-org/flyke/flyke/internal/domain/worker"
	"github.com/flyke-org/flyke/flyke/internal/domain/workflow"
)

const ProtocolVersion = "v1"

const encodingJSON = "json/plain"

type Handler struct {
	svc *usecase.Service
}

var _ workerv1connect.WorkerServiceHandler = (*Handler)(nil)

// New returns the mount path and HTTP handler of the worker service.
func New(svc *usecase.Service) (string, http.Handler) {
	return workerv1connect.NewWorkerServiceHandler(&Handler{svc: svc})
}

func (h *Handler) auth(ctx context.Context, hdr http.Header) (*worker.Worker, error) {
	raw := strings.TrimSpace(strings.TrimPrefix(hdr.Get("Authorization"), "Bearer "))
	if raw == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing worker credential"))
	}
	w, err := h.svc.AuthenticateWorker(ctx, raw)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid worker credential"))
	}
	return w, nil
}

func toConnect(err error) error {
	if err == nil {
		return nil
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		return err
	}
	code := connect.CodeInternal
	switch {
	case errors.Is(err, usecase.ErrNotFound):
		code = connect.CodeNotFound
	case errors.Is(err, usecase.ErrInvalidArgument):
		code = connect.CodeInvalidArgument
	case errors.Is(err, usecase.ErrFailedPrecondition):
		code = connect.CodeFailedPrecondition
	case errors.Is(err, usecase.ErrUnauthenticated):
		code = connect.CodeUnauthenticated
	case errors.Is(err, usecase.ErrPermissionDenied):
		code = connect.CodePermissionDenied
	case errors.Is(err, context.Canceled):
		code = connect.CodeCanceled
	}
	return connect.NewError(code, err)
}

// ------------------------------------------------------------- payloads ----

func payload(raw json.RawMessage) *workerv1.Payload {
	if len(raw) == 0 {
		return nil
	}
	return &workerv1.Payload{Encoding: encodingJSON, Data: raw}
}

func fromPayload(p *workerv1.Payload) (json.RawMessage, error) {
	if p == nil || len(p.Data) == 0 {
		return nil, nil
	}
	if p.Encoding != "" && p.Encoding != encodingJSON {
		return nil, fmt.Errorf("unsupported payload encoding %q", p.Encoding)
	}
	if !json.Valid(p.Data) {
		return nil, errors.New("payload is not valid JSON")
	}
	return json.RawMessage(p.Data), nil
}

func failureToProto(f *workflow.Failure) *workerv1.Failure {
	if f == nil {
		return nil
	}
	return &workerv1.Failure{Type: f.Type, Mro: f.MRO, Message: f.Message, Stack: f.Stack, NonRetryable: f.NonRetryable}
}

func failureFromProto(f *workerv1.Failure) workflow.Failure {
	if f == nil {
		return workflow.Failure{Type: "flyke.UnknownError", Message: "worker reported no failure details"}
	}
	return workflow.Failure{Type: f.Type, MRO: f.Mro, Message: f.Message, Stack: f.Stack, NonRetryable: f.NonRetryable}
}

var eventTypes = map[run.EventType]workerv1.EventType{
	run.EvRunStarted:      workerv1.EventType_EVENT_TYPE_RUN_STARTED,
	run.EvTaskScheduled:   workerv1.EventType_EVENT_TYPE_TASK_SCHEDULED,
	run.EvTaskCompleted:   workerv1.EventType_EVENT_TYPE_TASK_COMPLETED,
	run.EvTaskFailed:      workerv1.EventType_EVENT_TYPE_TASK_FAILED,
	run.EvTimerStarted:    workerv1.EventType_EVENT_TYPE_TIMER_STARTED,
	run.EvTimerFired:      workerv1.EventType_EVENT_TYPE_TIMER_FIRED,
	run.EvWaitStarted:     workerv1.EventType_EVENT_TYPE_WAIT_STARTED,
	run.EvEventReceived:   workerv1.EventType_EVENT_TYPE_EVENT_RECEIVED,
	run.EvWaitTimedOut:    workerv1.EventType_EVENT_TYPE_WAIT_TIMED_OUT,
	run.EvEventEmitted:    workerv1.EventType_EVENT_TYPE_EVENT_EMITTED,
	run.EvRunCompleted:    workerv1.EventType_EVENT_TYPE_RUN_COMPLETED,
	run.EvRunFailed:       workerv1.EventType_EVENT_TYPE_RUN_FAILED,
	run.EvCancelRequested: workerv1.EventType_EVENT_TYPE_CANCEL_REQUESTED,
}

func historyToProto(evs []run.HistoryEvent) []*workerv1.HistoryEvent {
	out := make([]*workerv1.HistoryEvent, len(evs))
	for i, ev := range evs {
		out[i] = &workerv1.HistoryEvent{
			Id: int32(ev.ID), Type: eventTypes[ev.Type], Time: timestamppb.New(ev.Time),
			Ref: int32(ev.Ref), Name: ev.Name, Payload: payload(ev.Payload), Failure: failureToProto(ev.Failure),
		}
	}
	return out
}

func commandsFromProto(cmds []*workerv1.Command) ([]run.Command, error) {
	out := make([]run.Command, 0, len(cmds))
	for _, c := range cmds {
		cmd := run.Command{Ref: int(c.GetRef())}
		var err error
		switch k := c.Kind.(type) {
		case *workerv1.Command_ScheduleTask:
			cmd.Kind, cmd.Name, cmd.IdempotencyKey = run.CmdScheduleTask, k.ScheduleTask.GetName(), k.ScheduleTask.GetIdempotencyKey()
			cmd.Payload, err = fromPayload(k.ScheduleTask.GetArgs())
		case *workerv1.Command_StartTimer:
			cmd.Kind = run.CmdStartTimer
			cmd.Duration = time.Duration(k.StartTimer.GetDurationMs()) * time.Millisecond
		case *workerv1.Command_WaitEvent:
			cmd.Kind, cmd.Name = run.CmdWaitEvent, k.WaitEvent.GetName()
			cmd.Duration = time.Duration(k.WaitEvent.GetTimeoutMs()) * time.Millisecond
			var where json.RawMessage
			if where, err = fromPayload(k.WaitEvent.GetWhere()); err == nil && len(where) > 0 {
				err = json.Unmarshal(where, &cmd.Where)
			}
		case *workerv1.Command_EmitEvent:
			cmd.Kind, cmd.Name = run.CmdEmitEvent, k.EmitEvent.GetName()
			cmd.Payload, err = fromPayload(k.EmitEvent.GetPayload())
		case *workerv1.Command_CompleteRun:
			cmd.Kind = run.CmdCompleteRun
			cmd.Payload, err = fromPayload(k.CompleteRun.GetResult())
			if cmd.Payload == nil {
				cmd.Payload = json.RawMessage("null")
			}
		case *workerv1.Command_FailRun:
			cmd.Kind = run.CmdFailRun
			f := failureFromProto(k.FailRun.GetFailure())
			cmd.Failure = &f
		default:
			return nil, fmt.Errorf("command ref %d: unknown kind", c.GetRef())
		}
		if err != nil {
			return nil, fmt.Errorf("command ref %d: %w", c.GetRef(), err)
		}
		out = append(out, cmd)
	}
	return out, nil
}

func assignmentToProto(a usecase.Assignment) *workerv1.Assignment {
	it := a.Item
	pa := &workerv1.Assignment{
		LeaseId:   it.LeaseID,
		RunId:     a.Run.ID,
		Namespace: a.Run.Namespace,
		Workflow:  a.Run.Workflow,
		WorkId:    it.ID,
		Source: &workerv1.Source{
			Entrypoint:   a.Workflow.Source.Entrypoint,
			LocalRoot:    a.Workflow.Source.LocalRoot,
			BundleDigest: a.Workflow.Source.BundleDigest,
		},
		Inputs: payload(a.Run.Inputs),
	}
	if it.Deadline != nil {
		pa.Deadline = timestamppb.New(*it.Deadline)
	}
	if it.Kind == task.KindStep {
		pa.Work = &workerv1.Assignment_Step{Step: &workerv1.StepWork{History: historyToProto(a.History)}}
		return pa
	}
	args := it.Args
	if len(args) == 0 {
		args = json.RawMessage("[]")
	}
	tw := &workerv1.TaskWork{
		TaskId: it.ID, Ref: int32(it.Ref), Name: it.Name, Attempt: int32(it.Attempt),
		Args: payload(args), IdempotencyKey: it.IdempotencyKey,
	}
	if it.Kind == task.KindCompensation {
		tw.Compensation = true
		tw.CompensatedTask = it.CompensatedTask
		res := it.Result
		if len(res) == 0 {
			res = json.RawMessage("null")
		}
		tw.CompensatedResult = payload(res)
	}
	pa.Work = &workerv1.Assignment_Task{Task: tw}
	return pa
}

// ------------------------------------------------------------------ rpcs ----

func (h *Handler) Register(ctx context.Context, req *connect.Request[workerv1.RegisterRequest]) (*connect.Response[workerv1.RegisterResponse], error) {
	m := req.Msg
	if m.GetProtocolVersion() != "" && m.GetProtocolVersion() != ProtocolVersion {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("unsupported protocol version %q (server speaks %s)", m.GetProtocolVersion(), ProtocolVersion))
	}
	in := usecase.RegisterInput{
		JoinToken: m.GetJoinToken(), Name: m.GetName(), Hostname: m.GetHostname(), Labels: m.GetLabels(),
		Runtimes: m.GetRuntimes(), AgentVersion: m.GetAgentVersion(),
	}
	if c := m.GetCapacity(); c != nil {
		in.Slots = int(c.GetSlots())
		in.Capacity = workflow.Resources{CPU: c.GetResources().GetCpu(), MemoryBytes: c.GetResources().GetMemoryBytes()}
	}
	reg, err := h.svc.RegisterWorker(ctx, in)
	if err != nil {
		return nil, toConnect(err)
	}
	cfg := h.svc.Config()
	return connect.NewResponse(&workerv1.RegisterResponse{
		WorkerName: reg.Worker.Name, Credential: reg.Credential, Labels: reg.Worker.Labels,
		HeartbeatIntervalSeconds: int32(cfg.HeartbeatInterval / time.Second),
		LeaseTtlSeconds:          int32(cfg.LeaseTTL / time.Second),
	}), nil
}

func (h *Handler) Deregister(ctx context.Context, req *connect.Request[workerv1.DeregisterRequest]) (*connect.Response[workerv1.DeregisterResponse], error) {
	w, err := h.auth(ctx, req.Header())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&workerv1.DeregisterResponse{}), toConnect(h.svc.DeregisterWorker(ctx, w))
}

func (h *Handler) Heartbeat(ctx context.Context, req *connect.Request[workerv1.HeartbeatRequest]) (*connect.Response[workerv1.HeartbeatResponse], error) {
	w, err := h.auth(ctx, req.Header())
	if err != nil {
		return nil, err
	}
	res, err := h.svc.Heartbeat(ctx, w, int(req.Msg.GetFreeSlots()), req.Msg.GetActiveLeases())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&workerv1.HeartbeatResponse{CancelLeases: res.CancelLeases, Drain: res.Drain}), nil
}

func (h *Handler) Poll(ctx context.Context, req *connect.Request[workerv1.PollRequest]) (*connect.Response[workerv1.PollResponse], error) {
	w, err := h.auth(ctx, req.Header())
	if err != nil {
		return nil, err
	}
	wait := time.Duration(req.Msg.GetWaitSeconds()) * time.Second
	as, err := h.svc.Poll(ctx, w, int(req.Msg.GetMaxItems()), wait)
	if err != nil {
		return nil, toConnect(err)
	}
	out := &workerv1.PollResponse{}
	for _, a := range as {
		out.Assignments = append(out.Assignments, assignmentToProto(a))
	}
	return connect.NewResponse(out), nil
}

func (h *Handler) AckTask(ctx context.Context, req *connect.Request[workerv1.AckTaskRequest]) (*connect.Response[workerv1.AckTaskResponse], error) {
	w, err := h.auth(ctx, req.Header())
	if err != nil {
		return nil, err
	}
	ok, err := h.svc.AckTask(ctx, w, req.Msg.GetLeaseId())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&workerv1.AckTaskResponse{Proceed: ok}), nil
}

func (h *Handler) CompleteTask(ctx context.Context, req *connect.Request[workerv1.CompleteTaskRequest]) (*connect.Response[workerv1.CompleteTaskResponse], error) {
	w, err := h.auth(ctx, req.Header())
	if err != nil {
		return nil, err
	}
	res, err := fromPayload(req.Msg.GetResult())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&workerv1.CompleteTaskResponse{}), toConnect(h.svc.CompleteTask(ctx, w, req.Msg.GetLeaseId(), res))
}

func (h *Handler) FailTask(ctx context.Context, req *connect.Request[workerv1.FailTaskRequest]) (*connect.Response[workerv1.FailTaskResponse], error) {
	w, err := h.auth(ctx, req.Header())
	if err != nil {
		return nil, err
	}
	f := failureFromProto(req.Msg.GetFailure())
	return connect.NewResponse(&workerv1.FailTaskResponse{}), toConnect(h.svc.FailTask(ctx, w, req.Msg.GetLeaseId(), f))
}

func (h *Handler) CompleteStep(ctx context.Context, req *connect.Request[workerv1.CompleteStepRequest]) (*connect.Response[workerv1.CompleteStepResponse], error) {
	w, err := h.auth(ctx, req.Header())
	if err != nil {
		return nil, err
	}
	cmds, err := commandsFromProto(req.Msg.GetCommands())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&workerv1.CompleteStepResponse{}), toConnect(h.svc.CompleteStep(ctx, w, req.Msg.GetLeaseId(), cmds))
}

func (h *Handler) FailStep(ctx context.Context, req *connect.Request[workerv1.FailStepRequest]) (*connect.Response[workerv1.FailStepResponse], error) {
	w, err := h.auth(ctx, req.Header())
	if err != nil {
		return nil, err
	}
	f := failureFromProto(req.Msg.GetFailure())
	return connect.NewResponse(&workerv1.FailStepResponse{}), toConnect(h.svc.FailStep(ctx, w, req.Msg.GetLeaseId(), f))
}

func (h *Handler) PushLogs(ctx context.Context, req *connect.Request[workerv1.PushLogsRequest]) (*connect.Response[workerv1.PushLogsResponse], error) {
	w, err := h.auth(ctx, req.Header())
	if err != nil {
		return nil, err
	}
	recs := make([]usecase.LogRecord, 0, len(req.Msg.GetRecords()))
	for _, r := range req.Msg.GetRecords() {
		rec := usecase.LogRecord{
			RunID: r.GetRunId(), WorkID: r.GetWorkId(), Attempt: int(r.GetAttempt()), Seq: r.GetSeq(),
			Level: r.GetLevel(), Message: r.GetMessage(), Stream: r.GetStream(), Time: time.Now().UTC(),
		}
		if r.GetTime() != nil {
			rec.Time = r.GetTime().AsTime()
		}
		if fj := r.GetFieldsJson(); fj != "" && json.Valid([]byte(fj)) {
			rec.Fields = json.RawMessage(fj)
		}
		recs = append(recs, rec)
	}
	n, err := h.svc.PushLogs(ctx, w, recs)
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&workerv1.PushLogsResponse{Accepted: int32(n)}), nil
}

func (h *Handler) FetchBundle(ctx context.Context, req *connect.Request[workerv1.FetchBundleRequest]) (*connect.Response[workerv1.FetchBundleResponse], error) {
	w, err := h.auth(ctx, req.Header())
	if err != nil {
		return nil, err
	}
	rc, err := h.svc.OpenBundle(ctx, w, req.Msg.GetDigest())
	if err != nil {
		return nil, toConnect(err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&workerv1.FetchBundleResponse{Data: data}), nil
}

func (h *Handler) GetSecret(ctx context.Context, req *connect.Request[workerv1.GetSecretRequest]) (*connect.Response[workerv1.GetSecretResponse], error) {
	w, err := h.auth(ctx, req.Header())
	if err != nil {
		return nil, err
	}
	sec, err := h.svc.SecretForTask(ctx, w, req.Msg.GetLeaseId(), req.Msg.GetKey())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&workerv1.GetSecretResponse{
		Key: sec.Key, Value: sec.Value, Sensitive: sec.Sensitive, Type: string(sec.Type),
	}), nil
}
