// Package v1 holds the wire types of the Flyke REST API (apiVersion flyke.io/v1).
//
// Resources follow the Kubernetes shape: metadata / spec / status. These are
// pure data-transfer objects shared by the control plane, the CLI and any Go
// client; they carry no behaviour.
package v1

import (
	"encoding/json"
	"time"
)

const APIVersion = "flyke.io/v1"

// ObjectMeta is the metadata common to every resource.
type ObjectMeta struct {
	Name            string            `json:"name"`
	Namespace       string            `json:"namespace,omitempty"`
	UID             string            `json:"uid,omitempty"`
	Labels          map[string]string `json:"labels,omitempty"`
	ResourceVersion string            `json:"resourceVersion,omitempty"`
	CreatedAt       time.Time         `json:"createdAt"`
}

// TypeMeta identifies the kind of a resource.
type TypeMeta struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
}

// List is a generic collection response.
type List[T any] struct {
	TypeMeta
	Items []T `json:"items"`
}

// Status is the error body returned for every non-2xx response.
type Status struct {
	TypeMeta
	Code    int    `json:"code"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

type Failure struct {
	Type         string   `json:"type"`
	MRO          []string `json:"mro,omitempty"`
	Message      string   `json:"message"`
	Stack        string   `json:"stack,omitempty"`
	NonRetryable bool     `json:"nonRetryable,omitempty"`
}

// ------------------------------------------------------------- workflows ----

type Source struct {
	// "<relative/path.py>:<ClassName>"
	Entrypoint   string `json:"entrypoint"`
	LocalRoot    string `json:"localRoot,omitempty"`
	BundleDigest string `json:"bundleDigest,omitempty"`
}

type WorkflowSpec struct {
	// Manifest produced by the SDK (`python -m flyke manifest`).
	Manifest json.RawMessage `json:"manifest"`
	Source   Source          `json:"source"`
}

type WorkflowStatus struct {
	// Version is the current version: the one new runs use.
	Version string `json:"version"`
	// ActivatedAt is when that version became current.
	ActivatedAt *time.Time `json:"activatedAt,omitempty"`
}

type Workflow struct {
	TypeMeta
	Metadata ObjectMeta     `json:"metadata"`
	Spec     WorkflowSpec   `json:"spec"`
	Status   WorkflowStatus `json:"status"`
}

// WorkflowVersion is one immutable, registered version of a workflow.
// metadata.name is the workflow; metadata.createdAt is when the version was
// first registered.
type WorkflowVersion struct {
	TypeMeta
	Metadata ObjectMeta `json:"metadata"`
	Version  string     `json:"version"`
	// Current is true for the version new runs, triggers and schedules use.
	Current bool `json:"current"`
	// ActivatedAt is when the version last became current.
	ActivatedAt time.Time       `json:"activatedAt"`
	Entrypoint  string          `json:"entrypoint"`
	Runtime     string          `json:"runtime"`
	Source      Source          `json:"source"`
	Bundle      *BundleInfo     `json:"bundle,omitempty"`
	Tasks       []string        `json:"tasks"`
	Manifest    json.RawMessage `json:"manifest"`
	Runs        VersionRuns     `json:"runs"`
}

type BundleInfo struct {
	Digest     string     `json:"digest"`
	Size       int64      `json:"size,omitempty"`
	UploadedAt *time.Time `json:"uploadedAt,omitempty"`
}

// VersionRuns summarizes the runs that executed a version.
type VersionRuns struct {
	Total      int            `json:"total"`
	Phases     map[string]int `json:"phases"`
	FirstRunAt *time.Time     `json:"firstRunAt,omitempty"`
	LastRunAt  *time.Time     `json:"lastRunAt,omitempty"`
}

type BundleUploadResponse struct {
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

// ------------------------------------------------------------------ runs ----

type StartRunRequest struct {
	// Version runs a specific registered version instead of the current one.
	Version        string            `json:"version,omitempty"`
	Inputs         json.RawMessage   `json:"inputs,omitempty"`
	Labels         map[string]string `json:"labels,omitempty"`
	IdempotencyKey string            `json:"idempotencyKey,omitempty"`
}

type RunSpec struct {
	Workflow    string          `json:"workflow"`
	Version     string          `json:"version"`
	Inputs      json.RawMessage `json:"inputs"`
	TriggeredBy string          `json:"triggeredBy,omitempty"`
	// Retry is set when this run retries another one.
	Retry *RetrySpec `json:"retry,omitempty"`
}

// RetrySpec says which run a run retries and which outcomes it reuses.
type RetrySpec struct {
	Of string `json:"of"`
	// "all" | "failed" | "from"
	Mode     string `json:"mode"`
	FromRef  int    `json:"fromRef,omitempty"`
	FromTask string `json:"fromTask,omitempty"`
}

// RetryRunRequest starts a new run from a finished one.
//
//   - mode "failed" (default): reuse every successful task, re-run the rest.
//   - mode "from": reuse what ran before fromRef (or the first call of
//     fromTask); that step and everything after it run again.
//   - mode "all": run everything again with the same inputs.
//
// A recorded result is reused only when the new run calls the same task at
// the same position with the same arguments, so retrying after changing
// code or inputs re-runs whatever changed.
type RetryRunRequest struct {
	Mode     string `json:"mode,omitempty"`
	FromRef  int    `json:"fromRef,omitempty"`
	FromTask string `json:"fromTask,omitempty"`
	// Inputs replaces the source run's inputs when set.
	Inputs json.RawMessage `json:"inputs,omitempty"`
	// Version is the code to retry with: "" (default) the current version,
	// "same" the source run's version, or an explicit version.
	Version string `json:"version,omitempty"`
}

type RunStatus struct {
	Phase          string          `json:"phase"`
	Reason         string          `json:"reason,omitempty"`
	Message        string          `json:"message,omitempty"`
	ConcurrencyKey string          `json:"concurrencyKey,omitempty"`
	Compensation   string          `json:"compensation,omitempty"`
	Result         json.RawMessage `json:"result,omitempty"`
	Failure        *Failure        `json:"failure,omitempty"`
	StartedAt      *time.Time      `json:"startedAt,omitempty"`
	FinishedAt     *time.Time      `json:"finishedAt,omitempty"`
	HistoryLength  int             `json:"historyLength"`
}

type Run struct {
	TypeMeta
	Metadata ObjectMeta `json:"metadata"`
	Spec     RunSpec    `json:"spec"`
	Status   RunStatus  `json:"status"`
}

// Terminal reports whether a run phase will never change again.
func Terminal(phase string) bool {
	switch phase {
	case "Succeeded", "Failed", "Cancelled", "TimedOut", "Skipped":
		return true
	}
	return false
}

type TaskRun struct {
	ID             string          `json:"id"`
	Kind           string          `json:"kind"`
	Ref            int             `json:"ref"`
	Name           string          `json:"name"`
	Phase          string          `json:"phase"`
	Attempt        int             `json:"attempt"`
	Worker         string          `json:"worker,omitempty"`
	IdempotencyKey string          `json:"idempotencyKey,omitempty"`
	Args           json.RawMessage `json:"args,omitempty"`
	Result         json.RawMessage `json:"result,omitempty"`
	Failure        *Failure        `json:"failure,omitempty"`
	CreatedAt      time.Time       `json:"createdAt"`
	StartedAt      *time.Time      `json:"startedAt,omitempty"`
	FinishedAt     *time.Time      `json:"finishedAt,omitempty"`
	// ReusedFrom is the run whose recorded result this task reused.
	ReusedFrom string `json:"reusedFrom,omitempty"`
}

type HistoryEvent struct {
	ID      int             `json:"id"`
	Type    string          `json:"type"`
	Time    time.Time       `json:"time"`
	Ref     int             `json:"ref,omitempty"`
	Name    string          `json:"name,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Failure *Failure        `json:"failure,omitempty"`
}

