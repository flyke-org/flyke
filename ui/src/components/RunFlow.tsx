import { useEffect, useMemo, useState, type FormEvent, type ReactNode } from "react";
import { Link, useNavigate } from "react-router-dom";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useConnection, useScope } from "../api/context";
import { TERMINAL, type GraphNode, type GraphSource, type HistoryEvent, type Manifest, type RetryMode, type Run, type TaskRun, type TaskSignature, type TaskSpec } from "../api/types";
import { dateTime, duration, shortType, span } from "./format";
import {
  END_ID, START_ID, STATUS_ICON, STATUS_TONE, compensationByRef, diagramOf, execsOf, flattenNodes, overlayRun, statusLabel,
  type Diagram, type Exec, type NodeStatus, type RunOverlay,
} from "./diagram";
import { FlowDiagram } from "./FlowDiagram";
import { ErrorBox, Json, Modal, PhaseBadge, TaskBadge } from "./ui";

const GUARANTEE: Record<TaskSpec["guarantee"], string> = {
  at_least_once: "at-least-once",
  at_most_once: "at-most-once",
  effectively_once: "effectively-once",
};

const KIND_TEXT: Record<string, string> = {
  task: "task", wait: "wait for event", sleep: "durable timer", emit: "emit event", return: "early return",
  parallel: "parallel block", branch: "branch", loop: "loop", try: "try / except", finally: "finally",
};

/** Positional task args → {param: value} when the manifest names them. */
export function namedArgs(args: unknown, params?: string[]): unknown {
  if (!Array.isArray(args) || !params || params.length !== args.length) return args;
  return Object.fromEntries(params.map((p, i) => [p, args[i]]));
}

function StatusBadge({ status }: { status: NodeStatus }) {
  return <span className={`badge badge-${STATUS_TONE[status]}`}><span aria-hidden className={status === "running" ? "pulse" : undefined}>{STATUS_ICON[status]}</span>{statusLabel(status)}</span>;
}

// ------------------------------------------------------------ run flow ----

export interface RetryIntent {
  mode: RetryMode;
  fromRef?: number;
  editInputs?: boolean;
}

