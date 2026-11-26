import type { ReactNode } from "react";
import type { GraphArg, GraphLane, GraphNode, GraphSource, Manifest, Run, TaskSpec } from "../api/types";
import { TERMINAL } from "../api/types";
import { END_ID, START_ID, STATUS_ICON, laneTaken, statusLabel, type Diagram, type NodeStatus, type RunOverlay } from "./diagram";
import { span } from "./format";

const KIND_ICONS: Record<string, string> = { task: "◆", wait: "◷", emit: "↗", sleep: "⏲", return: "⏎" };
const GROUP_TITLES: Record<string, string> = { parallel: "in parallel", branch: "branch", loop: "loop", try: "try / except", finally: "finally" };
const GUARANTEE: Record<TaskSpec["guarantee"], string> = { at_least_once: "at-least-once", at_most_once: "at-most-once", effectively_once: "effectively-once" };

interface Props {
  diagram: Diagram;
  manifest?: Manifest;
  /** When set, nodes are colored by what happened in this run. */
  run?: Run;
  overlay?: RunOverlay | null;
  selected: string | null;
  onSelect: (id: string) => void;
  now?: number;
}

interface Ctx {
  manifest?: Manifest;
  overlay: RunOverlay | null;
  selected: string | null;
  /** Nodes the selected node takes its values from. */
  feeds: Set<string>;
  onSelect: (id: string) => void;
  run?: Run;
  now: number;
}

/** Ids of the nodes (and "start" for inputs) that feed a node's arguments. */
export function feedsOf(node: GraphNode | undefined, output?: Diagram["output"]): Set<string> {
  const sources: GraphSource[] = node ? [...(node.args ?? []).flatMap((a) => a.sources), ...(node.sources ?? [])] : output?.sources ?? [];
  return new Set(sources.map((s) => (s.kind === "input" ? START_ID : s.node ?? "")).filter(Boolean));
}

function compact(v: unknown): string {
  if (v === undefined) return "—";
  const s = JSON.stringify(v);
  return s === undefined ? String(v) : s;
}

/** A bounded one-glance rendering of a value; the inspector shows it in full. */
function preview(v: unknown, max: number): string {
  const s = typeof v === "string" ? v : compact(v);
  return s.length > max ? `${s.slice(0, max)}…` : s;
}

/**
 * The workflow as a top-to-bottom flow: inputs, then every durable step in
 * call order (parallel blocks side by side, branches as alternative lanes),
 * then the output. Each step lists where its arguments come from.
 */
export function FlowDiagram({ diagram, manifest, run, overlay = null, selected, onSelect, now = Date.now() }: Props) {
  const nodes = [...flatten(diagram.tree), ...(overlay?.dynamic ?? [])];
  const sel = nodes.find((n) => n.id === selected);
  const feeds = selected === END_ID ? feedsOf(undefined, diagram.output) : feedsOf(sel);
  const ctx: Ctx = { manifest, overlay, selected, feeds, onSelect, run, now };
  const dynamic = overlay?.dynamic ?? [];
  const finished = run ? TERMINAL.includes(run.status.phase) : false;
  const inputs = (run?.spec.inputs ?? {}) as Record<string, unknown>;

  return (
    <div className="flow-canvas">
      <div className="flow" role="group" aria-label="Workflow diagram">
        <div className="flow-seq">
          <Terminal id={START_ID} ctx={ctx} title="Inputs" status={run ? "succeeded" : null}>
            {diagram.inputs.length === 0 ? (
              <span className="muted">no inputs</span>
            ) : (
              <ul className="flow-io">
                {diagram.inputs.map((input) => (
                  <li key={input.name}>
                    <span className="mono">{input.name}{input.required && !run && <span className="required">*</span>}</span>
                    {run ? (
                      <span className="flow-value mono" title={compact(inputs[input.name])}>{compact(inputs[input.name])}</span>
                    ) : (
                      <span className="flow-type flow-clip" title={input.type}>{input.type}</span>
                    )}
                  </li>
                ))}
              </ul>
            )}
          </Terminal>

          {diagram.known && <Sequence nodes={diagram.tree} ctx={ctx} />}

          {dynamic.length > 0 && (diagram.known ? (
            <>
              <Edge />
              <div className="flow-group flow-group-dynamic" title="These steps ran but are not visible in run()'s source">
                <div className="flow-group-title">called at run time</div>
                <div className="flow-lanes"><Lane lane={{ label: null, nodes: dynamic }} ctx={ctx} /></div>
              </div>
            </>
          ) : (
            <Sequence nodes={dynamic} ctx={ctx} />
          ))}
          {!run && diagram.dynamicTasks.length > 0 && (
            <>
              <Edge />
              <div className="flow-note">Chosen at run time: {diagram.dynamicTasks.map((t) => <span key={t} className="chip">{t}</span>)}</div>
            </>
          )}

          <Edge />
          <Terminal id={END_ID} ctx={ctx} title="Output" status={run ? (finished ? endStatus(run) : "pending") : null}>
            {run ? (
              run.status.phase === "Succeeded" ? (
                <span className="flow-result mono" title={preview(run.status.result ?? null, 1200)}>{preview(run.status.result ?? null, 400)}</span>
              ) : run.status.failure ? (
                <span className="flow-error flow-result" title={run.status.failure.message}>{run.status.failure.message}</span>
              ) : (
                <span className="muted">{finished ? run.status.phase.toLowerCase() : "not finished"}</span>
              )
            ) : (
              <span className="flow-ports">
                <span className="flow-type">{diagram.output.type}</span>
                {diagram.output.sources.length > 0 && <span className="flow-arg-src">← {diagram.output.sources.map(sourceText).join(", ")}</span>}
              </span>
            )}
          </Terminal>
        </div>
      </div>
    </div>
  );
}

