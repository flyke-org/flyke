package run

import (
	"encoding/json"
	"time"

	"github.com/flyke-org/flyke/flyke/internal/domain/workflow"
)

type EventType string

const (
	EvRunStarted      EventType = "RunStarted"
	EvTaskScheduled   EventType = "TaskScheduled"
	EvTaskCompleted   EventType = "TaskCompleted"
	EvTaskFailed      EventType = "TaskFailed"
	EvTimerStarted    EventType = "TimerStarted"
	EvTimerFired      EventType = "TimerFired"
	EvWaitStarted     EventType = "WaitStarted"
	EvEventReceived   EventType = "EventReceived"
	EvWaitTimedOut    EventType = "WaitTimedOut"
	EvEventEmitted    EventType = "EventEmitted"
	EvRunCompleted    EventType = "RunCompleted"
	EvRunFailed       EventType = "RunFailed"
	EvCancelRequested EventType = "CancelRequested"
)

// HistoryEvent is one entry of a run's append-only log.
// ID is assigned by the store (1-based, gapless per run).
type HistoryEvent struct {
	ID      int
	Type    EventType
	Time    time.Time
	Ref     int
	Name    string
	Payload json.RawMessage
	Failure *workflow.Failure
}

// CommandKind enumerates what a workflow step can ask for.
type CommandKind string

const (
	CmdScheduleTask CommandKind = "ScheduleTask"
	CmdStartTimer   CommandKind = "StartTimer"
	CmdWaitEvent    CommandKind = "WaitEvent"
	CmdEmitEvent    CommandKind = "EmitEvent"
	CmdCompleteRun  CommandKind = "CompleteRun"
	CmdFailRun      CommandKind = "FailRun"
)

// Command is produced by a workflow step on a worker.
type Command struct {
	Ref            int
	Kind           CommandKind
	Name           string
	Payload        json.RawMessage // task args, event payload, run result
	Where          map[string]any
	Duration       time.Duration // timer duration / wait timeout (0 = forever)
	IdempotencyKey string
	Failure        *workflow.Failure
}

// Wait is a durable timer or event subscription owned by a run.
type Wait struct {
	RunID     string
	Namespace string
	Ref       int
	// "timer" | "event"
	Kind     string
	Name     string
	Where    map[string]any
	Deadline *time.Time
	Done     bool
}
