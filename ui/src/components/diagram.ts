// Workflow diagrams: the static tree of run() (from the SDK) and what a run
// actually did, overlaid on it from the run's history and task runs.

import type { GraphLane, GraphNode, GraphSource, HistoryEvent, JSONSchema, Manifest, Run, TaskRun, TaskSignature } from "../api/types";
import { TERMINAL } from "../api/types";
import type { Status } from "./ui";

export const START_ID = "start";
export const END_ID = "end";

export interface InputField {
  name: string;
  type: string;
  required: boolean;
  default?: unknown;
}

export interface Diagram {
  tree: GraphNode[];
  output: { type: string; expr: string | null; sources: GraphSource[] };
  tasks: Record<string, TaskSignature>;
  dynamicTasks: string[];
  inputs: InputField[];
  /** False when the SDK did not describe run() (old SDK or unreadable source). */
  known: boolean;
}

function schemaType(s: JSONSchema | undefined): string {
  if (!s) return "any";
  if (s.enum) return s.enum.map((v) => JSON.stringify(v)).join(" | ");
  if (s.anyOf) return s.anyOf.map(schemaType).join(" | ");
  const t = Array.isArray(s.type) ? s.type.join(" | ") : s.type ?? "any";
  return t === "array" ? `${schemaType(s.items)}[]` : t;
}

export function inputsOf(m?: Manifest): InputField[] {
  const schema = m?.inputs_schema;
  const required = new Set(schema?.required ?? []);
  return Object.entries(schema?.properties ?? {}).map(([name, s]) => ({ name, type: schemaType(s), required: required.has(name), default: s.default }));
}

/** The diagram of a manifest, or an empty one (a run then draws what executed). */
export function diagramOf(m?: Manifest): Diagram {
  const g = m?.graph;
  let tree = g?.tree;
  if (!tree && g?.nodes?.length) {
    // Older SDKs describe a flat list of call sites: draw it as a sequence.
    tree = g.nodes.map((n) => {
      const node: GraphNode = { id: `s${n.id}`, kind: n.kind, label: n.kind === "sleep" ? `sleep ${n.name}` : n.name, line: n.line };
      if (n.kind === "task") node.task = n.name;
      if (n.kind === "wait" || n.kind === "emit") node.event = n.name;
      if (!n.fanout && !n.loop) return node;
      return { id: `w${n.id}`, kind: n.loop ? "loop" : "parallel", label: n.loop ?? "in parallel", lanes: [{ label: n.loop ?? "for each item", nodes: [node], fan_out: !n.loop }] };
    });
  }
  return {
    tree: tree ?? [],
    output: g?.output ?? { type: "Any", expr: null, sources: [] },
    tasks: g?.tasks ?? {},
    dynamicTasks: g?.dynamic_tasks ?? [],
    inputs: inputsOf(m),
    known: !!tree?.length,
  };
}

export function flattenNodes(nodes: GraphNode[]): GraphNode[] {
  return nodes.flatMap((n) => [n, ...(n.lanes ?? []).flatMap((lane) => flattenNodes(lane.nodes))]);
}

// --------------------------------------------------------------- runs ----

export type ExecKind = "task" | "wait" | "sleep" | "emit";

export type NodeStatus =
  | "running" | "waiting" | "retrying" | "succeeded" | "reused" | "failed"
  | "unknown" | "compensated" | "cancelled" | "pending" | "not-reached";

/** One executed step of a run: a command ref and what became of it. */
export interface Exec {
  ref: number;
  kind: ExecKind;
  name: string;
  status: NodeStatus;
  start: HistoryEvent;
  outcome?: HistoryEvent;
  task?: TaskRun;
  compensation?: TaskRun;
}

export interface NodeRun {
  execs: Exec[];
  status: NodeStatus;
}

export interface RunOverlay {
  byNode: Map<string, NodeRun>;
  /** Steps the static diagram could not place (tasks chosen at run time). */
  dynamic: GraphNode[];
  /** Node of each executed ref. */
  nodeOfRef: Map<number, string>;
}