/** The run as a diagram, with an inspector for the selected step. */
export function RunFlow({ run, history, tasks, manifest, now, onRetry, onShowLogs }: {
  run: Run;
  history: HistoryEvent[];
  tasks: TaskRun[];
  /** The manifest of the version this run executes, if known. */
  manifest?: Manifest;
  now: number;
  onRetry: (intent: RetryIntent) => void;
  onShowLogs: (task: string) => void;
}) {
  const diagram = useMemo(() => diagramOf(manifest), [manifest]);
  const execs = useMemo(() => execsOf(run, history, tasks), [run, history, tasks]);
  const overlay = useMemo(() => overlayRun(diagram, run, execs), [diagram, run, execs]);
  const [selected, setSelected] = useState<string | null>(null);
  // Open on the step that needs attention, once.
  const [autoPicked, setAutoPicked] = useState(false);
  useEffect(() => {
    if (autoPicked) return;
    const hot = execs.find((e) => ["failed", "unknown", "retrying"].includes(e.status));
    const id = hot && overlay.nodeOfRef.get(hot.ref);
    if (id) setSelected(id);
    if (id || TERMINAL.includes(run.status.phase)) setAutoPicked(true);
  }, [execs, overlay, autoPicked, run.status.phase]);
  const select = (id: string) => setSelected(id === selected ? null : id);

  return (
    <div className="diagram-box"><div className={`diagram ${selected ? "has-panel" : ""}`}>
      <div style={{ minWidth: 0 }}>
        <FlowDiagram diagram={diagram} manifest={manifest} run={run} overlay={overlay} selected={selected} onSelect={select} now={now} />
        <div className="flow-hint">
          Click a step to see where its inputs come from, its output and logs{TERMINAL.includes(run.status.phase) ? ", or to retry from it" : ""}.
          {!diagram.known && <> The code of this run's version is not described, so only executed steps are drawn.</>}
        </div>
      </div>
      {selected && (
        <Inspector
          id={selected} diagram={diagram} manifest={manifest} run={run} overlay={overlay} history={history} tasks={tasks} now={now}
          onSelect={setSelected} onClose={() => setSelected(null)} onRetry={onRetry} onShowLogs={onShowLogs}
        />
      )}
    </div></div>
  );
}

/** The workflow as written in run(), before any run. */
export function WorkflowFlow({ manifest }: { manifest: Manifest }) {
  const diagram = useMemo(() => diagramOf(manifest), [manifest]);
  const [selected, setSelected] = useState<string | null>(null);
  if (!diagram.known) {
    return <p className="muted" style={{ margin: 0 }}>No diagram: this workflow was registered by an SDK that does not describe its shape. Re-register it with <code>flyke run</code> or <code>flyke deploy</code>.</p>;
  }
  return (
    <div className="diagram-box"><div className={`diagram ${selected ? "has-panel" : ""}`}>
      <div style={{ minWidth: 0 }}>
        <FlowDiagram diagram={diagram} manifest={manifest} selected={selected} onSelect={(id) => setSelected(id === selected ? null : id)} />
        <div className="flow-hint">
          Read from <code>run()</code>: <code>asyncio.gather</code> runs lanes side by side, <code>if</code> becomes alternative lanes.
          Each step shows where its arguments come from (<span className="mono">← build</span>). Click a step for details.
        </div>
      </div>
      {selected && <Inspector id={selected} diagram={diagram} manifest={manifest} onSelect={setSelected} onClose={() => setSelected(null)} />}
    </div></div>
  );
}

// ----------------------------------------------------------- inspector ----

interface InspectorProps {
  id: string;
  diagram: Diagram;
  manifest?: Manifest;
  run?: Run;
  overlay?: RunOverlay;
  history?: HistoryEvent[];
  tasks?: TaskRun[];
  now?: number;
  onSelect: (id: string) => void;
  onClose: () => void;
  onRetry?: (i: RetryIntent) => void;
  onShowLogs?: (task: string) => void;
}

function Inspector(p: InspectorProps) {
  const nodes = useMemo(() => [...flattenNodes(p.diagram.tree), ...(p.overlay?.dynamic ?? [])], [p.diagram, p.overlay]);
  const byId = useMemo(() => new Map(nodes.map((n) => [n.id, n])), [nodes]);
  const node = byId.get(p.id);
  const head = (title: ReactNode, badge?: ReactNode, sub?: ReactNode) => (
    <div className="stack" style={{ gap: 2 }}>
      <div className="row between gap-sm">
        <div className="row gap-sm wrap" style={{ minWidth: 0 }}>
          <h2 className="mono" style={{ overflowWrap: "anywhere" }}>{title}</h2>
          {badge}
        </div>
        <button className="btn btn-ghost btn-sm" onClick={p.onClose} aria-label="Close inspector">✕</button>
      </div>
      {sub && <div className="muted" style={{ fontSize: 12.5 }}>{sub}</div>}
    </div>
  );
  const ctx = { ...p, byId, head, nodes };

  let body: ReactNode;
  if (p.id === START_ID) body = <StartPanel {...ctx} />;
  else if (p.id === END_ID) body = <EndPanel {...ctx} />;
  else if (!node) return null;
  else if (node.lanes?.length) body = <BlockPanel {...ctx} node={node} />;
  else body = <StepPanel key={node.id} {...ctx} node={node} />;
  return <aside className="inspector" aria-label="Step details">{body}</aside>;
}

type PanelProps = InspectorProps & {
  byId: Map<string, GraphNode>;
  nodes: GraphNode[];
  head: (title: ReactNode, badge?: ReactNode, sub?: ReactNode) => ReactNode;
};

function StartPanel({ diagram, run, onRetry, head }: PanelProps) {
  const values = (run?.spec.inputs ?? {}) as Record<string, unknown>;
  const finished = run && TERMINAL.includes(run.status.phase);
  return (
    <>
      {head("Inputs", undefined, "Workflow inputs, read in run() as self.<name>")}
      {run && (
        <dl className="kv">
          <dt>Triggered by</dt><dd>{run.spec.triggeredBy ?? "manual"}</dd>
          <dt>Version</dt><dd className="mono">{run.spec.version}</dd>
          {run.spec.retry && <><dt>Retry of</dt><dd><Link className="mono" to={`/runs/${run.spec.retry.of}`}>{run.spec.retry.of}</Link> - {retryLabel(run)}</dd></>}
        </dl>
      )}
      {diagram.inputs.length === 0 ? <p className="muted" style={{ margin: 0 }}>This workflow takes no inputs.</p> : (
        <table className="io-table">
          <thead><tr><th>Name</th><th>{run ? "Value" : "Default"}</th></tr></thead>
          <tbody>
            {diagram.inputs.map((i) => (
              <tr key={i.name}>
                <td><div className="mono">{i.name}{i.required && <span className="required">*</span>}</div><div className="flow-type">{i.type}</div></td>
                <td className="io-value">{run ? <Value value={values[i.name]} /> : i.required ? <span className="muted">required</span> : <Value value={i.default} />}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {finished && onRetry && <div className="row gap-sm wrap"><button className="btn btn-sm" onClick={() => onRetry({ mode: "all", editInputs: true })}>↻ Run again with other inputs…</button></div>}
    </>
  );
}

function EndPanel({ diagram, run, now, byId, onSelect, onRetry, head }: PanelProps) {
  const st = run?.status;
  const finished = run && TERMINAL.includes(run.status.phase);
  return (
    <>
      {head("Output", st && <PhaseBadge phase={st.phase} />, "What run() returns")}
      <section>
        <h3>Returns</h3>
        <div className="stack" style={{ gap: 6 }}>
          <span className="flow-type">{diagram.output.type}</span>
          {diagram.output.expr && <pre className="code-line">return {diagram.output.expr}</pre>}
          {diagram.output.sources.length > 0 && <Sources sources={diagram.output.sources} byId={byId} onSelect={onSelect} />}
        </div>
      </section>
      {st && (
        <dl className="kv">
          <dt>Duration</dt><dd className="tabular">{span(st.startedAt, st.finishedAt, now)}</dd>
          {st.reason && <><dt>Reason</dt><dd>{st.reason}</dd></>}
          {st.compensation && <><dt>Compensation</dt><dd>{st.compensation}</dd></>}
        </dl>
      )}
      {st && (st.failure ? <FailureBox f={st.failure} /> : finished && <section><h3>Result</h3><Json value={st.result} /></section>)}
      {finished && st?.phase !== "Succeeded" && onRetry && (
        <div className="row gap-sm wrap"><button className="btn btn-primary btn-sm" onClick={() => onRetry({ mode: "failed" })}>↻ Retry failed steps…</button></div>
      )}
    </>
  );
}

function BlockPanel({ node, overlay, byId, onSelect, head }: PanelProps & { node: GraphNode }) {
  const status = overlay?.byNode.get(node.id)?.status;
  return (
    <>
      {head(node.kind === "loop" || node.kind === "branch" ? node.label : KIND_TEXT[node.kind], status && <StatusBadge status={status} />, node.line ? `line ${node.line}` : undefined)}
      <p className="secondary" style={{ margin: 0 }}>
        {node.kind === "parallel" && "These steps run concurrently with asyncio.gather; the block finishes when all of them do. A stacked lane runs once per item."}
        {node.kind === "branch" && "Only one of these paths runs, depending on the condition. In a run, the path that was not taken is dimmed."}
        {node.kind === "loop" && "The steps inside run once per iteration, one after the other."}
        {node.kind === "try" && "If a step in the try lane raises, the matching except lane runs."}
        {node.kind === "finally" && "These steps always run, whether the try block succeeded or not."}
      </p>
      {(node.sources?.length ?? 0) > 0 && <section><h3>Depends on</h3><Sources sources={node.sources!} byId={byId} onSelect={onSelect} /></section>}
    </>
  );
}

function StepPanel({ node, manifest, run, overlay, tasks = [], now, nodes, byId, onSelect, onRetry, onShowLogs, head }: PanelProps & { node: GraphNode }) {
  const nodeRun = overlay?.byNode.get(node.id);
  const execs = nodeRun?.execs ?? [];
  const [ref, setRef] = useState<number | undefined>(execs.find((e) => e.status === "failed")?.ref ?? execs[0]?.ref);
  const exec = execs.find((e) => e.ref === ref) ?? execs[0];
  const spec = node.task ? manifest?.tasks[node.task] : undefined;
  const sig = node.task ? manifest?.graph?.tasks?.[node.task] : undefined;
  const finished = run && TERMINAL.includes(run.status.phase);
  const usedBy = nodes.filter((n) => [...(n.args ?? []).flatMap((a) => a.sources), ...(n.sources ?? [])].some((s) => s.kind === "step" && s.node === node.id));
  const status = nodeRun?.status;

  return (
    <>
      {head(node.label, status && <StatusBadge status={status} />, <>{KIND_TEXT[node.kind]}{node.line ? <> - line {node.line}</> : null}{node.id.startsWith("dyn:") ? " - called at run time" : ""}</>)}
      {sig?.description && <p className="secondary" style={{ margin: 0 }}>{sig.description}</p>}

      {execs.length > 1 && (
        <div className="exec-tabs" role="tablist" aria-label="Executions">
          {execs.map((e) => (
            <button key={e.ref} role="tab" aria-selected={e.ref === exec?.ref} className={e.ref === exec?.ref ? "on" : ""} onClick={() => setRef(e.ref)}>
              <span className={`tone-${STATUS_TONE[e.status]}`} aria-hidden>{STATUS_ICON[e.status]}</span> #{e.ref}
            </button>
          ))}
        </div>
      )}

      <section>
        <h3>Inputs</h3>
        <InputTable node={node} sig={sig} exec={exec} spec={spec} byId={byId} onSelect={onSelect} />
      </section>

      {node.kind !== "emit" && node.kind !== "sleep" && (
        <section>
          <h3>Output</h3>
          <div className="stack" style={{ gap: 6 }}>
            {(node.kind === "task" || node.kind === "wait" || node.assign) && (
              <div className="row gap-sm wrap" style={{ fontSize: 12.5 }}>
                <span className="flow-type">{node.kind === "wait" ? "Event" : sig?.returns ?? "Any"}</span>
                {node.assign && <span className="muted">assigned to <code>{node.assign}</code></span>}
              </div>
            )}
            {usedBy.length > 0 && (
              <span className="sources">used by {usedBy.map((n) => <button key={n.id} type="button" className="source-chip source-step" onClick={() => onSelect(n.id)}>{n.label}</button>)}</span>
            )}
            {exec && <ExecOutput exec={exec} />}
          </div>
        </section>
      )}

      {exec && <Execution exec={exec} spec={spec} now={now} tasks={tasks} />}
      {run && !exec && <p className="muted" style={{ margin: 0 }}>{finished ? "This run never reached this step." : "Not reached yet."}</p>}
      {!run && spec && <SpecFacts spec={spec} />}

      {run && exec && (
        <div className="stack" style={{ gap: 8 }}>
          {node.kind === "task" && onShowLogs && <div className="row gap-sm wrap"><button className="btn btn-sm" onClick={() => onShowLogs(node.label)}>≡ Logs of {node.label}</button></div>}
          {finished && onRetry ? (
            <div className="row gap-sm wrap">
              <button className="btn btn-primary btn-sm" onClick={() => onRetry({ mode: "from", fromRef: exec.ref })} title="Reuse everything before this step; run it and everything after it again">↻ Retry from here…</button>
              {run.status.phase !== "Succeeded" && <button className="btn btn-sm" onClick={() => onRetry({ mode: "failed" })}>Retry failed steps…</button>}
            </div>
          ) : (
            <p className="muted" style={{ margin: 0, fontSize: 12.5 }}>Retry becomes available once the run finishes{run.status.phase === "Paused" ? " — or resume it" : ""}.</p>
          )}
        </div>
      )}
    </>
  );
}

function InputTable({ node, sig, exec, spec, byId, onSelect }: {
  node: GraphNode; sig?: TaskSignature; exec?: Exec; spec?: TaskSpec; byId: Map<string, GraphNode>; onSelect: (id: string) => void;
}) {
  const args = (node.args ?? []).filter((a) => !((node.kind === "wait" || node.kind === "emit") && a.name === "event"));
  const params = sig?.params ?? (spec?.params ?? []).map((name) => ({ name, type: "", default: undefined as unknown }));
  const raw = exec?.kind === "task" ? exec.task?.args ?? exec.start.payload : exec?.start.payload;
  const named = exec?.kind === "task" ? namedArgs(raw, params.map((x) => x.name)) : raw;
  const values = named && typeof named === "object" && !Array.isArray(named) ? (named as Record<string, unknown>) : null;
  const names = [...params.map((x) => x.name), ...args.map((a) => a.name).filter((n) => !params.some((x) => x.name === n))];
  if (values) for (const k of Object.keys(values)) if (!names.includes(k) && k !== "event") names.push(k);

  if (!names.length) return exec && raw !== undefined && !values ? <Json value={raw} /> : <p className="muted" style={{ margin: 0 }}>No inputs.</p>;
  return (
    <table className="io-table">
      <thead><tr><th>Name</th><th>{exec ? "Value - from" : "From"}</th></tr></thead>
      <tbody>
        {names.map((name) => {
          const param = params.find((x) => x.name === name);
          const arg = args.find((a) => a.name === name);
          return (
            <tr key={name}>
              <td><div className="mono">{name}</div>{param?.type && <div className="flow-type">{param.type}</div>}</td>
              <td className="io-value">
                {values && name in values && <Value value={values[name]} />}
                {arg && (
                  <div className="io-origin">
                    <code>{arg.expr}</code>
                    {arg.sources.length > 0 && <Sources sources={arg.sources} byId={byId} onSelect={onSelect} />}
                  </div>
                )}
                {!arg && !(values && name in values) && param && (
                  <span className="muted">{param.default !== undefined ? `default ${JSON.stringify(param.default)}` : "not passed in run()"}</span>
                )}
              </td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}

function ExecOutput({ exec }: { exec: Exec }) {
  if (exec.kind === "wait") {
    if (exec.outcome?.type === "EventReceived") return <Json value={exec.outcome.payload} />;
    if (exec.outcome?.type === "WaitTimedOut") return <span style={{ color: "var(--critical-ink)" }}>✕ Timed out waiting for the event.</span>;
    return <span className="muted">{exec.status === "waiting" ? "Waiting for the event…" : "No event received."}</span>;
  }
  const t = exec.task;
  const failure = t?.failure ?? exec.outcome?.failure;
  if (failure && t?.phase !== "Succeeded") return <FailureBox f={failure} title={t?.phase === "Pending" ? "Last attempt failed (retrying)" : "Error"} />;
  const result = t?.result ?? (exec.outcome?.type === "TaskCompleted" ? exec.outcome.payload : undefined);
  if (result !== undefined || t?.phase === "Succeeded") return <Json value={result ?? null} />;
  return <span className="muted">{exec.status === "running" ? "Running…" : exec.status === "waiting" ? "Queued…" : "No result."}</span>;
}

function Execution({ exec, spec, now, tasks }: { exec: Exec; spec?: TaskSpec; now?: number; tasks: TaskRun[] }) {
  const t = exec.task;
  const comp = compensationByRef(tasks).get(exec.ref);
  return (
    <section>
      <h3>Execution</h3>
      <dl className="kv">
        <dt>Ref</dt><dd className="mono">#{exec.ref}</dd>
        {t && <><dt>Attempts</dt><dd className="tabular">{t.attempt}{spec?.retry ? ` of ${spec.retry.attempts}` : ""}</dd></>}
        {t?.worker && <><dt>Worker</dt><dd className="mono">{t.worker}</dd></>}
        {t ? <><dt>Duration</dt><dd className="tabular">{t.reusedFrom ? "— (reused)" : span(t.startedAt, t.finishedAt, now)}</dd></>
          : <><dt>Started</dt><dd>{dateTime(exec.start.time)}</dd>{exec.outcome && <><dt>Resolved</dt><dd>{dateTime(exec.outcome.time)} <span className="muted">({span(exec.start.time, exec.outcome.time)})</span></dd></>}</>}
        {t?.reusedFrom && <><dt>Reused from</dt><dd><Link className="mono" to={`/runs/${t.reusedFrom}`}>{t.reusedFrom}</Link> <span className="muted">(not executed again)</span></dd></>}
        {spec && <><dt>Guarantee</dt><dd>{GUARANTEE[spec.guarantee]}</dd></>}
        {t?.idempotencyKey && <><dt>Idempotency key</dt><dd className="mono">{t.idempotencyKey}</dd></>}
        {comp && <><dt>Compensation</dt><dd>↶ <span className="mono">{comp.name}</span> <TaskBadge phase={comp.phase} /></dd></>}
        {exec.kind === "wait" && <><dt>Matching</dt><dd><Json value={exec.start.payload ?? {}} /></dd></>}
      </dl>
    </section>
  );
}

function Sources({ sources, byId, onSelect }: { sources: GraphSource[]; byId: Map<string, GraphNode>; onSelect: (id: string) => void }) {
  return (
    <span className="sources">
      from
      {sources.map((s, i) =>
        s.kind === "input" ? (
          <button key={i} type="button" className="source-chip source-input" onClick={() => onSelect(START_ID)} title="A workflow input">input {s.name}</button>
        ) : (
          <button key={i} type="button" className="source-chip source-step" onClick={() => s.node && onSelect(s.node)} title="The result of an earlier step">
            {s.task ?? byId.get(s.node ?? "")?.label ?? s.node}
            {byId.get(s.node ?? "")?.assign ? ` → ${byId.get(s.node ?? "")!.assign}` : ""}
          </button>
        ),
      )}
    </span>
  );
}

function Value({ value }: { value: unknown }) {
  if (value !== null && typeof value === "object") return <Json value={value} />;
  return <span className="mono">{value === undefined ? <span className="muted">—</span> : JSON.stringify(value)}</span>;
}

function FailureBox({ f, title = "Error" }: { f: { type: string; message: string; stack?: string }; title?: string }) {
  return (
    <div>
      <h3>{title}</h3>
      <div className="stack" style={{ gap: 6 }}>
        <strong style={{ color: "var(--critical-ink)" }}>✕ {shortType(f.type)}: <span style={{ fontWeight: 400 }}>{f.message}</span></strong>
        {f.stack && <pre className="json" style={{ fontSize: 11.5 }}>{f.stack}</pre>}
      </div>
    </div>
  );
}

function SpecFacts({ spec }: { spec: TaskSpec }) {
  return (
    <section>
      <h3>Options</h3>
      <dl className="kv">
        <dt>Guarantee</dt><dd>{GUARANTEE[spec.guarantee]}</dd>
        <dt>Retry</dt><dd>{spec.retry ? `${spec.retry.attempts}× ${spec.retry.backoff} ${duration(spec.retry.delay_ms)}${spec.retry.on?.length ? ` on ${spec.retry.on.map(shortType).join(", ")}` : ""}` : "default"}</dd>
        {spec.timeout_ms ? <><dt>Timeout</dt><dd>{duration(spec.timeout_ms)}</dd></> : null}
        {spec.on_unknown && spec.on_unknown !== "retry" && <><dt>If outcome unknown</dt><dd>{spec.on_unknown}</dd></>}
        {spec.idempotency_key && <><dt>Idempotency key</dt><dd className="mono">{spec.idempotency_key}</dd></>}
        {spec.compensation && <><dt>Compensation</dt><dd className="mono">↶ {spec.compensation}</dd></>}
      </dl>
    </section>
  );
}

// --------------------------------------------------------------- retry ----

export function retryLabel(run: Run): string {
  const r = run.spec.retry;
  if (!r) return "";
  if (r.mode === "from") return `from ${r.fromTask} (#${r.fromRef})`;
  return r.mode === "failed" ? "failed steps only" : "everything";
}

interface PlanRow {
  ref: number;
  name: string;
  kind: string;
  reuse: boolean;
  why: string;
}

/** Mirrors the control plane's reuse rule (run.Reuse) to preview a retry. */
export function retryPlan(history: HistoryEvent[], tasks: TaskRun[], mode: RetryMode, fromRef?: number): PlanRow[] {
  const comps = compensationByRef(tasks);
  const rows: PlanRow[] = [];
  const seen = new Set<number>();
  for (const ev of history) {
    if (!ev.ref || seen.has(ev.ref) || !["TaskScheduled", "TimerStarted", "WaitStarted", "EventEmitted"].includes(ev.type)) continue;
    seen.add(ev.ref);
    const has = (...types: string[]) => history.some((h) => h.ref === ev.ref && types.includes(h.type));
    const kind = { TaskScheduled: "task", TimerStarted: "sleep", WaitStarted: "wait", EventEmitted: "emit" }[ev.type]!;
    const ok = kind === "task" ? has("TaskCompleted") && !comps.has(ev.ref) : kind === "sleep" ? has("TimerFired") : kind === "wait" ? has("EventReceived", "WaitTimedOut") : true;
    const covered = mode === "failed" || (mode === "from" && fromRef !== undefined && ev.ref < fromRef);
    let why = "reuse recorded result";
    if (!covered) why = mode === "all" ? "runs again" : "runs again (from here on)";
    else if (!ok) why = comps.has(ev.ref) ? "runs again (was compensated)" : has("TaskFailed") ? "runs again (failed)" : "runs again (never finished)";
    rows.push({ ref: ev.ref, name: ev.name ?? kind, kind, reuse: covered && ok, why });
  }
  return rows.sort((a, b) => a.ref - b.ref);
}

export function RetryDialog({ run, history, tasks, latestVersion, initial, onClose }: {
  run: Run;
  history: HistoryEvent[];
  tasks: TaskRun[];
  latestVersion?: string;
  initial: RetryIntent;
  onClose: () => void;
}) {
  const { client } = useConnection();
  const scope = useScope();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const steps = useMemo(() => retryPlan(history, tasks, "all"), [history, tasks]);
  const firstBad = useMemo(() => retryPlan(history, tasks, "failed").find((r) => !r.reuse)?.ref, [history, tasks]);
  const [mode, setMode] = useState<RetryMode>(initial.mode);
  const [fromRef, setFromRef] = useState<number | undefined>(initial.fromRef ?? firstBad ?? steps[0]?.ref);
  const original = JSON.stringify(run.spec.inputs ?? {}, null, 2);
  const [inputs, setInputs] = useState(original);
  const [parseError, setParseError] = useState<Error | null>(null);
  const codeChanged = !!latestVersion && latestVersion !== run.spec.version;
  const [sameCode, setSameCode] = useState(false);
  const plan = useMemo(() => retryPlan(history, tasks, mode, fromRef), [history, tasks, mode, fromRef]);
  const reused = plan.filter((p) => p.reuse).length;

  const retry = useMutation({
    mutationFn: (body: { inputs?: unknown }) => client.retryRun(run.metadata.name, {
      mode, fromRef: mode === "from" ? fromRef : undefined, version: sameCode ? "same" : undefined, ...body,
    }),
    onSuccess: (r) => {
      qc.invalidateQueries({ queryKey: [scope, "runs"] });
      qc.invalidateQueries({ queryKey: [scope, "run", run.metadata.name] });
      onClose();
      navigate(`/runs/${r.metadata.name}`);
    },
  });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setParseError(null);
    let body: { inputs?: unknown } = {};
    if (inputs.trim() !== original.trim()) {
      try {
        body = { inputs: JSON.parse(inputs) };
      } catch (err) {
        setParseError(new Error(`Inputs must be a JSON object: ${(err as Error).message}`));
        return;
      }
    }
    retry.mutate(body);
  };

  const option = (m: RetryMode, title: string, desc: ReactNode, extra?: ReactNode) => (
    <label className="choice">
      <input type="radio" name="retry-mode" checked={mode === m} onChange={() => setMode(m)} />
      <span className="stack" style={{ gap: 2, minWidth: 0, flex: 1 }}>
        <strong>{title}</strong>
        <span className="secondary">{desc}</span>
        {extra}
      </span>
    </label>
  );

  return (
    <Modal title={`Retry ${run.metadata.name}`} onClose={onClose}>
      <form className="stack" onSubmit={submit}>
        {option("failed", "Only failed steps", "Reuse every step that succeeded; run what failed or never ran.")}
        {option("from", "From a step", "Reuse what ran before the chosen step; run it and everything after again.",
          mode === "from" && (
            <select value={fromRef} onChange={(e) => setFromRef(Number(e.target.value))} aria-label="Restart from step" style={{ marginTop: 6, maxWidth: "100%" }}>
              {steps.map((s) => <option key={s.ref} value={s.ref}>#{s.ref} - {s.kind === "task" ? s.name : `${s.kind} ${s.name}`}</option>)}
            </select>
          ))}
        {option("all", "Everything", "A fresh run with the same inputs; nothing is reused.")}

        {plan.length > 0 && (
          <div className="stack" style={{ gap: 6 }}>
            <div className="row between"><strong style={{ fontSize: 13 }}>What happens</strong><span className="muted" style={{ fontSize: 12 }}>{reused} reused - {plan.length - reused} run again</span></div>
            <ul className="plan">
              {plan.map((p) => (
                <li key={p.ref}>
                  <span className="mono muted" style={{ width: 28 }}>#{p.ref}</span>
                  <span className="mono grow" style={{ overflowWrap: "anywhere" }}>{p.kind === "task" ? p.name : `${p.kind} ${p.name}`}</span>
                  <span className={p.reuse ? "act-reuse" : "act-rerun"}>{p.reuse ? "✓ " : "↻ "}{p.why}</span>
                </li>
              ))}
            </ul>
            <span className="muted" style={{ fontSize: 12 }}>
              Steps the run never reached run too. A result is only reused if the task is called with the same arguments,
              so changed inputs or code re-run whatever they affect.
              {sameCode
                ? <> Runs the code this run used (<span className="mono">{run.spec.version}</span>).</>
                : codeChanged
                  ? <> Runs the <strong>current code</strong> (<span className="mono">{latestVersion}</span>, this run used <span className="mono">{run.spec.version}</span>).</>
                  : <> Runs the current code.</>}
            </span>
          </div>
        )}

        {codeChanged && (
          <label className="row gap-sm" style={{ fontSize: 13 }}>
            <input type="checkbox" checked={sameCode} onChange={(e) => setSameCode(e.target.checked)} />
            <span>Retry with the code this run used (<span className="mono">{run.spec.version}</span>) instead of the current version</span>
          </label>
        )}

        <details className="inputs" open={initial.editInputs}>
          <summary>Change inputs</summary>
          <textarea rows={Math.min(12, original.split("\n").length + 1)} value={inputs} onChange={(e) => setInputs(e.target.value)} aria-label="Inputs (JSON)" spellCheck={false} />
        </details>

        <ErrorBox error={parseError ?? retry.error} />
        <div className="row gap-sm end">
          <button type="button" className="btn" onClick={onClose}>Cancel</button>
          <button type="submit" className="btn btn-primary" disabled={retry.isPending || (mode === "from" && !fromRef)}>↻ Start retry</button>
        </div>
      </form>
    </Modal>
  );
}
