// Package workflow models registered workflow definitions and their policies.
//
// A Manifest is produced by a language SDK by introspecting user code; the
// control plane never executes user code, it only reasons about manifests.
package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Guarantee is the delivery guarantee of a task.
type Guarantee string

const (
	AtLeastOnce     Guarantee = "at_least_once"
	AtMostOnce      Guarantee = "at_most_once"
	EffectivelyOnce Guarantee = "effectively_once"
)

// OnUnknown is what to do when a task's outcome is unknown (worker lost mid-attempt).
type OnUnknown string

const (
	OnUnknownRetry OnUnknown = "retry"
	OnUnknownPause OnUnknown = "pause"
	OnUnknownFail  OnUnknown = "fail"
)

// ConflictPolicy decides what happens when a concurrency slot is taken.
type ConflictPolicy string

const (
	ConflictQueue         ConflictPolicy = "queue"
	ConflictSkip          ConflictPolicy = "skip"
	ConflictCancelRunning ConflictPolicy = "cancel_running"
)

type Resources struct {
	CPU         float64 `json:"cpu,omitempty"`
	MemoryBytes int64   `json:"memory_bytes,omitempty"`
	// Profile names a resource profile defined on a Pool (e.g. "heavy").
	Profile string `json:"profile,omitempty"`
}

// Fits reports whether r fits into the available resources.
func (r Resources) Fits(available Resources) bool {
	return r.CPU <= available.CPU+1e-9 && r.MemoryBytes <= available.MemoryBytes
}

func (r Resources) Add(o Resources) Resources {
	return Resources{CPU: r.CPU + o.CPU, MemoryBytes: r.MemoryBytes + o.MemoryBytes}
}

func (r Resources) Sub(o Resources) Resources {
	return Resources{CPU: r.CPU - o.CPU, MemoryBytes: r.MemoryBytes - o.MemoryBytes}
}

type Concurrency struct {
	Key        string         `json:"key"`
	Limit      int            `json:"limit"`
	OnConflict ConflictPolicy `json:"on_conflict"`
}

type Trigger struct {
	// "event" | "schedule"
	Type  string         `json:"type"`
	Event string         `json:"event,omitempty"`
	Where map[string]any `json:"where,omitempty"`
	Cron  string         `json:"cron,omitempty"`
	TZ    string         `json:"tz,omitempty"`
	// Name names the schedule a schedule trigger creates (default: derived
	// from the workflow name). Missed is its missed-tick policy.
	Name   string `json:"name,omitempty"`
	Missed string `json:"missed,omitempty"`
	// Inputs maps workflow inputs to values; strings may use "{event.field}" templates.
	Inputs map[string]any `json:"inputs,omitempty"`
}

type TaskSpec struct {
	Retry          RetryPolicy `json:"retry"`
	Guarantee      Guarantee   `json:"guarantee"`
	OnUnknown      OnUnknown   `json:"on_unknown"`
	TimeoutMs      int64       `json:"timeout_ms,omitempty"`
	Resources      *Resources  `json:"resources,omitempty"`
	Pool           string      `json:"pool,omitempty"`
	IdempotencyKey string      `json:"idempotency_key,omitempty"`
	// Compensation is the name of the compensating function, if any.
	Compensation string `json:"compensation,omitempty"`
	// Params names the task's arguments, in call order (for display).
	Params []string `json:"params,omitempty"`
}

func (t TaskSpec) Timeout() time.Duration { return time.Duration(t.TimeoutMs) * time.Millisecond }

type Manifest struct {
	Name         string              `json:"name"`
	Description  string              `json:"description,omitempty"`
	Entrypoint   string              `json:"entrypoint"`
	Runtime      string              `json:"runtime"`
	InputsSchema json.RawMessage     `json:"inputs_schema"`
	Triggers     []Trigger           `json:"triggers,omitempty"`
	Concurrency  *Concurrency        `json:"concurrency,omitempty"`
	Resources    *Resources          `json:"resources,omitempty"`
	Pool         string              `json:"pool,omitempty"`
	TimeoutMs    int64               `json:"timeout_ms,omitempty"`
	Labels       map[string]string   `json:"labels,omitempty"`
	Tasks        map[string]TaskSpec `json:"tasks"`
	// Compensations maps a compensation function name to its policy.
	Compensations map[string]TaskSpec `json:"compensations,omitempty"`
	// Graph is the SDK's static view of the workflow body (nodes and
	// edges), used only for display. The control plane does not interpret it.
	Graph json.RawMessage `json:"graph,omitempty"`
}