// ResolveTaskRequest settles a task whose outcome is unknown (AT_MOST_ONCE).
type ResolveTaskRequest struct {
	// "succeeded" | "failed" | "retry"
	Outcome string          `json:"outcome"`
	Result  json.RawMessage `json:"result,omitempty"`
	Message string          `json:"message,omitempty"`
}

// ------------------------------------------------------------------ logs ----

type LogEntry struct {
	ID    int64     `json:"id"`
	RunID string    `json:"runId"`
	Time  time.Time `json:"time"`
	Level string    `json:"level"`
	// "worker" (user code), "workflow" (workflow body) or "system" (control plane).
	Source  string          `json:"source"`
	Stream  string          `json:"stream,omitempty"`
	Message string          `json:"message"`
	Fields  json.RawMessage `json:"fields,omitempty"`
	Task    string          `json:"task,omitempty"`
	Ref     int             `json:"ref,omitempty"`
	Attempt int             `json:"attempt,omitempty"`
	Worker  string          `json:"worker,omitempty"`
}

// ---------------------------------------------------------------- events ----

type EmitEventRequest struct {
	Name    string          `json:"name"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

type Event struct {
	TypeMeta
	Metadata ObjectMeta      `json:"metadata"`
	Name     string          `json:"name"`
	Payload  json.RawMessage `json:"payload"`
	// Runs resumed or started by this event.
	Matched []string `json:"matched,omitempty"`
}

// --------------------------------------------------------------- workers ----

type Resources struct {
	CPU         float64 `json:"cpu,omitempty"`
	MemoryBytes int64   `json:"memoryBytes,omitempty"`
}

type WorkerSpec struct {
	Unschedulable bool `json:"unschedulable"`
	Draining      bool `json:"draining,omitempty"`
}

type WorkerStatus struct {
	Phase         string     `json:"phase"`
	Hostname      string     `json:"hostname"`
	Pool          string     `json:"pool"`
	Runtimes      []string   `json:"runtimes"`
	Slots         int        `json:"slots"`
	FreeSlots     int        `json:"freeSlots"`
	Capacity      Resources  `json:"capacity"`
	ActiveTasks   int        `json:"activeTasks"`
	AgentVersion  string     `json:"agentVersion,omitempty"`
	LastHeartbeat *time.Time `json:"lastHeartbeat,omitempty"`
}

type Worker struct {
	TypeMeta
	Metadata ObjectMeta   `json:"metadata"`
	Spec     WorkerSpec   `json:"spec"`
	Status   WorkerStatus `json:"status"`
}

// ----------------------------------------------------------------- pools ----

type PoolSpec struct {
	// Workers whose labels match all entries belong to the pool.
	// Defaults to {"flyke.io/pool": <name>}.
	Selector           map[string]string    `json:"selector,omitempty"`
	MaxConcurrentTasks int                  `json:"maxConcurrentTasks,omitempty"`
	ResourceProfiles   map[string]Resources `json:"resourceProfiles,omitempty"`
}

type PoolStatus struct {
	Workers      int `json:"workers"`
	ReadyWorkers int `json:"readyWorkers"`
	FreeSlots    int `json:"freeSlots"`
}

type Pool struct {
	TypeMeta
	Metadata ObjectMeta `json:"metadata"`
	Spec     PoolSpec   `json:"spec"`
	Status   PoolStatus `json:"status"`
}

type CreateJoinTokenRequest struct {
	TTLSeconds    int               `json:"ttlSeconds,omitempty"`
	MaxUses       int               `json:"maxUses,omitempty"`
	AllowedLabels map[string]string `json:"allowedLabels,omitempty"`
}

type JoinToken struct {
	ID        string    `json:"id"`
	Pool      string    `json:"pool"`
	ExpiresAt time.Time `json:"expiresAt"`
	MaxUses   int       `json:"maxUses"`
	Uses      int       `json:"uses"`
	// Only returned once, at creation.
	Token string `json:"token,omitempty"`
}

// Secret is a key/value secret.
//
// Type "Private" secrets belong to one namespace; "Shared" secrets (empty
// namespace) are visible from every namespace, and a Private secret with the
// same key overrides them.
//
// Sensitive secrets are encrypted at rest and Value is never returned for
// them; non-sensitive secrets are plain variables whose Value is returned.
type Secret struct {
	ID        string    `json:"id"`
	Namespace string    `json:"namespace,omitempty"`
	Key       string    `json:"key"`
	Value     string    `json:"value,omitempty"`
	Sensitive bool      `json:"sensitive"`
	Type      string    `json:"type"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// SecretSpec is the body of POST .../secrets and PUT .../secrets/{key}.
//
// Type is "Private" (default under /namespaces/{ns}) or "Shared". On PUT, an
// omitted value or sensitive keeps the stored one.
type SecretSpec struct {
	Key       string  `json:"key,omitempty"`
	Value     *string `json:"value,omitempty"`
	Sensitive *bool   `json:"sensitive,omitempty"`
	Type      string  `json:"type,omitempty"`
}

// ------------------------------------------------------------ playground ----

// PlaygroundInfo says whether the UI playground is available. It is only on
// dev control planes (`flyke server --dev`), because saving a project runs
// the SDK on the control plane's machine.
type PlaygroundInfo struct {
	TypeMeta
	Enabled bool   `json:"enabled"`
	Reason  string `json:"reason,omitempty"`
}

// PlaygroundProjectSpec is a project's code: relative path → file content.
type PlaygroundProjectSpec struct {
	Files map[string]string `json:"files"`
}

type PlaygroundProject struct {
	TypeMeta
	Name      string            `json:"name"`
	Files     map[string]string `json:"files"`
	UpdatedAt *time.Time        `json:"updatedAt,omitempty"`
	// Discovery is set by GET and PUT: the workflows found in the code.
	Discovery *PlaygroundDiscovery `json:"discovery,omitempty"`
}

type PlaygroundDiscovery struct {
	Workflows []PlaygroundWorkflow `json:"workflows"`
	Errors    []PlaygroundError    `json:"errors"`
}

type PlaygroundWorkflow struct {
	Entrypoint string          `json:"entrypoint"`
	Manifest   json.RawMessage `json:"manifest"`
}

// PlaygroundError is a file that could not be imported.
type PlaygroundError struct {
	File  string `json:"file"`
	Line  int    `json:"line,omitempty"`
	Error string `json:"error"`
}

// PlaygroundRunRequest registers the project's workflow at Entrypoint (from
// the code as saved) and starts a run of it.
type PlaygroundRunRequest struct {
	Entrypoint string          `json:"entrypoint"`
	Inputs     json.RawMessage `json:"inputs,omitempty"`
}

// ------------------------------------------------------------ namespaces ----
//
// A namespace is a tenant. The admin (the control plane's admin token)
// manages all of them; an access token only works inside its own namespace.

type NamespaceSpec struct {
	Description string `json:"description,omitempty"`
	// SharedWorkers lets the namespace's work run on shared pools (pools the
	// admin created outside any namespace). Off by default: the tenant's
	// code then only runs on its own workers.
	SharedWorkers bool `json:"sharedWorkers"`
}

type NamespaceStatus struct {
	Workflows  int `json:"workflows"`
	ActiveRuns int `json:"activeRuns"`
	Workers    int `json:"workers"`
	Tokens     int `json:"tokens"`
}

type Namespace struct {
	TypeMeta
	Metadata ObjectMeta      `json:"metadata"`
	Spec     NamespaceSpec   `json:"spec"`
	Status   NamespaceStatus `json:"status"`
}

type CreateNamespaceRequest struct {
	Name string `json:"name"`
	NamespaceSpec
}

type CreateAccessTokenRequest struct {
	Name string `json:"name"`
	// TTLSeconds 0 = the token never expires.
	TTLSeconds int `json:"ttlSeconds,omitempty"`
}

// AccessToken authenticates API calls for one namespace
// ("Authorization: Bearer flyke_at_…").
type AccessToken struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Namespace string     `json:"namespace"`
	CreatedAt time.Time  `json:"createdAt"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	// Only returned once, at creation.
	Token string `json:"token,omitempty"`
}

// WhoAmI describes the caller's credential.
type WhoAmI struct {
	TypeMeta
	// Admin callers can use every namespace and the cluster-wide routes.
	Admin bool `json:"admin"`
	// Namespace is the only namespace a non-admin caller can use.
	Namespace string `json:"namespace,omitempty"`
	// Token is the name of the access token used, if any.
	Token string `json:"token,omitempty"`
	// AuthRequired is false when the control plane has no admin token: the
	// API is then open and every caller is an admin (development).
	AuthRequired bool `json:"authRequired"`
}

// -------------------------------------------------------------- schedules ----

// ScheduleSpec says which workflow to run and when.
type ScheduleSpec struct {
	Workflow string `json:"workflow"`
	// Cron is a 5-field cron expression ("0 3 * * *"), a shortcut (@hourly,
	// @daily, @weekly, @monthly) or an interval ("@every 15m").
	Cron string `json:"cron"`
	// TZ is the IANA time zone Cron is read in (default UTC).
	TZ string `json:"tz,omitempty"`
	// Inputs are the workflow inputs of each run. Strings may use the
	// templates {schedule.time}, {schedule.date} and {schedule.name}.
	Inputs map[string]any `json:"inputs,omitempty"`
	// Missed is what to do with a tick that could not fire on time:
	// "run_once" (default) starts one run as soon as possible, "skip" waits
	// for the next tick.
	Missed string `json:"missed,omitempty"`
	Paused bool   `json:"paused,omitempty"`
}

type ScheduleStatus struct {
	// Description is Cron in plain English.
	Description string `json:"description"`
	// Declared is true for a schedule that comes from workflow code
	// (on.schedule): it is changed by deploying, not through the API.
	Declared  bool       `json:"declared,omitempty"`
	NextRunAt *time.Time `json:"nextRunAt,omitempty"`
	// Upcoming lists the next ticks (single-schedule responses only).
	Upcoming     []time.Time `json:"upcoming,omitempty"`
	LastRunAt    *time.Time  `json:"lastRunAt,omitempty"`
	LastRun      string      `json:"lastRun,omitempty"`
	LastRunPhase string      `json:"lastRunPhase,omitempty"`
	// LastError is why the last tick could not start a run.
	LastError string `json:"lastError,omitempty"`
}

type Schedule struct {
	TypeMeta
	Metadata ObjectMeta     `json:"metadata"`
	Spec     ScheduleSpec   `json:"spec"`
	Status   ScheduleStatus `json:"status"`
}

// SchedulePreviewRequest asks what a cron expression means before saving it.
type SchedulePreviewRequest struct {
	Cron string `json:"cron"`
	TZ   string `json:"tz,omitempty"`
}

type SchedulePreview struct {
	Description string      `json:"description"`
	Upcoming    []time.Time `json:"upcoming"`
}
