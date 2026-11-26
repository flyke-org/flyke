import { useState } from "react";
import { Link, useParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useConnection, useScope } from "../api/context";
import type { RunPhase, TaskSpec, Trigger, Workflow, WorkflowVersion } from "../api/types";
import { StartRunForm } from "../components/forms";
import { WorkflowFlow } from "../components/RunFlow";
import { ago, bytes, dateTime, duration, shortType } from "../components/format";
import { Card, Confirm, CopyButton, Empty, ErrorBox, Json, Labels, Modal, Pager, PhaseBadge, runStatus, usePaged } from "../components/ui";
import { useRuns, useWorkflows, useWorkflowVersions } from "./hooks";

const GUARANTEE: Record<TaskSpec["guarantee"], string> = {
  at_least_once: "at-least-once",
  at_most_once: "at-most-once",
  effectively_once: "effectively-once",
};

function triggerText(t: Trigger): string {
  if (t.type === "schedule") return `⏲ ${t.cron}${t.tz ? ` (${t.tz})` : ""}`;
  const where = t.where ? Object.entries(t.where).map(([k, v]) => `${k}=${JSON.stringify(v)}`).join(", ") : "";
  return `⚡ ${t.event}${where ? ` where ${where}` : ""}`;
}

function source(wf: Workflow): string {
  return wf.spec.source.bundleDigest ? `bundle ${wf.spec.source.bundleDigest.slice(7, 19)}` : "local code";
}

export function Workflows() {
  const workflows = useWorkflows();
  const [starting, setStarting] = useState<Workflow | null>(null);
  return (
    <>
      <div className="page-head">
        <div>
          <h1>Workflows</h1>
          <div className="subtitle">Registered with <code>flyke deploy</code> or <code>flyke run</code></div>
        </div>
      </div>
      <ErrorBox error={workflows.error} />
      {workflows.data?.length === 0 && (
        <Card><Empty>No workflows registered yet. Try <code>flyke run examples/deploy.py:DeployService --service=api</code>.</Empty></Card>
      )}
      <div className="grid grid-2">
        {(workflows.data ?? []).map((wf) => {
          const m = wf.spec.manifest;
          return (
            <Card key={wf.metadata.name}
              title={<Link to={`/workflows/${wf.metadata.name}`}>{wf.metadata.name}</Link>}
              actions={<button className="btn btn-sm btn-primary" onClick={() => setStarting(wf)}>▶ Run</button>}>
              <div className="stack" style={{ gap: 8 }}>
                {m.description && <p className="secondary" style={{ margin: 0 }}>{m.description}</p>}
                <div className="row gap-sm wrap">
                  {Object.entries(m.tasks).map(([n, t]) => (
                    <span key={n} className="chip" title={GUARANTEE[t.guarantee]}>{n}{t.compensation ? " ↶" : ""}</span>
                  ))}
                </div>
                <div className="muted" style={{ fontSize: 12 }}>
                  {[wf.status.version, source(wf), ...(m.triggers ?? []).map(triggerText), `registered ${ago(wf.metadata.createdAt)}`].join(" - ")}
                </div>
              </div>
            </Card>
          );
        })}
      </div>
      {starting && (
        <Modal title={`Run ${starting.metadata.name}`} onClose={() => setStarting(null)}>
          <StartRunForm workflow={starting} onDone={() => setStarting(null)} />
        </Modal>
      )}
    </>
  );
}

