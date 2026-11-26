// Wire types of the Flyke REST API (flyke.io/v1). Mirrors api/rest/v1/types.go.

export interface ObjectMeta {
  name: string;
  namespace?: string;
  uid?: string;
  labels?: Record<string, string>;
  resourceVersion?: string;
  createdAt: string;
}

export interface List<T> {
  items: T[];
}

export interface Failure {
  type: string;
  mro?: string[];
  message: string;
  stack?: string;
  nonRetryable?: boolean;
}

export type RunPhase =
  | "Pending" | "Queued" | "Running" | "Waiting" | "Paused" | "Compensating"
  | "Succeeded" | "Failed" | "Cancelled" | "TimedOut" | "Skipped";

export const TERMINAL: RunPhase[] = ["Succeeded", "Failed", "Cancelled", "TimedOut", "Skipped"];

export interface Run {
  metadata: ObjectMeta;
  spec: { workflow: string; version: string; inputs: unknown; triggeredBy?: string; retry?: RetrySpec };
  status: {
    phase: RunPhase;
    reason?: string;
    message?: string;
    concurrencyKey?: string;
    compensation?: string;
    result?: unknown;
    failure?: Failure;
    startedAt?: string;
    finishedAt?: string;
    historyLength: number;
  };
}

export type RetryMode = "all" | "failed" | "from";

export interface RetrySpec {
  of: string;
  mode: RetryMode;
  fromRef?: number;
  fromTask?: string;
}

export interface RetryRequest {
  mode: RetryMode;
  fromRef?: number;
  inputs?: unknown;
  /** "" the current version, "same" the source run's, or an explicit version. */
  version?: string;
}

export interface TaskRun {
  id: string;
  kind: "task" | "compensation" | "step";
  ref: number;
  name: string;
  phase: "Pending" | "Scheduled" | "Running" | "Succeeded" | "Failed" | "Unknown" | "Cancelled";
  attempt: number;
  worker?: string;
  idempotencyKey?: string;
  args?: unknown;
  result?: unknown;
  failure?: Failure;
  createdAt: string;
  startedAt?: string;
  finishedAt?: string;
  reusedFrom?: string;
}

export interface HistoryEvent {
  id: number;
  type: string;
  time: string;
  ref?: number;
  name?: string;
  payload?: unknown;
  failure?: Failure;
}

export interface LogEntry {
  id: number;
  runId: string;
  time: string;
  level: "debug" | "info" | "warning" | "error";
  source: "worker" | "workflow" | "system";
  stream?: string;
  message: string;
  fields?: Record<string, unknown>;
  task?: string;
  ref?: number;
  attempt?: number;
  worker?: string;
}

export interface Source {
  entrypoint: string;
  localRoot?: string;
  bundleDigest?: string;
}

export interface JSONSchema {
  type?: string | string[];
  enum?: unknown[];
  default?: unknown;
  items?: JSONSchema;
  properties?: Record<string, JSONSchema>;
  required?: string[];
  anyOf?: JSONSchema[];
  description?: string;
}

export interface TaskSpec {
  retry?: { attempts: number; backoff: string; delay_ms: number; max_delay_ms?: number; on?: string[] };
  guarantee: "at_least_once" | "at_most_once" | "effectively_once";
  on_unknown?: string;
  timeout_ms?: number;
  resources?: { cpu?: number; memory_bytes?: number; profile?: string };
  pool?: string;
  idempotency_key?: string;
  compensation?: string;
  params?: string[];
}

/** Where a value comes from: a workflow input, or the result of an earlier node. */
export interface GraphSource {
  kind: "input" | "step";
  name?: string;
  node?: string;
  task?: string;
}

export interface GraphArg {
  name: string;
  expr: string;
  sources: GraphSource[];
}

export interface GraphLane {
  label: string | null;
  nodes: GraphNode[];
  fan_out?: boolean;
}

export type GraphNodeKind = "task" | "wait" | "emit" | "sleep" | "parallel" | "branch" | "loop" | "try" | "finally" | "return";

export interface GraphNode {
  id: string;
  kind: GraphNodeKind;
  label: string;
  line?: number;
  task?: string;
  event?: string;
  args?: GraphArg[];
  assign?: string;
  lanes?: GraphLane[];
  sources?: GraphSource[];
  expr?: string | null;
}

export interface TaskSignature {
  params: { name: string; type: string; default?: unknown }[];
  returns: string;
  description?: string;
}

/** Static shape of run(), extracted by the SDK (display only). */
export interface WorkflowGraph {
  tree?: GraphNode[];
  output?: { type: string; expr: string | null; sources: GraphSource[] };
  tasks?: Record<string, TaskSignature>;
  dynamic_tasks?: string[];
  warnings?: string[];
  /** Older SDKs: a flat list of call sites. */
  nodes?: { id: number; kind: "task" | "wait" | "sleep" | "emit"; name: string; line?: number; fanout?: boolean; when?: string; loop?: string }[];
}

