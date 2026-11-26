import { Link, useNavigate, useSearchParams } from "react-router-dom";
import type { RunPhase } from "../api/types";
import { ago, dateTime, span } from "../components/format";
import { Card, Empty, ErrorBox, Pager, PhaseBadge, useNow, usePaged } from "../components/ui";
import { useRuns, useWorkflows, useWorkflowVersions } from "./hooks";

const PHASES: RunPhase[] = ["Running", "Waiting", "Queued", "Paused", "Compensating", "Succeeded", "Failed", "TimedOut", "Cancelled", "Skipped"];

export function Runs() {
  const [params, setParams] = useSearchParams();
  const workflow = params.get("workflow") ?? "";
  const version = params.get("version") ?? "";
  const phase = params.get("phase") ?? "";
  const selected = new Set(phase ? phase.split(",") : []);
  const runs = useRuns({ workflow, version, phase, limit: 300 });
  const workflows = useWorkflows();
  const versions = useWorkflowVersions(workflow);
  const now = useNow(1000);
  const navigate = useNavigate();
  const { rows, pager } = usePaged(runs.data, `${workflow}|${version}|${phase}`);

  const set = (k: string, v: string) => {
    const next = new URLSearchParams(params);
    if (v) next.set(k, v);
    else next.delete(k);
    setParams(next, { replace: true });
  };
  const toggle = (p: string) => {
    const s = new Set(selected);
    if (s.has(p)) s.delete(p);
    else s.add(p);
    set("phase", [...s].join(","));
  };

  return (
    <>
      <div className="page-head">
        <div>
          <h1>Runs</h1>
          <div className="subtitle">Every workflow execution in this namespace</div>
        </div>
      </div>
      <div className="row gap-sm wrap">
        <select value={workflow} onChange={(e) => {
          const next = new URLSearchParams(params);
          if (e.target.value) next.set("workflow", e.target.value);
          else next.delete("workflow");
          next.delete("version");
          setParams(next, { replace: true });
        }} aria-label="Workflow">
          <option value="">All workflows</option>
          {(workflows.data ?? []).map((w) => <option key={w.metadata.name}>{w.metadata.name}</option>)}
        </select>
        {(workflow || version) && (
          <select value={version} onChange={(e) => set("version", e.target.value)} aria-label="Version">
            <option value="">All versions</option>
            {version && !versions.data?.some((v) => v.version === version) && <option value={version}>{version}</option>}
            {(versions.data ?? []).map((v) => (
              <option key={v.version} value={v.version}>{v.version}{v.current ? " (current)" : ""} - {v.runs.total} runs</option>
            ))}
          </select>
        )}
        <div className="chips" role="group" aria-label="Phase filter">
          {PHASES.map((p) => (
            <button key={p} className={`btn btn-sm ${selected.has(p) ? "btn-primary" : ""}`} onClick={() => toggle(p)} aria-pressed={selected.has(p)}>{p}</button>
          ))}
          {selected.size > 0 && <button className="btn btn-sm btn-ghost" onClick={() => set("phase", "")}>Clear</button>}
        </div>
      </div>
      <ErrorBox error={runs.error} />
      <Card className="flush">
        {runs.data?.length === 0 ? (
          <Empty>No runs match these filters.</Empty>
        ) : (
          <><div className="table-wrap">
            <table className="table">
              <thead>
                <tr><th>Run</th><th>Workflow</th><th>Version</th><th>Phase</th><th>Details</th><th>Trigger</th><th className="num">Duration</th><th className="num">Created</th></tr>
              </thead>
              <tbody>
                {rows.map((r) => (
                  <tr key={r.metadata.name} className="clickable" onClick={() => navigate(`/runs/${r.metadata.name}`)}>
                    <td><Link className="mono" to={`/runs/${r.metadata.name}`} onClick={(e) => e.stopPropagation()}>{r.metadata.name}</Link>{r.spec.retry && <span className="muted" title={`retry of ${r.spec.retry.of}`}> ↻</span>}</td>
                    <td>{r.spec.workflow}</td>
                    <td>
                      <Link className="mono muted" style={{ fontSize: 12 }} title="Only runs of this version" onClick={(e) => e.stopPropagation()}
                        to={`/runs?workflow=${encodeURIComponent(r.spec.workflow)}&version=${encodeURIComponent(r.spec.version)}`}>{r.spec.version}</Link>
                    </td>
                    <td>
                      <span className="row gap-sm">
                        <PhaseBadge phase={r.status.phase} />
                        {r.status.compensation && <span className="badge badge-outline" title="Compensation">↶ {r.status.compensation.toLowerCase()}</span>}
                      </span>
                    </td>
                    <td className="secondary" style={{ maxWidth: 380, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }} title={r.status.message}>
                      {r.status.message || (r.status.concurrencyKey ? <span className="muted">key {r.status.concurrencyKey}</span> : "")}
                    </td>
                    <td className="muted">{r.spec.triggeredBy ?? "—"}</td>
                    <td className="num">{span(r.status.startedAt, r.status.finishedAt, now)}</td>
                    <td className="num muted" title={dateTime(r.metadata.createdAt)}>{ago(r.metadata.createdAt, now)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <Pager {...pager} /></>
        )}
      </Card>
    </>
  );
}