const START_EVENTS: Record<string, ExecKind> = { TaskScheduled: "task", TimerStarted: "sleep", WaitStarted: "wait", EventEmitted: "emit" };
const DONE_EVENTS = new Set(["TaskCompleted", "TaskFailed", "TimerFired", "EventReceived", "WaitTimedOut"]);

/** Latest task run (not compensation) per ref. */
export function taskByRef(tasks: TaskRun[]): Map<number, TaskRun> {
  const m = new Map<number, TaskRun>();
  for (const t of tasks) if (t.kind === "task") m.set(t.ref, t);
  return m;
}

export function compensationByRef(tasks: TaskRun[]): Map<number, TaskRun> {
  const m = new Map<number, TaskRun>();
  for (const t of tasks) if (t.kind === "compensation") m.set(t.ref, t);
  return m;
}

/** Every durable step the run issued, in order. */
export function execsOf(run: Run, history: HistoryEvent[], tasks: TaskRun[]): Exec[] {
  const finished = TERMINAL.includes(run.status.phase);
  const byRef = taskByRef(tasks);
  const comps = compensationByRef(tasks);
  const out: Exec[] = [];
  const seen = new Set<number>();
  for (const ev of history) {
    const kind = START_EVENTS[ev.type];
    if (!kind || !ev.ref || seen.has(ev.ref)) continue;
    seen.add(ev.ref);
    const outcome = history.find((h) => h.ref === ev.ref && DONE_EVENTS.has(h.type) && h.id >= ev.id);
    const e: Exec = { ref: ev.ref, kind, name: ev.name ?? kind, status: "running", start: ev, outcome };
    if (kind === "task") {
      const t = byRef.get(ev.ref);
      const c = comps.get(ev.ref);
      e.task = t;
      e.compensation = c;
      if (c) e.status = c.phase === "Succeeded" ? "compensated" : c.phase === "Failed" ? "failed" : "running";
      else if (t) e.status = taskPhaseStatus(t);
      else e.status = outcome?.type === "TaskCompleted" ? "succeeded" : outcome?.type === "TaskFailed" ? "failed" : finished ? "cancelled" : "running";
    } else if (kind === "sleep") {
      e.status = outcome ? "succeeded" : finished ? "cancelled" : "waiting";
    } else if (kind === "wait") {
      e.status = outcome?.type === "EventReceived" ? "succeeded" : outcome?.type === "WaitTimedOut" ? "failed" : finished ? "cancelled" : "waiting";
    } else {
      e.status = "succeeded";
    }
    out.push(e);
  }
  return out;
}

function taskPhaseStatus(t: TaskRun): NodeStatus {
  if (t.reusedFrom) return "reused";
  switch (t.phase) {
    case "Pending": return t.attempt > 1 ? "retrying" : "waiting";
    case "Scheduled":
    case "Running": return "running";
    case "Succeeded": return "succeeded";
    case "Failed": return "failed";
    case "Unknown": return "unknown";
    default: return "cancelled";
  }
}

function nodeKey(n: GraphNode): string | null {
  switch (n.kind) {
    case "task": return n.task ? `task:${n.task}` : null;
    case "wait":
    case "emit": return n.event ? `${n.kind}:${n.event}` : null;
    case "sleep": return "sleep";
    default: return null;
  }
}

function execKey(e: Exec): string {
  return e.kind === "sleep" ? "sleep" : `${e.kind}:${e.name}`;
}

/** Nodes that execute once per item: inside a fan-out lane or a loop. */
function repeatedNodes(nodes: GraphNode[], repeated = false, out = new Set<string>()): Set<string> {
  for (const n of nodes) {
    if (repeated) out.add(n.id);
    for (const lane of n.lanes ?? []) repeatedNodes(lane.nodes, repeated || !!lane.fan_out || n.kind === "loop", out);
  }
  return out;
}