function flatten(nodes: GraphNode[]): GraphNode[] {
  return nodes.flatMap((n) => [n, ...(n.lanes ?? []).flatMap((l) => flatten(l.nodes))]);
}

function endStatus(run: Run): NodeStatus {
  const p = run.status.phase;
  if (p === "Succeeded") return run.status.compensation ? "compensated" : "succeeded";
  if (p === "Cancelled" || p === "Skipped") return "cancelled";
  return "failed";
}

function Sequence({ nodes, ctx }: { nodes: GraphNode[]; ctx: Ctx }) {
  return (
    <>
      {nodes.map((n) => (
        <div key={n.id} className="flow-step">
          <Edge />
          {n.lanes?.length ? <Container node={n} ctx={ctx} /> : <StepNode node={n} ctx={ctx} />}
        </div>
      ))}
    </>
  );
}

function Edge() {
  return <div className="flow-edge" aria-hidden />;
}

function cls(base: string, status: NodeStatus | null | undefined, ctx: Ctx, id: string): string {
  return [base, status ? `is-${status}` : "", ctx.selected === id ? "selected" : "", ctx.feeds.has(id) ? "feeds" : ""].join(" ");
}

function Terminal({ id, title, ctx, status, children }: { id: string; title: string; ctx: Ctx; status: NodeStatus | null; children: ReactNode }) {
  return (
    <button type="button" className={cls("flow-node flow-terminal", status, ctx, id)} onClick={() => ctx.onSelect(id)} aria-pressed={ctx.selected === id}>
      <span className="flow-node-head"><span className="flow-kind">{title}</span></span>
      <span className="flow-node-body">{children}</span>
    </button>
  );
}

function Container({ node, ctx }: { node: GraphNode; ctx: Ctx }) {
  const status = ctx.overlay?.byNode.get(node.id)?.status;
  const title = node.kind === "loop" ? node.label : GROUP_TITLES[node.kind] ?? node.kind;
  const single = node.kind === "loop" || node.kind === "finally";
  return (
    <div className={`flow-group flow-group-${node.kind} ${status ? `is-${status}` : ""}`}>
      <button type="button" className={`flow-group-title ${ctx.selected === node.id ? "selected" : ""}`} onClick={() => ctx.onSelect(node.id)}>
        <span className="flow-group-kind">{title}</span>
        {node.line && <span className="flow-line">line {node.line}</span>}
      </button>
      <div className={`flow-lanes ${single ? "single" : ""}`}>
        {(node.lanes ?? []).map((lane, i) => <Lane key={i} lane={lane} ctx={ctx} showLabel={!single} />)}
      </div>
    </div>
  );
}

function Lane({ lane, ctx, showLabel = true }: { lane: GraphLane; ctx: Ctx; showLabel?: boolean }) {
  const taken = laneTaken(lane, ctx.overlay);
  const runs = lane.fan_out && ctx.overlay ? Math.max(0, ...lane.nodes.map((n) => ctx.overlay?.byNode.get(n.id)?.execs.length ?? 0)) : null;
  return (
    <div className={`flow-lane ${taken === false ? "not-taken" : ""} ${lane.fan_out ? "fan-out" : ""}`}>
      {showLabel && lane.label && (
        <div className="flow-lane-label mono" title={lane.label}>
          {lane.label}
          {runs !== null && runs > 0 && <span className="count">×{runs}</span>}
        </div>
      )}
      <div className="flow-seq">
        {lane.nodes.length ? <Sequence nodes={lane.nodes} ctx={ctx} /> : <><Edge /><div className="flow-skip">nothing durable</div></>}
      </div>
    </div>
  );
}