export function WorkflowDetail() {
  const { name = "" } = useParams();
  const { client } = useConnection();
  const scope = useScope();
  const wf = useQuery({ queryKey: [scope, "workflow", name], queryFn: () => client.getWorkflow(name) });
  const runs = useRuns({ workflow: name, limit: 20 });

  if (wf.error) return <ErrorBox error={wf.error} />;
  if (!wf.data) return <Empty>Loading…</Empty>;
  const m = wf.data.spec.manifest;

  return (
    <>
      <div className="page-head">
        <div className="stack" style={{ gap: 6 }}>
          <div className="muted" style={{ fontSize: 12.5 }}><Link to="/workflows">Workflows</Link></div>
          <h1>{m.name}</h1>
          {m.description && <div className="subtitle">{m.description}</div>}
        </div>
        <Link className="btn" to={`/schedules?new=${encodeURIComponent(m.name)}`}>⏲ Schedule…</Link>
      </div>
      <div className="grid grid-2">
        <Card title="Definition">
          <dl className="kv">
            <dt>Version</dt>
            <dd>
              <span className="mono">{wf.data.status.version}</span>
              {wf.data.status.activatedAt && <span className="muted" title={dateTime(wf.data.status.activatedAt)}> - current since {ago(wf.data.status.activatedAt)}</span>}
              {" "}<a href="#versions">all versions ↓</a>
            </dd>
            <dt>Entrypoint</dt><dd className="mono">{m.entrypoint}</dd>
            <dt>Code</dt><dd>{source(wf.data)}</dd>
            <dt>Runtime</dt><dd>{m.runtime}</dd>
            <dt>Triggers</dt><dd>{m.triggers?.length ? m.triggers.map((t, i) => <div key={i}>{triggerText(t)}</div>) : "manual / API"}</dd>
            {m.concurrency && <><dt>Concurrency</dt><dd><code>{m.concurrency.key}</code> - limit {m.concurrency.limit} - on conflict {m.concurrency.on_conflict}</dd></>}
            {m.timeout_ms ? <><dt>Timeout</dt><dd>{duration(m.timeout_ms)}</dd></> : null}
            {m.resources && <><dt>Resources</dt><dd>{m.resources.profile ?? `${m.resources.cpu ?? 0} cpu - ${bytes(m.resources.memory_bytes)}`}</dd></>}
            <dt>Labels</dt><dd><Labels labels={m.labels} /></dd>
          </dl>
        </Card>
        <Card title="Start a run"><StartRunForm workflow={wf.data} /></Card>
      </div>
      <VersionsCard name={name} />
      <Card title="Diagram"><WorkflowFlow manifest={m} /></Card>
      <Card title="Tasks" className="flush">
        <div className="table-wrap">
          <table className="table">
            <thead><tr><th>Task</th><th>Guarantee</th><th>Retry</th><th>Timeout</th><th>Resources</th><th>Idempotency key</th><th>Compensation</th></tr></thead>
            <tbody>
              {Object.entries(m.tasks).map(([n, t]) => (
                <tr key={n}>
                  <td className="mono">{n}</td>
                  <td>
                    <span className="badge badge-outline">{GUARANTEE[t.guarantee]}</span>
                    {t.on_unknown && t.on_unknown !== "retry" && <span className="muted" style={{ fontSize: 12 }}> - unknown → {t.on_unknown}</span>}
                  </td>
                  <td className="secondary">
                    {t.retry ? `${t.retry.attempts}× ${t.retry.backoff} ${duration(t.retry.delay_ms)}${t.retry.on?.length ? ` on ${t.retry.on.map(shortType).join(", ")}` : ""}` : "default (3× exp 1s)"}
                  </td>
                  <td>{t.timeout_ms ? duration(t.timeout_ms) : "—"}</td>
                  <td>{t.resources?.profile ? <span className="chip">{t.resources.profile}</span> : t.resources ? `${t.resources.cpu ?? 0} cpu` : "—"}</td>
                  <td className="mono secondary">{t.idempotency_key ?? "—"}</td>
                  <td className="mono">{t.compensation ? `↶ ${t.compensation}` : "—"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Card>
      <div className="grid grid-2">
        <Card title="Recent runs" actions={<Link to={`/runs?workflow=${name}`}>All →</Link>} className="flush">
          {runs.data?.length === 0 ? <Empty>No runs yet.</Empty> : (
            <table className="table"><tbody>
              {(runs.data ?? []).map((r) => (
                <tr key={r.metadata.name}>
                  <td><Link className="mono" to={`/runs/${r.metadata.name}`}>{r.metadata.name}</Link></td>
                  <td className="mono muted" style={{ fontSize: 12 }}>{r.spec.version}</td>
                  <td><PhaseBadge phase={r.status.phase} /></td>
                  <td className="num muted">{ago(r.metadata.createdAt)}</td>
                </tr>
              ))}
            </tbody></table>
          )}
        </Card>
        <Card title="Inputs schema"><Json value={m.inputs_schema} /></Card>
      </div>
    </>
  );
}

const OUTCOMES: RunPhase[] = ["Succeeded", "Failed", "TimedOut", "Cancelled", "Running", "Waiting", "Queued", "Paused", "Compensating", "Skipped"];

function Outcomes({ phases }: { phases: WorkflowVersion["runs"]["phases"] }) {
  const shown = OUTCOMES.filter((p) => phases[p]);
  if (!shown.length) return <span className="muted">—</span>;
  return (
    <span className="row gap-sm wrap">
      {shown.map((p) => <span key={p} className={`badge badge-${runStatus(p)}`} title={p}>{p === "Succeeded" ? "✓" : p === "Failed" ? "✕" : p} {phases[p]}</span>)}
    </span>
  );
}

function codeText(v: WorkflowVersion): string {
  return v.bundle ? `bundle ${v.bundle.digest.slice(7, 19)}` : `local ${v.source.localRoot ?? ""}`;
}

/** Every registered version: when it was made, what code it ships, which runs used it. */
function VersionsCard({ name }: { name: string }) {
  const { client } = useConnection();
  const scope = useScope();
  const qc = useQueryClient();
  const versions = useWorkflowVersions(name);
  const [activating, setActivating] = useState<WorkflowVersion | null>(null);
  const [running, setRunning] = useState<WorkflowVersion | null>(null);
  const activate = useMutation({
    mutationFn: (v: string) => client.activateWorkflowVersion(name, v),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: [scope, "workflow", name] });
      qc.invalidateQueries({ queryKey: [scope, "workflow-versions", name] });
      qc.invalidateQueries({ queryKey: [scope, "workflows"] });
    },
  });
  const list = versions.data ?? [];
  const current = list.find((v) => v.current);
  const previous = list.find((v) => !v.current);
  const { rows, pager } = usePaged(list, undefined, 10);

  return (
    <div id="versions">
      <Card title={`Versions${list.length ? ` (${list.length})` : ""}`} className="flush"
        actions={previous && <button className="btn btn-sm" onClick={() => setActivating(previous)} title={`Make ${previous.version} current again`}>↶ Roll back</button>}>
        <ErrorBox error={versions.error ?? activate.error} />
        {list.length === 0 ? <Empty>{versions.isLoading ? "Loading…" : "No versions."}</Empty> : (
          <><div className="table-wrap">
            <table className="table">
              <thead>
                <tr><th>Version</th><th>Code</th><th>Registered</th><th className="num">Activated</th><th className="num">Runs</th><th>Outcomes</th><th>Last run</th><th className="actions">Actions</th></tr>
              </thead>
              <tbody>
                {rows.map((v) => (
                  <tr key={v.version}>
                    <td>
                      <span className="row gap-sm">
                        <span className="mono">{v.version}</span>
                        {v.current && <span className="badge badge-good">current</span>}
                      </span>
                    </td>
                    <td className="secondary">
                      <span className="mono" title={v.bundle?.digest ?? v.source.localRoot}>{codeText(v)}</span>
                      {v.bundle?.size ? <span className="muted"> - {bytes(v.bundle.size)}</span> : null}
                      {v.bundle && <CopyButton text={v.bundle.digest} label="⧉" />}
                    </td>
                    <td className="muted" title={dateTime(v.metadata.createdAt)}>{dateTime(v.metadata.createdAt)}</td>
                    <td className="num muted" title={dateTime(v.activatedAt)}>{ago(v.activatedAt)}</td>
                    <td className="num">
                      {v.runs.total > 0
                        ? <Link to={`/runs?workflow=${encodeURIComponent(name)}&version=${encodeURIComponent(v.version)}`}>{v.runs.total} →</Link>
                        : <span className="muted">0</span>}
                    </td>
                    <td><Outcomes phases={v.runs.phases} /></td>
                    <td className="muted" title={v.runs.lastRunAt ? dateTime(v.runs.lastRunAt) : undefined}>{v.runs.lastRunAt ? ago(v.runs.lastRunAt) : "—"}</td>
                    <td className="actions">
                      <span className="row gap-sm end">
                        <button className="btn btn-sm btn-ghost" onClick={() => setRunning(v)} title="Start a run of this exact version">▶ Run</button>
                        {!v.current && (
                          <button className="btn btn-sm" disabled={activate.isPending} onClick={() => setActivating(v)}>Make current</button>
                        )}
                      </span>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <Pager {...pager} /></>
        )}
      </Card>
      {activating && (
        <Confirm title={`Make ${activating.version} current?`} action="Make current"
          body={
            <div className="stack" style={{ gap: 8 }}>
              <div>
                <span className="mono">{name}</span> goes from <span className="mono">{current?.version}</span> to{" "}
                <span className="mono">{activating.version}</span> ({codeText(activating)}, registered {ago(activating.metadata.createdAt)} ago).
              </div>
              <div className="muted" style={{ fontSize: 12.5 }}>
                New runs, event triggers and schedules declared in code use it from now on. Runs already started keep their version.
                You can switch back at any time.
              </div>
            </div>
          }
          onConfirm={() => activate.mutate(activating.version)} onClose={() => setActivating(null)} />
      )}
      {running && (
        <Modal title={`Run ${name} @ ${running.version}`} onClose={() => setRunning(null)}>
          {!running.current && <p className="muted" style={{ marginTop: 0 }}>Runs this older version once; the current version stays {current?.version}.</p>}
          <StartRunForm workflow={{ metadata: running.metadata, spec: { manifest: running.manifest } }}
            version={running.current ? undefined : running.version} onDone={() => setRunning(null)} />
        </Modal>
      )}
    </div>
  );
}