func (m Manifest) Timeout() time.Duration { return time.Duration(m.TimeoutMs) * time.Millisecond }

// Task returns the policy of a task or compensation by name.
func (m Manifest) Task(name string) (TaskSpec, bool) {
	if t, ok := m.Tasks[name]; ok {
		return t, true
	}
	t, ok := m.Compensations[name]
	return t, ok
}

var ErrInvalidManifest = errors.New("invalid manifest")

// Validate checks structural invariants and fills defaults.
func (m *Manifest) Validate() error {
	if m.Name == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidManifest)
	}
	if m.Entrypoint == "" {
		return fmt.Errorf("%w: entrypoint is required", ErrInvalidManifest)
	}
	if len(m.InputsSchema) == 0 {
		m.InputsSchema = json.RawMessage(`{"type":"object"}`)
	}
	if m.Concurrency != nil {
		if m.Concurrency.Limit <= 0 {
			m.Concurrency.Limit = 1
		}
		switch m.Concurrency.OnConflict {
		case "":
			m.Concurrency.OnConflict = ConflictQueue
		case ConflictQueue, ConflictSkip, ConflictCancelRunning:
		default:
			return fmt.Errorf("%w: unknown on_conflict %q", ErrInvalidManifest, m.Concurrency.OnConflict)
		}
	}
	for _, tr := range m.Triggers {
		switch tr.Type {
		case "event":
			if tr.Event == "" {
				return fmt.Errorf("%w: event trigger needs an event name", ErrInvalidManifest)
			}
		case "schedule":
			if tr.Cron == "" {
				return fmt.Errorf("%w: schedule trigger needs a cron expression", ErrInvalidManifest)
			}
		default:
			return fmt.Errorf("%w: unknown trigger type %q", ErrInvalidManifest, tr.Type)
		}
	}
	if m.Tasks == nil {
		m.Tasks = map[string]TaskSpec{}
	}
	normalize := func(kind string, specs map[string]TaskSpec) error {
		for name, t := range specs {
			if err := t.normalize(); err != nil {
				return fmt.Errorf("%w: %s %s: %v", ErrInvalidManifest, kind, name, err)
			}
			specs[name] = t
		}
		return nil
	}
	if err := normalize("task", m.Tasks); err != nil {
		return err
	}
	if err := normalize("compensation", m.Compensations); err != nil {
		return err
	}
	for name, t := range m.Tasks {
		if t.Compensation != "" {
			if _, ok := m.Compensations[t.Compensation]; !ok {
				return fmt.Errorf("%w: task %s references unknown compensation %s", ErrInvalidManifest, name, t.Compensation)
			}
		}
	}
	return nil
}

func (t *TaskSpec) normalize() error {
	switch t.Guarantee {
	case "":
		t.Guarantee = AtLeastOnce
	case AtLeastOnce, AtMostOnce, EffectivelyOnce:
	default:
		return fmt.Errorf("unknown guarantee %q", t.Guarantee)
	}
	switch t.OnUnknown {
	case "":
		if t.Guarantee == AtMostOnce {
			t.OnUnknown = OnUnknownFail
		} else {
			t.OnUnknown = OnUnknownRetry
		}
	case OnUnknownRetry, OnUnknownPause, OnUnknownFail:
	default:
		return fmt.Errorf("unknown on_unknown %q", t.OnUnknown)
	}
	if t.Guarantee == AtMostOnce && t.OnUnknown == OnUnknownRetry {
		return errors.New("at_most_once tasks cannot use on_unknown=retry")
	}
	return t.Retry.normalize()
}

// Source locates the code of a workflow version.
type Source struct {
	Entrypoint   string `json:"entrypoint"`
	LocalRoot    string `json:"local_root,omitempty"`
	BundleDigest string `json:"bundle_digest,omitempty"`
}

// Workflow is an immutable, registered version of a workflow definition.
type Workflow struct {
	Namespace string
	Name      string
	Version   string
	Manifest  Manifest
	Source    Source
	// CreatedAt is when this version was first registered.
	CreatedAt time.Time
	// ActivatedAt is when this version last became the current one (by a
	// deploy of the same content, or an explicit activation/rollback).
	ActivatedAt time.Time
}

// ComputeVersion derives a content-addressed version from manifest and source.
func ComputeVersion(m Manifest, s Source) string {
	h := sha256.New()
	mb, _ := json.Marshal(m)
	sb, _ := json.Marshal(s)
	h.Write(mb)
	h.Write(sb)
	return "v-" + hex.EncodeToString(h.Sum(nil))[:12]
}