function StepNode({ node, ctx }: { node: GraphNode; ctx: Ctx }) {
  const nodeRun = ctx.overlay?.byNode.get(node.id);
  const status = nodeRun?.status;
  const spec = node.task ? ctx.manifest?.tasks[node.task] : undefined;
  const sig = node.task ? ctx.manifest?.graph?.tasks?.[node.task] : undefined;
  const execs = nodeRun?.execs ?? [];
  const retries = execs.reduce((n, e) => n + Math.max(0, (e.task?.attempt ?? 1) - 1), 0);
  const starts = execs.map((e) => e.task?.startedAt ?? e.start.time);
  const ends = execs.map((e) => e.task?.finishedAt ?? e.outcome?.time);
  const allDone = execs.length > 0 && ends.every(Boolean);
  const took = execs.length && !execs.every((e) => e.status === "reused")
    ? span(starts.sort()[0], allDone ? (ends as string[]).sort().reverse()[0] : undefined, ctx.now)
    : null;
  const args = (node.args ?? []).filter((a) => !((node.kind === "wait" || node.kind === "emit") && a.name === "event"));
  const undo = execs.filter((e) => e.compensation);
  const label = node.kind === "sleep" && node.label === "sleep" && node.args?.[0] ? `sleep ${node.args[0].expr}` : node.label;

  return (
    <button
      type="button"
      className={cls(`flow-node flow-node-${node.kind}`, status, ctx, node.id)}
      onClick={() => ctx.onSelect(node.id)}
      aria-pressed={ctx.selected === node.id}
      title={status ? statusLabel(status) : undefined}
    >
      <span className="flow-node-head">
        <span className="flow-kind">{KIND_ICONS[node.kind]} {node.kind}</span>
        <span className="flow-name mono">{node.kind === "sleep" ? label.replace(/^sleep\s*/, "") || "sleep" : label}</span>
        {spec && spec.guarantee !== "at_least_once" && <span className={`chip guarantee-${spec.guarantee}`}>{GUARANTEE[spec.guarantee]}</span>}
      </span>

      {node.kind !== "return" && node.kind !== "sleep" && (args.length > 0 || (sig?.params.length ?? 0) > 0) && (
        <span className="flow-ports">
          <span className="flow-port-label">in</span>
          <span className="flow-port-values">
            {args.length ? args.map((a) => <ArgChip key={a.name} arg={a} />) : <span className="muted">—</span>}
          </span>
        </span>
      )}
      {node.kind === "task" && (
        <span className="flow-ports">
          <span className="flow-port-label">out</span>
          <span className="flow-port-values">
            <span className="flow-type">{sig?.returns ?? "Any"}</span>
            {node.assign && <span className="flow-assign mono">→ {node.assign}</span>}
          </span>
        </span>
      )}
      {node.kind === "wait" && node.assign && (
        <span className="flow-ports">
          <span className="flow-port-label">out</span>
          <span className="flow-port-values"><span className="flow-type">Event</span> <span className="flow-assign mono">→ {node.assign}</span></span>
        </span>
      )}
      {node.kind === "return" && <span className="flow-ports mono flow-clip">{node.expr}</span>}

      {status && (
        <span className="flow-run">
          <span className={`flow-status ${status === "running" ? "pulse" : ""}`}>{STATUS_ICON[status]} {statusLabel(status)}</span>
          {execs.length > 1 && <span className="muted">×{execs.length}</span>}
          {retries > 0 && <span className="chip chip-warn">{retries} retr{retries === 1 ? "y" : "ies"}</span>}
          {took && <span className="muted mono flow-took">{took}</span>}
        </span>
      )}
      {spec?.compensation && (
        <span className={`flow-undo ${undo.length ? (undo.some((e) => e.compensation?.phase === "Failed") ? "is-failed" : "is-ran") : ""}`}>
          ↶ undo: <span className="mono">{spec.compensation}</span>
          {undo.length > 0 && <strong> ran ×{undo.length}</strong>}
        </span>
      )}
    </button>
  );
}

function ArgChip({ arg }: { arg: GraphArg }) {
  return (
    <span className="flow-arg" title={`${arg.name} = ${arg.expr}`}>
      <span className="mono">{arg.name}</span>
      {arg.sources.length > 0 && <span className="flow-arg-src">← {arg.sources.map(sourceText).join(", ")}</span>}
    </span>
  );
}

export function sourceText(s: GraphSource): string {
  return s.kind === "input" ? `input ${s.name}` : s.task ?? "wait";
}
