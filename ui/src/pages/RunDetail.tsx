import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useConnection, useScope } from "../api/context";
import type { Run, TaskRun } from "../api/types";
import { EmitDialog } from "../components/forms";
import { clock, dateTime, shortType, span } from "../components/format";
import { LogViewer } from "../components/LogViewer";
import { RetryDialog, retryLabel, RunFlow, type RetryIntent } from "../components/RunFlow";
import { Timeline } from "../components/Timeline";
import { Card, Confirm, Empty, ErrorBox, Json, Labels, Modal, Pager, PhaseBadge, TaskBadge, useNow, usePaged } from "../components/ui";
import { isLive, openWait, POLL, useRunHistory, useRuns } from "./hooks";

type Tab = "logs" | "tasks" | "history" | "data";

export function RunDetail() {
  const { id = "" } = useParams();
  const { client } = useConnection();
  const scope = useScope();
  const qc = useQueryClient();
  const [tab, setTab] = useState<Tab>("logs");
  const [confirmCancel, setConfirmCancel] = useState(false);
  const [retrying, setRetrying] = useState<RetryIntent | null>(null);
  const [logSource, setLogSource] = useState("all");
  const navigate = useNavigate();
  const now = useNow(1000);

  const run = useQuery({
    queryKey: [scope, "run", id],
    queryFn: () => client.getRun(id),
    refetchInterval: (q) => (isLive(q.state.data) ? POLL : false),
  });
  const live = isLive(run.data);
  const tasks = useQuery({
    queryKey: [scope, "run", id, "tasks"],
    queryFn: () => client.runTasks(id),
    refetchInterval: live ? 1000 : false,
  });
  const history = useRunHistory(id, live);
  const workflowName = run.data?.spec.workflow;
  const workflow = useQuery({
    queryKey: [scope, "workflow", workflowName],
    queryFn: () => client.getWorkflow(workflowName!),
    enabled: !!workflowName,
    staleTime: 10_000,
  });
  const retries = useRuns({ retryOf: id, limit: 20 });

  const invalidate = () => qc.invalidateQueries({ queryKey: [scope, "run", id] });
  const cancel = useMutation({ mutationFn: () => client.cancelRun(id), onSuccess: invalidate });
  const resume = useMutation({ mutationFn: () => client.resumeRun(id), onSuccess: invalidate });
  const quickRetry = useMutation({
    mutationFn: (intent: RetryIntent) => client.retryRun(id, { mode: intent.mode, fromRef: intent.fromRef }),
    onSuccess: (n) => {
      qc.invalidateQueries({ queryKey: [scope, "runs"] });
      navigate(`/runs/${n.metadata.name}`);
    },
  });

  if (run.error) return <ErrorBox error={run.error} />;
  if (!run.data) return <Empty>Loading…</Empty>;
  const r = run.data;
  const st = r.status;
  const unknown = (tasks.data ?? []).filter((t) => t.phase === "Unknown");
  const wait = st.phase === "Waiting" ? openWait(history.data ?? []) : null;
  const finished = !live;
  // The diagram may only draw not-yet-run steps from the version this run executes.
  const wf = workflow.data;
  const manifest = wf ? (wf.status.version === r.spec.version ? wf.spec.manifest : { ...wf.spec.manifest, graph: undefined }) : undefined;
  const failedTask = (tasks.data ?? []).find((t) => t.kind === "task" && (t.phase === "Failed" || t.phase === "Cancelled"));
  const showLogs = (task: string) => {
    setLogSource(task);
    setTab("logs");
    requestAnimationFrame(() => document.getElementById("run-tabs")?.scrollIntoView({ behavior: "smooth", block: "start" }));
  };

  return (
    <>
      <div className="page-head">
        <div className="stack" style={{ gap: 6 }}>
          <div className="muted" style={{ fontSize: 12.5 }}><Link to="/runs">Runs</Link> / <Link to={`/workflows/${r.spec.workflow}`}>{r.spec.workflow}</Link></div>
          <div className="row gap-sm wrap">
            <h1 className="mono">{r.metadata.name}</h1>
            <PhaseBadge phase={st.phase} />
            {st.compensation && <span className="badge badge-outline">↶ compensation {st.compensation.toLowerCase()}</span>}
          </div>
          {(r.spec.retry || (retries.data?.length ?? 0) > 0) && (
            <div className="lineage">
              {r.spec.retry && <span>↻ Retry of <Link className="mono" to={`/runs/${r.spec.retry.of}`}>{r.spec.retry.of}</Link> - {retryLabel(r)}</span>}
              {(retries.data?.length ?? 0) > 0 && (
                <span>{r.spec.retry ? " - " : ""}Retried as {retries.data!.map((x, i) => (
                  <span key={x.metadata.name}>{i > 0 && ", "}<Link className="mono" to={`/runs/${x.metadata.name}`}>{x.metadata.name}</Link> <span className="muted">({x.status.phase.toLowerCase()})</span></span>
                ))}</span>
              )}
            </div>
          )}
        </div>
        <div className="row gap-sm">
          {finished && (
            <button className={`btn ${st.phase === "Succeeded" ? "" : "btn-primary"}`} onClick={() => setRetrying({ mode: st.phase === "Succeeded" ? "all" : "failed" })}>↻ Retry…</button>
          )}
          {st.phase === "Paused" && unknown.length === 0 && (
            <button className="btn btn-primary" onClick={() => resume.mutate()} disabled={resume.isPending}>▶ Resume</button>
          )}
          {live && st.phase !== "Compensating" && (
            <button className="btn btn-danger" onClick={() => setConfirmCancel(true)}>Cancel run</button>
          )}
        </div>
      </div>
      <ErrorBox error={cancel.error ?? resume.error ?? quickRetry.error} />
      {retrying && (
        <RetryDialog run={r} history={history.data ?? []} tasks={tasks.data ?? []} latestVersion={wf?.status.version} initial={retrying} onClose={() => setRetrying(null)} />
      )}
      {finished && st.phase !== "Succeeded" && st.phase !== "Skipped" && (
        <div className="alert alert-critical" role="status">
          <strong>✕ {st.phase}</strong>
          <span className="grow">
            {failedTask ? <><code>{failedTask.name}</code> (#{failedTask.ref}) {failedTask.phase === "Failed" ? "failed" : "was cancelled"}{failedTask.failure ? <>: {shortType(failedTask.failure.type)} — {failedTask.failure.message}</> : null}.</> : st.message}
            <span className="muted"> Fix the cause, then retry: finished steps are reused.</span>
          </span>
          <div className="row gap-sm wrap">
            <button className="btn btn-primary btn-sm" disabled={quickRetry.isPending} onClick={() => quickRetry.mutate({ mode: "failed" })}>↻ Retry failed steps</button>
            {failedTask && <button className="btn btn-sm" disabled={quickRetry.isPending} onClick={() => setRetrying({ mode: "from", fromRef: failedTask.ref })}>Retry from {failedTask.name}…</button>}
            <button className="btn btn-sm" onClick={() => setRetrying({ mode: "from" })}>Other options…</button>
          </div>
        </div>
      )}
      {confirmCancel && (
        <Confirm
          title="Cancel this run?" action="Cancel run" danger
          body={<>Running tasks are stopped. Completed tasks with a compensation (e.g. <code>provision → deprovision</code>) are undone in reverse order.</>}
          onConfirm={() => cancel.mutate()} onClose={() => setConfirmCancel(false)}
        />
      )}

      <Banners run={r} unknown={unknown} wait={wait} />

      <div className="grid grid-2">
        <Card title="Run">
          <dl className="kv">
            <dt>Workflow</dt><dd><Link to={`/workflows/${r.spec.workflow}`}>{r.spec.workflow}</Link> <span className="muted mono">{r.spec.version}</span></dd>
            <dt>Triggered by</dt><dd>{r.spec.triggeredBy ?? "—"}</dd>
            <dt>Created</dt><dd>{dateTime(r.metadata.createdAt)}</dd>
            <dt>Started</dt><dd>{dateTime(st.startedAt)}</dd>
            <dt>Finished</dt><dd>{dateTime(st.finishedAt)}</dd>
            <dt>Duration</dt><dd className="tabular">{span(st.startedAt, st.finishedAt, now)}</dd>
            {st.concurrencyKey && <><dt>Concurrency key</dt><dd className="mono">{st.concurrencyKey}</dd></>}
            <dt>Labels</dt><dd><Labels labels={r.metadata.labels} /></dd>
          </dl>
        </Card>
        <Card title="Status">
          <dl className="kv">
            <dt>Phase</dt><dd><PhaseBadge phase={st.phase} /></dd>
            <dt>Reason</dt><dd>{st.reason ?? "—"}</dd>
            {st.message && <><dt>Message</dt><dd>{st.message}</dd></>}
            <dt>History</dt><dd className="tabular">{st.historyLength} events</dd>
            <dt>Tasks</dt><dd className="tabular">{(tasks.data ?? []).filter((t) => t.kind === "task").length} task runs - {(tasks.data ?? []).filter((t) => t.kind === "compensation").length} compensations</dd>
          </dl>
          {st.failure && (
            <div className="stack" style={{ marginTop: 12, gap: 6 }}>
              <strong style={{ color: "var(--critical-ink)" }}>✕ {shortType(st.failure.type)}</strong>
              <span>{st.failure.message}</span>
            </div>
          )}
        </Card>
      </div>

      <Card title="Diagram">
        <RunFlow
          run={r} history={history.data ?? []} tasks={tasks.data ?? []} manifest={manifest} now={now}
          onRetry={setRetrying} onShowLogs={showLogs}
        />
      </Card>

      <Card title="Timeline">
        <Timeline run={r} tasks={tasks.data ?? []} history={history.data ?? []} />
      </Card>

      <Card>
        <div className="tabs" role="tablist" id="run-tabs" style={{ scrollMarginTop: 16 }}>
          {(["logs", "tasks", "history", "data"] as Tab[]).map((t) => (
            <button key={t} role="tab" aria-selected={tab === t} className={tab === t ? "on" : ""} onClick={() => setTab(t)}>
              {t[0].toUpperCase() + t.slice(1)}
              {t === "tasks" && <span className="count">{tasks.data?.length ?? 0}</span>}
              {t === "history" && <span className="count">{history.data?.length ?? 0}</span>}
            </button>
          ))}
        </div>
        <div style={{ paddingTop: 14 }}>
          {tab === "logs" && <LogViewer run={r} source={logSource} onSourceChange={setLogSource} />}
          {tab === "tasks" && <TasksTable runId={id} tasks={tasks.data ?? []} now={now} />}
          {tab === "history" && <HistoryTable events={history.data ?? []} />}
          {tab === "data" && (
            <div className="grid grid-2">
              <div className="stack"><h2>Inputs</h2><Json value={r.spec.inputs} /></div>
              <div className="stack"><h2>Result</h2><Json value={st.result} /></div>
              {st.failure?.stack && <div className="stack" style={{ gridColumn: "1 / -1" }}><h2>Failure</h2><pre className="stack-trace json">{st.failure.stack}</pre></div>}
            </div>
          )}
        </div>
      </Card>
    </>
  );
}

function Banners({ run, unknown, wait }: { run: Run; unknown: TaskRun[]; wait: ReturnType<typeof openWait> }) {
  const [emitting, setEmitting] = useState(false);
  const [resolving, setResolving] = useState<TaskRun | null>(null);
  const st = run.status;
  return (
    <>
      {unknown.map((t) => (
        <div key={t.id} className="alert alert-warning" role="status">
          <strong>❚❚ Outcome unknown</strong>
          <span className="grow">
            <code>{t.name}</code> (ref {t.ref}) is <strong>at-most-once</strong> and its worker <code>{t.worker}</code> was lost mid-attempt.
            Flyke will not guess: check the side effect, then tell it what happened.
          </span>
          <button className="btn btn-primary btn-sm" onClick={() => setResolving(t)}>Resolve…</button>
        </div>
      ))}
      {st.phase === "Paused" && unknown.length === 0 && (
        <div className="alert alert-warning" role="status">
          <strong>❚❚ Paused</strong>
          <span className="grow">{st.message}{st.reason === "NonDeterminism" || st.reason === "StepFailed" ? " — fix the workflow code, then resume." : ""}</span>
        </div>
      )}
      {wait && (
        <div className="alert alert-info" role="status">
          <strong>◷ Waiting for <code>{wait.name}</code></strong>
          <span className="grow">
            {Object.keys(wait.where).length > 0 && <>where {Object.entries(wait.where).map(([k, v]) => <code key={k} style={{ marginRight: 6 }}>{k}={JSON.stringify(v)}</code>)}</>}
            <span className="muted"> - no worker is held while waiting</span>
          </span>
          <button className="btn btn-primary btn-sm" onClick={() => setEmitting(true)}>Emit {wait.name}</button>
        </div>
      )}
      {st.phase === "Compensating" && (
        <div className="alert alert-serious" role="status"><strong>↶ Compensating</strong><span>{st.message} — undoing completed tasks in reverse order.</span></div>
      )}
      {emitting && wait && <EmitDialog name={wait.name} fields={wait.where} onClose={() => setEmitting(false)} />}
      {resolving && <ResolveDialog runId={run.metadata.name} task={resolving} onClose={() => setResolving(null)} />}
    </>
  );
}

function ResolveDialog({ runId, task, onClose }: { runId: string; task: TaskRun; onClose: () => void }) {
  const { client } = useConnection();
  const scope = useScope();
  const qc = useQueryClient();
  const [outcome, setOutcome] = useState<"succeeded" | "failed" | "retry">("succeeded");
  const [result, setResult] = useState("null");
  const [message, setMessage] = useState("");
  const [parseError, setParseError] = useState<Error | null>(null);
  const resolve = useMutation({
    mutationFn: (res: unknown) => client.resolveTask(runId, task.ref, outcome, res, message || undefined),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: [scope, "run", runId] });
      onClose();
    },
  });
  return (
    <Modal title={`Resolve ${task.name}`} onClose={onClose}>
      <form className="stack" onSubmit={(e) => {
        e.preventDefault();
        setParseError(null);
        try {
          resolve.mutate(outcome === "succeeded" ? JSON.parse(result || "null") : undefined);
        } catch (err) {
          setParseError(new Error(`Result must be JSON: ${(err as Error).message}`));
        }
      }}>
        <div className="segmented" role="radiogroup">
          {(["succeeded", "failed", "retry"] as const).map((o) => (
            <button type="button" key={o} className={outcome === o ? "on" : ""} onClick={() => setOutcome(o)} aria-pressed={outcome === o}>
              {o === "succeeded" ? "✓ It succeeded" : o === "failed" ? "✕ It failed" : "↻ Run it again"}
            </button>
          ))}
        </div>
        {outcome === "succeeded" && (
          <label className="field">Result the workflow should receive (JSON)
            <textarea rows={3} value={result} onChange={(e) => setResult(e.target.value)} />
          </label>
        )}
        {outcome === "failed" && (
          <label className="field">Reason <input value={message} onChange={(e) => setMessage(e.target.value)} placeholder="e.g. charge was never captured" /></label>
        )}
        {outcome === "retry" && <p className="secondary" style={{ margin: 0 }}>Only choose this if you verified the side effect did <strong>not</strong> happen.</p>}
        <ErrorBox error={parseError ?? resolve.error} />
        <div className="row gap-sm end">
          <button type="button" className="btn" onClick={onClose}>Cancel</button>
          <button type="submit" className="btn btn-primary" disabled={resolve.isPending}>Resolve and continue</button>
        </div>
      </form>
    </Modal>
  );
}