/** Attach a run's executed steps to the nodes of the workflow diagram. */
export function overlayRun(d: Diagram, run: Run, execs: Exec[]): RunOverlay {
  const active = !TERMINAL.includes(run.status.phase);
  const nodes = flattenNodes(d.tree);
  const repeated = repeatedNodes(d.tree);
  const nodesByKey = new Map<string, GraphNode[]>();
  for (const n of nodes) {
    const k = nodeKey(n);
    if (k) nodesByKey.set(k, [...(nodesByKey.get(k) ?? []), n]);
  }
  const execsByKey = new Map<string, Exec[]>();
  for (const e of execs) execsByKey.set(execKey(e), [...(execsByKey.get(execKey(e)) ?? []), e]);

  const assigned = new Map<string, Exec[]>();
  const dynamic: GraphNode[] = [];
  const synth = (id: string, e: Exec): GraphNode => ({
    id, kind: e.kind, label: e.kind === "sleep" ? "sleep" : e.name,
    ...(e.kind === "task" ? { task: e.name } : e.kind === "wait" || e.kind === "emit" ? { event: e.name } : {}),
  });
  if (!d.known) {
    // Nothing to match against: one node per executed step, in order.
    for (const e of execs) {
      const n = synth(`dyn:r${e.ref}`, e);
      dynamic.push(n);
      assigned.set(n.id, [e]);
    }
  } else {
    for (const [key, list] of execsByKey) {
      const candidates = nodesByKey.get(key) ?? [];
      if (!candidates.length) {
        const n = synth(`dyn:${key}`, list[0]);
        dynamic.push(n);
        assigned.set(n.id, list);
      } else if (candidates.length === 1 || candidates.some((n) => repeated.has(n.id)) || candidates.length !== list.length) {
        // Can't tell call sites apart: every call site shows every execution.
        for (const n of candidates) assigned.set(n.id, list);
      } else {
        // Same step at N call sites, N executions: the i-th call is the i-th site.
        candidates.forEach((n, i) => assigned.set(n.id, [list[i]]));
      }
    }
  }

  const byNode = new Map<string, NodeRun>();
  const nodeOfRef = new Map<number, string>();
  for (const n of [...nodes, ...dynamic]) {
    const list = assigned.get(n.id) ?? [];
    for (const e of list) if (!nodeOfRef.has(e.ref)) nodeOfRef.set(e.ref, n.id);
    byNode.set(n.id, { execs: list, status: combine(list.map((e) => e.status), active) });
  }
  // Containers take the status of what ran inside them, innermost first.
  for (const n of [...nodes].reverse()) {
    if (!n.lanes?.length) continue;
    const inner = n.lanes.flatMap((lane) => lane.nodes).map((c) => byNode.get(c.id)?.status ?? "not-reached");
    byNode.set(n.id, { execs: [], status: combine(inner, active) });
  }
  return { byNode, dynamic, nodeOfRef };
}

const PRIORITY: NodeStatus[] = ["running", "retrying", "waiting", "unknown", "failed", "compensated", "cancelled", "succeeded", "reused"];

function combine(statuses: NodeStatus[], active: boolean): NodeStatus {
  const ran = statuses.filter((s) => s !== "not-reached" && s !== "pending");
  if (!ran.length) return active ? "pending" : "not-reached";
  return PRIORITY.find((s) => (ran as NodeStatus[]).includes(s)) ?? ran[0];
}

/** Did any node in this lane execute? (A branch shows the path that was taken.) */
export function laneTaken(lane: GraphLane, overlay: RunOverlay | null): boolean | null {
  if (!overlay) return null;
  return flattenNodes(lane.nodes).some((n) => (overlay.byNode.get(n.id)?.execs.length ?? 0) > 0);
}

export const STATUS_TONE: Record<NodeStatus, Status> = {
  running: "info", waiting: "waiting", retrying: "warning", succeeded: "good", reused: "good", failed: "critical",
  unknown: "warning", compensated: "serious", cancelled: "muted", pending: "muted", "not-reached": "muted",
};

export const STATUS_ICON: Record<NodeStatus, string> = {
  running: "●", waiting: "◷", retrying: "↻", succeeded: "✓", reused: "✓", failed: "✕",
  unknown: "?", compensated: "↶", cancelled: "⊘", pending: "○", "not-reached": "–",
};

export function statusLabel(s: NodeStatus): string {
  return { "not-reached": "not reached", reused: "reused", compensated: "compensated", unknown: "outcome unknown" }[s as string] ?? s;
}
