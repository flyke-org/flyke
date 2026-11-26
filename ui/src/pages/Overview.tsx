import { useMemo, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import type { Run, RunPhase } from "../api/types";
import { EmitDialog } from "../components/forms";
import { ago, span } from "../components/format";
import { Icon, type IconName } from "../components/icons";
import { Card, Empty, ErrorBox, PhaseBadge, useNow } from "../components/ui";
import { openWait, useRunHistory, useRuns, useWorkers } from "./hooks";

const DAY = 86_400_000;

type Tone = "info" | "waiting" | "warning" | "critical" | "good" | "accent";

function Tile({ label, value, sub, tone = "accent", icon, to, meter }: {
  label: string; value: number | string; sub?: string; tone?: Tone; icon: IconName; to?: string; meter?: number | null;
}) {
  const body = (
    <>
      <span className="tile-head">
        <span className="tile-icon"><Icon name={icon} /></span>
        <span className="tile-label">{label}</span>
      </span>
      <span className="tile-value">{value}</span>
      {sub && <span className="tile-sub">{sub}</span>}
      {meter != null && (
        <span className="tile-meter" role="presentation"><span style={{ width: `${Math.max(0, Math.min(100, meter))}%` }} /></span>
      )}
    </>
  );
  return to ? <Link className="tile" data-tone={tone} to={to}>{body}</Link> : <div className="tile" data-tone={tone}>{body}</div>;
}

export function Overview() {
  const runs = useRuns({ limit: 500 });
  const workers = useWorkers();
  const now = useNow(5000);
  const navigate = useNavigate();

  const stats = useMemo(() => {
    const all = runs.data ?? [];
    const recent = all.filter((r) => now - Date.parse(r.metadata.createdAt) < DAY);
    const count = (p: RunPhase[], list = all) => list.filter((r) => p.includes(r.status.phase)).length;
    const done = recent.filter((r) => r.status.finishedAt && r.status.phase !== "Skipped");
    const ok = count(["Succeeded"], done);
    return {
      running: count(["Running", "Pending", "Compensating"]),
      waiting: count(["Waiting"]),
      queued: count(["Queued"]),
      paused: count(["Paused"]),
      failed24: count(["Failed", "TimedOut"], recent),
      succeeded24: ok,
      rate: done.length ? Math.round((ok / done.length) * 100) : null,
    };
  }, [runs.data, now]);

  const ready = (workers.data ?? []).filter((w) => w.status.phase === "Ready").length;
  const totalSlots = (workers.data ?? []).filter((w) => w.status.phase === "Ready").reduce((a, w) => a + w.status.slots, 0);
  const busySlots = (workers.data ?? []).filter((w) => w.status.phase === "Ready").reduce((a, w) => a + w.status.slots - w.status.freeSlots, 0);
  const attention = (runs.data ?? []).filter((r) => r.status.phase === "Paused" || r.status.phase === "Waiting");

  return (
    <>
      <div className="page-head">
        <div>
          <h1>Overview</h1>
          <div className="subtitle">Live state of the control plane - refreshes every 2s</div>
        </div>
      </div>
      <ErrorBox error={runs.error} />
      <div className="tiles">
        <Tile label="Running" icon="runs" value={stats.running} tone="info" to="/runs?phase=Running,Pending,Compensating" sub="in progress now" />
        <Tile label="Waiting on events" icon="schedules" value={stats.waiting} tone="waiting" to="/runs?phase=Waiting" sub={stats.queued ? `${stats.queued} queued` : "none queued"} />
        <Tile label="Paused" icon="pause" value={stats.paused} tone="warning" to="/runs?phase=Paused" sub="needs a human" />
        <Tile label="Failed · 24h" icon="failed" value={stats.failed24} tone="critical" to="/runs?phase=Failed,TimedOut" sub="failed or timed out" />
        <Tile label="Succeeded · 24h" icon="check" value={stats.succeeded24} tone="good" to="/runs?phase=Succeeded"
          sub={stats.rate === null ? "no finished runs" : `${stats.rate}% success rate`} meter={stats.rate} />
        <Tile label="Workers ready" icon="workers" value={`${ready}/${workers.data?.length ?? 0}`} to="/workers"
          sub={`${busySlots}/${totalSlots} slots busy`} meter={totalSlots ? (busySlots / totalSlots) * 100 : 0} />
      </div>

      <div className="grid grid-2">
        <Card title={<>Needs attention{attention.length > 0 && <span className="count-pill">{attention.length}</span>}</>} className="flush">
          {attention.length === 0 ? (
            <Empty>Nothing is paused or waiting. 🎉</Empty>
          ) : (
            <div className="table-wrap">
              <table className="table">
                <tbody>{attention.map((r) => <AttentionRow key={r.metadata.name} run={r} />)}</tbody>
              </table>
            </div>
          )}
        </Card>

        <Card title="Recent runs" actions={<Link to="/runs">All runs →</Link>} className="flush">
          {runs.data && runs.data.length === 0 ? (
            <Empty>No runs yet. Start one from <Link to="/workflows">Workflows</Link> or with <code>flyke run</code>.</Empty>
          ) : (
            <div className="table-wrap">
              <table className="table">
                <tbody>
                  {(runs.data ?? []).slice(0, 10).map((r) => (
                    <tr key={r.metadata.name} className="clickable" onClick={() => navigate(`/runs/${r.metadata.name}`)}>
                      <td><Link to={`/runs/${r.metadata.name}`} className="mono" onClick={(e) => e.stopPropagation()}>{r.metadata.name}</Link></td>
                      <td><PhaseBadge phase={r.status.phase} /></td>
                      <td className="num muted">{span(r.status.startedAt, r.status.finishedAt, now)}</td>
                      <td className="num muted">{ago(r.metadata.createdAt, now)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Card>
      </div>
    </>
  );
}

function AttentionRow({ run }: { run: Run }) {
  const id = run.metadata.name;
  const history = useRunHistory(id, true);
  const wait = run.status.phase === "Waiting" ? openWait(history.data ?? []) : null;
  const [emitting, setEmitting] = useState(false);
  return (
    <tr>
      <td>
        <div className="stack" style={{ gap: 2 }}>
          <Link to={`/runs/${id}`} className="mono">{id}</Link>
          <span className="muted" style={{ fontSize: 12 }}>
            {wait ? <>waiting for <code>{wait.name}</code>{Object.keys(wait.where).length > 0 && <> where {Object.entries(wait.where).map(([k, v]) => `${k}=${JSON.stringify(v)}`).join(", ")}</>}</>
              : run.status.message}
          </span>
        </div>
      </td>
      <td><PhaseBadge phase={run.status.phase} /></td>
      <td className="num">
        {wait ? (
          <button className="btn btn-sm btn-primary btn-wrap" onClick={() => setEmitting(true)}>Emit {wait.name}</button>
        ) : (
          <Link className="btn btn-sm" to={`/runs/${id}`}>Resolve →</Link>
        )}
        {emitting && wait && <EmitDialog name={wait.name} fields={wait.where} onClose={() => setEmitting(false)} />}
      </td>
    </tr>
  );
}
