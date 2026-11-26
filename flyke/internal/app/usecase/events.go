package usecase

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flyke-org/flyke/flyke/internal/app/ports"
	"github.com/flyke-org/flyke/flyke/internal/domain/event"
	"github.com/flyke-org/flyke/flyke/internal/domain/run"
	"github.com/flyke-org/flyke/flyke/internal/domain/workflow"
)

// PostEvent publishes an event: it wakes matching waiting runs and starts
// runs of workflows with matching event triggers. Returns affected run ids.
func (s *Service) PostEvent(ctx context.Context, ns, name string, payload json.RawMessage) (event.Event, []string, error) {
	if name == "" {
		return event.Event{}, nil, invalid("event name is required")
	}
	var ev event.Event
	var matched []string
	err := s.tx(ctx, func(t *txn) error {
		var err error
		ev, matched, err = t.postEvent(ns, name, payload, "api")
		return err
	})
	return ev, matched, err
}

func (t *txn) postEvent(ns, name string, payload json.RawMessage, source string) (event.Event, []string, error) {
	if len(payload) == 0 {
		payload = json.RawMessage("{}")
	}
	ev := event.Event{ID: t.s.ids.NewID("ev"), Namespace: ns, Name: name, Payload: payload, Source: source, CreatedAt: t.now}
	fields := map[string]any{}
	if err := json.Unmarshal(payload, &fields); err != nil {
		return ev, nil, invalid("event payload must be a JSON object")
	}
	if err := t.Events().Create(t.ctx, ev); err != nil {
		return ev, nil, err
	}
	var matched []string

	// 1. Resume runs waiting for this event (each wait consumes one event).
	waits, err := t.Waits().ListOpenEvent(t.ctx, ns, name)
	if err != nil {
		return ev, nil, err
	}
	for _, w := range waits {
		if !workflow.MatchWhere(w.Where, fields) {
			continue
		}
		r, err := t.Runs().GetByID(t.ctx, w.RunID)
		if err != nil || (r.Phase != run.Running && r.Phase != run.Waiting) {
			continue
		}
		if err := t.Waits().MarkDone(t.ctx, w.RunID, w.Ref); err != nil {
			return ev, nil, err
		}
		if err := t.appendHistory(r, run.HistoryEvent{Type: run.EvEventReceived, Ref: w.Ref, Name: name, Payload: payload}); err != nil {
			return ev, nil, err
		}
		t.syslog(r, "info", "event "+name+" received", fields, nil)
		r.Wake()
		if err := t.saveRun(r); err != nil {
			return ev, nil, err
		}
		matched = append(matched, r.ID)
	}

	// 2. Start runs from event triggers.
	wfs, err := t.Workflows().ListLatest(t.ctx, ns)
	if err != nil {
		return ev, nil, err
	}
	vars := map[string]any{"event": fields}
	for _, wf := range wfs {
		for i, tr := range wf.Manifest.Triggers {
			if tr.Type != "event" || tr.Event != name || !workflow.MatchWhere(tr.Where, fields) {
				continue
			}
			inputs := map[string]any{}
			if len(tr.Inputs) == 0 {
				// Default mapping: event fields that are workflow inputs.
				inputs = fields
			}
			for k, v := range tr.Inputs {
				rv, err := workflow.RenderValue(v, vars)
				if err != nil {
					return ev, nil, invalid("trigger %d of %s: %v", i, wf.Name, err)
				}
				inputs[k] = rv
			}
			raw, _ := json.Marshal(filterToSchema(wf, inputs, len(tr.Inputs) == 0))
			r, err := t.startRun(wf, StartRunInput{
				Inputs:         raw,
				IdempotencyKey: fmt.Sprintf("event:%s:%d", ev.ID, i),
				TriggeredBy:    "event:" + name,
			})
			if err != nil {
				t.s.log.Warn("event trigger could not start run", "workflow", wf.Name, "event", name, "err", err)
				continue
			}
			matched = append(matched, r.ID)
		}
	}
	t.notify(ports.TopicReconcile)
	return ev, matched, nil
}

// filterToSchema keeps only declared inputs when mapping event fields by name.
func filterToSchema(wf workflow.Workflow, in map[string]any, filter bool) map[string]any {
	if !filter {
		return in
	}
	var shape struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	_ = json.Unmarshal(wf.Manifest.InputsSchema, &shape)
	out := map[string]any{}
	for k, v := range in {
		if _, ok := shape.Properties[k]; ok {
			out[k] = v
		}
	}
	return out
}

func (s *Service) ListEvents(ctx context.Context, ns, name string, limit int) (out []event.Event, err error) {
	err = s.read(ctx, func(r ports.Repos) error {
		out, err = r.Events().List(ctx, ns, name, limit)
		return err
	})
	return
}

// renderInputs renders trigger input templates against vars.
func renderInputs(in map[string]any, vars map[string]any) (json.RawMessage, error) {
	out := map[string]any{}
	for k, v := range in {
		rv, err := workflow.RenderValue(v, vars)
		if err != nil {
			return nil, err
		}
		out[k] = rv
	}
	return json.Marshal(out)
}