export interface Trigger {
  type: "event" | "schedule";
  event?: string;
  where?: Record<string, unknown>;
  cron?: string;
  name?: string;
  missed?: string;
  tz?: string;
  inputs?: Record<string, unknown>;
}

export interface Manifest {
  name: string;
  description?: string;
  entrypoint: string;
  runtime: string;
  inputs_schema: JSONSchema;
  triggers?: Trigger[];
  concurrency?: { key: string; limit: number; on_conflict: string };
  resources?: { cpu?: number; memory_bytes?: number; profile?: string };
  pool?: string;
  timeout_ms?: number;
  labels?: Record<string, string>;
  tasks: Record<string, TaskSpec>;
  compensations?: Record<string, TaskSpec>;
  graph?: WorkflowGraph;
}

export interface Workflow {
  metadata: ObjectMeta;
  spec: { manifest: Manifest; source: Source };
  /** version is the current one; activatedAt is when it became current. */
  status: { version: string; activatedAt?: string };
}

/** One immutable registered version of a workflow (metadata.createdAt = first registered). */
export interface WorkflowVersion {
  metadata: ObjectMeta;
  version: string;
  current: boolean;
  activatedAt: string;
  entrypoint: string;
  runtime: string;
  source: Source;
  bundle?: { digest: string; size?: number; uploadedAt?: string };
  tasks: string[];
  manifest: Manifest;
  runs: { total: number; phases: Partial<Record<RunPhase, number>>; firstRunAt?: string; lastRunAt?: string };
}

export interface FlykeEvent {
  metadata: ObjectMeta;
  name: string;
  payload: Record<string, unknown>;
  matched?: string[];
}

export interface Resources {
  cpu?: number;
  memoryBytes?: number;
}

export interface Worker {
  metadata: ObjectMeta;
  spec: { unschedulable: boolean; draining?: boolean };
  status: {
    phase: "Ready" | "NotReady";
    hostname: string;
    pool: string;
    runtimes: string[];
    slots: number;
    freeSlots: number;
    capacity: Resources;
    activeTasks: number;
    agentVersion?: string;
    lastHeartbeat?: string;
  };
}

export interface Pool {
  metadata: ObjectMeta;
  spec: { selector?: Record<string, string>; maxConcurrentTasks?: number; resourceProfiles?: Record<string, Resources> };
  status: { workers: number; readyWorkers: number; freeSlots: number };
}

export interface JoinToken {
  id: string;
  pool: string;
  expiresAt: string;
  maxUses: number;
  uses: number;
  token?: string;
}

export type SecretType = "Private" | "Shared";

/** A secret or variable. `value` is never present for sensitive secrets. */
export interface Secret {
  id: string;
  namespace?: string;
  key: string;
  value?: string;
  sensitive: boolean;
  type: SecretType;
  createdAt: string;
  updatedAt: string;
}

export interface SecretSpec {
  key?: string;
  value?: string;
  sensitive?: boolean;
  type?: SecretType;
}

// ------------------------------------------------------------ playground ----

export interface PlaygroundInfo { enabled: boolean; reason?: string }

export interface PlaygroundWorkflow { entrypoint: string; manifest: Manifest }

export interface PlaygroundError { file: string; line?: number; error: string }

export interface PlaygroundProject {
  name: string;
  files?: Record<string, string>;
  updatedAt?: string;
  discovery?: { workflows: PlaygroundWorkflow[]; errors: PlaygroundError[] };
}

// ------------------------------------------------------------ namespaces ----

/** A namespace is a tenant. */
export interface Namespace {
  metadata: ObjectMeta;
  spec: { description?: string; sharedWorkers: boolean };
  status: { workflows: number; activeRuns: number; workers: number; tokens: number };
}

export interface AccessToken {
  id: string;
  name: string;
  namespace: string;
  createdAt: string;
  expiresAt?: string;
  /** Only present right after creation. */
  token?: string;
}

export interface WhoAmI {
  admin: boolean;
  namespace?: string;
  token?: string;
  authRequired: boolean;
}

// -------------------------------------------------------------- schedules ----

export interface ScheduleSpec {
  workflow: string;
  /** 5-field cron, a shortcut (@hourly, @daily…) or an interval ("@every 15m"). */
  cron: string;
  /** IANA time zone the expression is read in (default UTC). */
  tz?: string;
  inputs?: Record<string, unknown>;
  /** What to do with a tick missed while the server was down. */
  missed?: "run_once" | "skip";
  paused?: boolean;
}

export interface Schedule {
  metadata: ObjectMeta;
  spec: ScheduleSpec;
  status: {
    /** The cron expression in plain English. */
    description: string;
    /** Declared in workflow code (on.schedule): changed by deploying. */
    declared?: boolean;
    nextRunAt?: string;
    upcoming?: string[];
    lastRunAt?: string;
    lastRun?: string;
    lastRunPhase?: RunPhase;
    lastError?: string;
  };
}

export interface SchedulePreview {
  description: string;
  upcoming: string[];
}
