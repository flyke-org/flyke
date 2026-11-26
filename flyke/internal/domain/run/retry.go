package run

import (
	"bytes"
	"encoding/json"
	"reflect"
)

// RetryMode selects which recorded outcomes of a source run a retry reuses.
type RetryMode string

const (
	// RetryAll starts from scratch with the same inputs: nothing is reused.
	RetryAll RetryMode = "all"
	// RetryFailed reuses every successful outcome; failed, cancelled and
	// never-reached work runs again.
	RetryFailed RetryMode = "failed"
	// RetryFrom reuses the outcomes recorded before FromRef; the step at
	// FromRef and everything after it runs again.
	RetryFrom RetryMode = "from"
)

func (m RetryMode) Valid() bool { return m == RetryAll || m == RetryFailed || m == RetryFrom }

// RetryPlan links a run to the run it retries and says what it may reuse.
type RetryPlan struct {
	Of      string    `json:"of"`
	Mode    RetryMode `json:"mode"`
	FromRef int       `json:"from_ref,omitempty"`
	// FromName is the name of the step at FromRef (for display).
	FromName string `json:"from_name,omitempty"`
}

// Covers reports whether the plan allows reusing the outcome at ref.
func (p *RetryPlan) Covers(ref int) bool {
	if p == nil {
		return false
	}
	switch p.Mode {
	case RetryFailed:
		return true
	case RetryFrom:
		return ref < p.FromRef
	}
	return false
}

// Reuse returns the history events that replay a source run's recorded
// outcome for command c, or ok=false when c must really execute.
//
// Reuse is deliberately strict so that a retry after a code or input change
// stays correct: the source must have issued the same kind of command, with
// the same name and the same arguments, at the same ref, and that command
// must have succeeded (tasks), fired (timers), resolved (waits) or been
// emitted (events). Tasks that were compensated in the source run are never
// reused, because their side effects were undone.
func Reuse(src []HistoryEvent, compensated map[int]bool, plan *RetryPlan, c Command) (evs []HistoryEvent, ok bool) {
	if !plan.Covers(c.Ref) {
		return nil, false
	}
	var started *HistoryEvent
	for i := range src {
		ev := &src[i]
		if ev.Ref != c.Ref {
			continue
		}
		switch ev.Type {
		case EvTaskScheduled, EvTimerStarted, EvWaitStarted, EvEventEmitted:
			started = ev
		}
		if started != nil {
			break
		}
	}
	if started == nil || started.Name != c.Name {
		return nil, false
	}
	outcome := func(types ...EventType) *HistoryEvent {
		for i := range src {
			if src[i].Ref != c.Ref {
				continue
			}
			for _, t := range types {
				if src[i].Type == t {
					return &src[i]
				}
			}
		}
		return nil
	}
	switch c.Kind {
	case CmdScheduleTask:
		if started.Type != EvTaskScheduled || compensated[c.Ref] || !SameJSON(started.Payload, c.Payload) {
			return nil, false
		}
		done := outcome(EvTaskCompleted)
		if done == nil {
			return nil, false
		}
		return []HistoryEvent{
			{Type: EvTaskScheduled, Ref: c.Ref, Name: c.Name, Payload: c.Payload},
			{Type: EvTaskCompleted, Ref: c.Ref, Name: c.Name, Payload: done.Payload},
		}, true
	case CmdStartTimer:
		if started.Type != EvTimerStarted || outcome(EvTimerFired) == nil {
			return nil, false
		}
		return []HistoryEvent{
			{Type: EvTimerStarted, Ref: c.Ref, Name: c.Name},
			{Type: EvTimerFired, Ref: c.Ref, Name: c.Name},
		}, true
	case CmdWaitEvent:
		where, _ := json.Marshal(c.Where)
		if started.Type != EvWaitStarted || !SameJSON(started.Payload, where) {
			return nil, false
		}
		done := outcome(EvEventReceived, EvWaitTimedOut)
		if done == nil {
			return nil, false
		}
		return []HistoryEvent{
			{Type: EvWaitStarted, Ref: c.Ref, Name: c.Name, Payload: where},
			{Type: done.Type, Ref: c.Ref, Name: done.Name, Payload: done.Payload},
		}, true
	case CmdEmitEvent:
		if started.Type != EvEventEmitted || !SameJSON(started.Payload, c.Payload) {
			return nil, false
		}
		// Recorded but not published again: the source run already emitted it.
		return []HistoryEvent{{Type: EvEventEmitted, Ref: c.Ref, Name: c.Name, Payload: c.Payload}}, true
	}
	return nil, false
}

// SameJSON compares two JSON documents semantically (key order and
// whitespace do not matter; empty means null).
func SameJSON(a, b json.RawMessage) bool {
	norm := func(r json.RawMessage) json.RawMessage {
		if len(bytes.TrimSpace(r)) == 0 {
			return json.RawMessage("null")
		}
		return r
	}
	a, b = norm(a), norm(b)
	if bytes.Equal(a, b) {
		return true
	}
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}
