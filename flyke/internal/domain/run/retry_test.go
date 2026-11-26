package run

import (
	"encoding/json"
	"testing"
)

func raw(s string) json.RawMessage { return json.RawMessage(s) }

// A source run: load(1) ✓, chunk(2) ✓, embed(3) ✕, timer(4) never reached.
var source = []HistoryEvent{
	{ID: 1, Type: EvRunStarted},
	{ID: 2, Type: EvTaskScheduled, Ref: 1, Name: "load", Payload: raw(`[]`)},
	{ID: 3, Type: EvTaskCompleted, Ref: 1, Name: "load", Payload: raw(`"doc"`)},
	{ID: 4, Type: EvTaskScheduled, Ref: 2, Name: "chunk", Payload: raw(`["doc", 2]`)},
	{ID: 5, Type: EvTaskCompleted, Ref: 2, Name: "chunk", Payload: raw(`["a","b"]`)},
	{ID: 6, Type: EvTaskScheduled, Ref: 3, Name: "embed", Payload: raw(`[["a","b"]]`)},
	{ID: 7, Type: EvTaskFailed, Ref: 3, Name: "embed"},
	{ID: 8, Type: EvWaitStarted, Ref: 5, Name: "approved", Payload: raw(`{"svc":"api"}`)},
	{ID: 9, Type: EvEventReceived, Ref: 5, Name: "approved", Payload: raw(`{"user":"al"}`)},
}

func task(ref int, name, args string) Command {
	return Command{Ref: ref, Kind: CmdScheduleTask, Name: name, Payload: raw(args)}
}

func TestReuse(t *testing.T) {
	failed := &RetryPlan{Of: "r1", Mode: RetryFailed}
	fromChunk := &RetryPlan{Of: "r1", Mode: RetryFrom, FromRef: 2}
	all := &RetryPlan{Of: "r1", Mode: RetryAll}
	cases := []struct {
		name string
		plan *RetryPlan
		comp map[int]bool
		cmd  Command
		want bool
	}{
		{"failed mode reuses a success", failed, nil, task(1, "load", `[]`), true},
		{"args compared semantically", failed, nil, task(2, "chunk", ` [ "doc",2 ] `), true},
		{"different args re-run", failed, nil, task(2, "chunk", `["doc", 3]`), false},
		{"different task at ref re-runs", failed, nil, task(2, "split", `["doc", 2]`), false},
		{"a failure is never reused", failed, nil, task(3, "embed", `[["a","b"]]`), false},
		{"unknown ref re-runs", failed, nil, task(9, "load", `[]`), false},
		{"compensated tasks re-run", failed, map[int]bool{1: true}, task(1, "load", `[]`), false},
		{"from: before the step is reused", fromChunk, nil, task(1, "load", `[]`), true},
		{"from: the step itself re-runs", fromChunk, nil, task(2, "chunk", `["doc", 2]`), false},
		{"all reuses nothing", all, nil, task(1, "load", `[]`), false},
		{"no plan reuses nothing", nil, nil, task(1, "load", `[]`), false},
		{"resolved waits are reused", failed, nil, Command{Ref: 5, Kind: CmdWaitEvent, Name: "approved", Where: map[string]any{"svc": "api"}}, true},
		{"waits with another filter re-run", failed, nil, Command{Ref: 5, Kind: CmdWaitEvent, Name: "approved", Where: map[string]any{"svc": "web"}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			evs, ok := Reuse(source, c.comp, c.plan, c.cmd)
			if ok != c.want {
				t.Fatalf("reuse = %v, want %v", ok, c.want)
			}
			if ok && c.cmd.Kind == CmdScheduleTask {
				if len(evs) != 2 || evs[0].Type != EvTaskScheduled || evs[1].Type != EvTaskCompleted {
					t.Fatalf("unexpected events %+v", evs)
				}
				if c.cmd.Ref == 1 && string(evs[1].Payload) != `"doc"` {
					t.Fatalf("result not carried over: %s", evs[1].Payload)
				}
			}
		})
	}
}

func TestReusedWaitKeepsEventPayload(t *testing.T) {
	evs, ok := Reuse(source, nil, &RetryPlan{Mode: RetryFailed}, Command{Ref: 5, Kind: CmdWaitEvent, Name: "approved", Where: map[string]any{"svc": "api"}})
	if !ok || evs[1].Type != EvEventReceived || string(evs[1].Payload) != `{"user":"al"}` {
		t.Fatalf("got %v %+v", ok, evs)
	}
}