function TasksTable({ runId, tasks, now }: { runId: string; tasks: TaskRun[]; now: number }) {
  const [open, setOpen] = useState<string | null>(null);
  const { rows, pager } = usePaged(tasks);
  if (!tasks.length) return <Empty>No tasks yet.</Empty>;
  return (
    <div className="table-wrap">
      <table className="table">
        <thead><tr><th className="num">Ref</th><th>Task</th><th>State</th><th className="num">Attempt</th><th>Worker</th><th className="num">Duration</th><th>Details</th></tr></thead>
        <tbody>
          {rows.map((t) => (
            <FragmentRow key={t.id} t={t} now={now} open={open === t.id} toggle={() => setOpen(open === t.id ? null : t.id)} runId={runId} />
          ))}
        </tbody>
      </table>
      <Pager {...pager} />
    </div>
  );
}

function FragmentRow({ t, now, open, toggle }: { t: TaskRun; now: number; open: boolean; toggle: () => void; runId: string }) {
  return (
    <>
      <tr className="clickable" onClick={toggle} aria-expanded={open}>
        <td className="num mono">{t.ref}</td>
        <td>{t.kind === "compensation" ? <>↶ {t.name} <span className="muted">(compensation)</span></> : t.name}{t.reusedFrom && <span className="badge badge-outline" style={{ marginLeft: 6 }} title={`result reused from ${t.reusedFrom}`}>reused</span>}</td>
        <td><TaskBadge phase={t.phase} /></td>
        <td className="num">{t.attempt}</td>
        <td className="mono">{t.worker || "—"}</td>
        <td className="num">{span(t.startedAt, t.finishedAt, now)}</td>
        <td className="secondary" style={{ maxWidth: 360, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
          {t.failure ? `${shortType(t.failure.type)}: ${t.failure.message}` : t.idempotencyKey ? <span className="mono muted">key {t.idempotencyKey}</span> : ""}
        </td>
      </tr>
      {open && (
        <tr>
          <td colSpan={7} style={{ background: "var(--surface-2)" }}>
            <div className="grid grid-2">
              <div className="stack"><h2>Arguments</h2><Json value={t.args} /></div>
              <div className="stack"><h2>Result</h2><Json value={t.result} /></div>
              {t.failure && <div className="stack" style={{ gridColumn: "1 / -1" }}><h2>Last failure</h2><pre className="json">{t.failure.stack || `${t.failure.type}: ${t.failure.message}`}</pre></div>}
            </div>
          </td>
        </tr>
      )}
    </>
  );
}

function HistoryTable({ events }: { events: { id: number; type: string; time: string; ref?: number; name?: string; payload?: unknown; failure?: { type: string; message: string } }[] }) {
  const { rows, pager } = usePaged(events, undefined, 50);
  if (!events.length) return <Empty>No history.</Empty>;
  return (
    <div className="table-wrap">
      <p className="secondary" style={{ marginTop: 0 }}>The append-only event log the workflow body is replayed against.</p>
      <table className="table">
        <thead><tr><th className="num">#</th><th>Time</th><th>Event</th><th className="num">Ref</th><th>Name</th><th>Payload</th></tr></thead>
        <tbody>
          {rows.map((e) => (
            <tr key={e.id}>
              <td className="num mono">{e.id}</td>
              <td className="mono muted">{clock(e.time)}</td>
              <td>{e.type}</td>
              <td className="num mono">{e.ref || ""}</td>
              <td className="mono">{e.name}</td>
              <td className="mono secondary" style={{ maxWidth: 460, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
                {e.failure ? `${shortType(e.failure.type)}: ${e.failure.message}` : e.payload !== undefined ? JSON.stringify(e.payload) : ""}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      <Pager {...pager} />
    </div>
  );
}
