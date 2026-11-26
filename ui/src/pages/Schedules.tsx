import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { useConnection, useScope } from "../api/context";
import type { Schedule, ScheduleSpec } from "../api/types";
import { InputsForm } from "../components/forms";
import { ago, dateTime, until, zonedTime } from "../components/format";
import { Card, Confirm, Empty, ErrorBox, Modal, Pager, PhaseBadge, useNow, usePaged } from "../components/ui";
import { useWorkflows } from "./hooks";

const NAME_RE = /^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$/;

function StateBadge({ s }: { s: Schedule }) {
  if (s.spec.paused) return <span className="badge badge-warning"><span aria-hidden>❚❚</span>Paused</span>;
  if (s.status.lastError) return <span className="badge badge-critical" title={s.status.lastError}><span aria-hidden>✕</span>Error</span>;
  return <span className="badge badge-good"><span aria-hidden>●</span>Active</span>;
}

export function Schedules() {
  const { client } = useConnection();
  const scope = useScope();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const now = useNow(10_000);
  const [params, setParams] = useSearchParams();
  const schedules = useQuery({ queryKey: [scope, "schedules"], queryFn: () => client.listSchedules(), refetchInterval: 5000 });
  const [editing, setEditing] = useState<Schedule | "new" | null>(params.has("new") ? "new" : null);
  const [deleting, setDeleting] = useState<Schedule | null>(null);
  const refresh = () => qc.invalidateQueries({ queryKey: [scope, "schedules"] });
  const toggle = useMutation({
    mutationFn: (s: Schedule) => client.scheduleAction(s.metadata.name, s.spec.paused ? "resume" : "pause"),
    onSuccess: refresh,
  });
  const trigger = useMutation({
    mutationFn: (s: Schedule) => client.triggerSchedule(s.metadata.name),
    onSuccess: (run) => navigate(`/runs/${run.metadata.name}`),
  });
  const remove = useMutation({ mutationFn: (s: Schedule) => client.deleteSchedule(s.metadata.name), onSuccess: refresh });
  const all = schedules.data ?? [];
  const { rows, pager } = usePaged(all);
  const close = () => {
    setEditing(null);
    if (params.has("new")) setParams({}, { replace: true });
  };

  return (
    <>
      <div className="page-head">
        <div>
          <h1>Schedules</h1>
          <div className="subtitle">
            Start a workflow at recurring times: every 15 minutes, every day at 03:00, or any cron expression.
          </div>
        </div>
        <button className="btn btn-primary" onClick={() => setEditing("new")}>+ New schedule</button>
      </div>
      <ErrorBox error={schedules.error ?? toggle.error ?? trigger.error ?? remove.error} />
      <Card className="flush">
        {schedules.isSuccess && all.length === 0 ? (
          <Empty>
            No schedules yet. Create one here, run <code>flyke schedules create nightly Report --daily 03:00</code>,
            or declare it in code with <code>on.schedule("0 3 * * *")</code>.
          </Empty>
        ) : (
          <><div className="table-wrap">
            <table className="table">
              <thead><tr><th>Schedule</th><th>Workflow</th><th>When</th><th>State</th><th>Next run</th><th>Last run</th><th className="actions">Actions</th></tr></thead>
              <tbody>
                {rows.map((s) => (
                  <tr key={s.metadata.name}>
                    <td>
                      <span className="mono">{s.metadata.name}</span>
                      {s.status.declared && (
                        <span className="badge badge-outline" style={{ marginLeft: 6 }} title="Declared in the workflow's code with on.schedule: change it there and deploy again">code</span>
                      )}
                    </td>
                    <td><Link to={`/workflows/${s.spec.workflow}`}>{s.spec.workflow}</Link></td>
                    <td>
                      <div>{s.status.description}</div>
                      <div className="muted mono" style={{ fontSize: 11.5 }}>{s.spec.cron}</div>
                    </td>
                    <td><StateBadge s={s} /></td>
                    <td title={s.status.nextRunAt ? zonedTime(s.status.nextRunAt, s.spec.tz) : undefined}>
                      {s.status.nextRunAt ? until(s.status.nextRunAt, now) : <span className="muted">—</span>}
                    </td>
                    <td>
                      {s.status.lastRun ? (
                        <span className="row gap-sm" title={dateTime(s.status.lastRunAt)}>
                          {s.status.lastRunPhase ? <PhaseBadge phase={s.status.lastRunPhase} /> : null}
                          <Link className="muted" to={`/runs/${s.status.lastRun}`}>{ago(s.status.lastRunAt, now)}</Link>
                        </span>
                      ) : <span className="muted">never</span>}
                    </td>
                    <td className="actions">
                      <span className="row gap-sm end">
                        <button className="btn btn-sm" onClick={() => trigger.mutate(s)} disabled={trigger.isPending} title="Start a run now; the timetable does not change">▶ Run now</button>
                        <button className="btn btn-sm" onClick={() => toggle.mutate(s)} disabled={toggle.isPending}>{s.spec.paused ? "Resume" : "Pause"}</button>
                        {!s.status.declared && <button className="btn btn-sm" onClick={() => setEditing(s)}>Edit</button>}
                        {!s.status.declared && <button className="btn btn-sm btn-danger" onClick={() => setDeleting(s)}>Delete</button>}
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
      {editing && <ScheduleForm existing={editing === "new" ? undefined : editing} workflow={params.get("new") ?? undefined} onClose={close} />}
      {deleting && (
        <Confirm
          title={`Delete ${deleting.metadata.name}?`}
          danger
          action="Delete"
          body={<><strong>{deleting.spec.workflow}</strong> will no longer run “{deleting.status.description}”. Runs it already started are kept.</>}
          onConfirm={() => remove.mutate(deleting)}
          onClose={() => setDeleting(null)}
        />
      )}
    </>
  );
}

// ------------------------------------------------------------ the "when" ----

type Mode = "interval" | "hourly" | "daily" | "weekly" | "monthly" | "cron";

interface When {
  mode: Mode;
  every: number;
  unit: "m" | "h";
  minute: number;
  time: string; // HH:MM
  days: number[]; // 0 = Sunday
  dom: number;
  cron: string;
}

const MODES: [Mode, string][] = [
  ["interval", "Interval"], ["hourly", "Hourly"], ["daily", "Daily"], ["weekly", "Weekly"], ["monthly", "Monthly"], ["cron", "Cron"],
];
const DAYS = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];
const DEFAULT_WHEN: When = { mode: "daily", every: 15, unit: "m", minute: 0, time: "03:00", days: [1], dom: 1, cron: "0 9 * * 1-5" };

function toCron(w: When): string {
  const [h, m] = w.time.split(":").map(Number);
  switch (w.mode) {
    case "interval": return `@every ${w.every}${w.unit}`;
    case "hourly": return `${w.minute} * * * *`;
    case "daily": return `${m} ${h} * * *`;
    case "weekly": return `${m} ${h} * * ${[...w.days].sort().join(",")}`;
    case "monthly": return `${m} ${h} ${w.dom} * *`;
    default: return w.cron.trim();
  }
}

/** Reads a cron expression back into the form; anything else stays in Cron mode. */
function fromCron(cron: string): When {
  const w = { ...DEFAULT_WHEN, mode: "cron" as Mode, cron };
  const every = /^@every (\d+)(m|h)$/.exec(cron);
  if (every) return { ...w, mode: "interval", every: Number(every[1]), unit: every[2] as "m" | "h" };
  const f = cron.split(/\s+/);
  if (f.length !== 5 || !/^\d+$/.test(f[0]) || f[3] !== "*") return w;
  const time = /^\d+$/.test(f[1]) ? `${f[1].padStart(2, "0")}:${f[0].padStart(2, "0")}` : "";
  if (f[1] === "*" && f[2] === "*" && f[4] === "*") return { ...w, mode: "hourly", minute: Number(f[0]) };
  if (!time) return w;
  if (f[2] === "*" && f[4] === "*") return { ...w, mode: "daily", time };
  if (f[2] === "*" && /^[0-6](,[0-6])*$/.test(f[4])) return { ...w, mode: "weekly", time, days: f[4].split(",").map(Number) };
  if (/^\d+$/.test(f[2]) && f[4] === "*") return { ...w, mode: "monthly", time, dom: Number(f[2]) };
  return w;
}

function timeZones(): string[] {
  const intl = Intl as unknown as { supportedValuesOf?: (key: string) => string[] };
  try {
    return ["UTC", ...(intl.supportedValuesOf?.("timeZone") ?? [])];
  } catch {
    return ["UTC"];
  }
}

function localZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

function useDebounced<T>(value: T, ms: number): T {
  const [v, setV] = useState(value);
  useEffect(() => {
    const t = setTimeout(() => setV(value), ms);
    return () => clearTimeout(t);
  }, [value, ms]);
  return v;
}

function ScheduleForm({ existing, workflow: preselected, onClose }: { existing?: Schedule; workflow?: string; onClose: () => void }) {
  const { client } = useConnection();
  const scope = useScope();
  const qc = useQueryClient();
  const now = useNow(30_000);
  const workflows = useWorkflows();
  const [name, setName] = useState(existing?.metadata.name ?? "");
  const [workflow, setWorkflow] = useState(existing?.spec.workflow ?? preselected ?? "");
  const [when, setWhen] = useState<When>(() => (existing ? fromCron(existing.spec.cron) : DEFAULT_WHEN));
  const [tz, setTz] = useState(existing ? existing.spec.tz || "UTC" : localZone());
  const [skipMissed, setSkipMissed] = useState(existing?.spec.missed === "skip");
  const [err, setErr] = useState<Error | null>(null);
  const zones = useMemo(timeZones, []);
  const set = (patch: Partial<When>) => setWhen((w) => ({ ...w, ...patch }));

  const names = (workflows.data ?? []).map((w) => w.metadata.name);
  useEffect(() => {
    if (!workflow && names.length) setWorkflow(names[0]);
  }, [workflow, names.join(",")]); // eslint-disable-line react-hooks/exhaustive-deps
  const wf = workflows.data?.find((w) => w.metadata.name === workflow);

  // The control plane explains the expression: one parser, one wording, the
  // same as the CLI shows.
  const cron = toCron(when);
  const zone = tz === "UTC" ? "" : tz;
  const asked = useDebounced(cron, 300);
  const preview = useQuery({
    queryKey: [scope, "schedule-preview", asked, zone],
    queryFn: () => client.previewSchedule(asked, zone),
    enabled: asked !== "",
    retry: false,
  });
  const usesClock = when.mode !== "interval" && when.mode !== "hourly";

  const save = useMutation({
    mutationFn: (inputs: Record<string, unknown>) => {
      const spec: ScheduleSpec = {
        workflow, cron, tz: zone, inputs, missed: skipMissed ? "skip" : "run_once", paused: existing?.spec.paused,
      };
      return client.putSchedule(name.trim(), spec);
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: [scope, "schedules"] });
      onClose();
    },
  });
  const submit = (inputs: Record<string, unknown>) => {
    setErr(null);
    if (!NAME_RE.test(name.trim())) return setErr(new Error("Name: lowercase letters, digits and '-' only, e.g. nightly-report."));
    if (!workflow) return setErr(new Error("Choose the workflow to run."));
    if (when.mode === "weekly" && when.days.length === 0) return setErr(new Error("Choose at least one day of the week."));
    if (preview.error) return setErr(preview.error as Error);
    save.mutate(inputs);
  };

  return (
    <Modal title={existing ? `Edit ${existing.metadata.name}` : "New schedule"} onClose={onClose}>
      <div className="stack">
        <div className="form-grid">
          <label className="field">Name
            <input value={name} onChange={(e) => setName(e.target.value)} placeholder="nightly-report" disabled={!!existing} autoFocus={!existing} className="mono" />
          </label>
          <label className="field">Workflow
            <select value={workflow} onChange={(e) => setWorkflow(e.target.value)}>
              {names.length === 0 && <option value="">no workflow deployed yet</option>}
              {names.map((n) => <option key={n} value={n}>{n}</option>)}
            </select>
          </label>
        </div>

        <div className="stack" style={{ gap: 6 }}>
          <span className="secondary" style={{ fontSize: 12.5, fontWeight: 500 }}>Repeat</span>
          <div className="segmented" role="radiogroup" aria-label="Repeat">
            {MODES.map(([m, label]) => (
              <button key={m} type="button" className={when.mode === m ? "on" : ""} aria-pressed={when.mode === m} onClick={() => set({ mode: m })}>{label}</button>
            ))}
          </div>
        </div>

        <div className="row gap-sm wrap" style={{ alignItems: "flex-end" }}>
          {when.mode === "interval" && (
            <>
              <label className="field">Every
                <input type="number" min={1} value={when.every} onChange={(e) => set({ every: Math.max(1, Number(e.target.value) || 1) })} style={{ width: 90 }} />
              </label>
              <label className="field">Unit
                <select value={when.unit} onChange={(e) => set({ unit: e.target.value as "m" | "h" })}>
                  <option value="m">minutes</option>
                  <option value="h">hours</option>
                </select>
              </label>
            </>
          )}
          {when.mode === "hourly" && (
            <label className="field">At minute
              <input type="number" min={0} max={59} value={when.minute} onChange={(e) => set({ minute: Math.min(59, Math.max(0, Number(e.target.value) || 0)) })} style={{ width: 90 }} />
            </label>
          )}
          {when.mode === "weekly" && (
            <div className="stack" style={{ gap: 4 }}>
              <span className="secondary" style={{ fontSize: 12.5, fontWeight: 500 }}>On</span>
              <div className="segmented" role="group" aria-label="Days of the week">
                {[1, 2, 3, 4, 5, 6, 0].map((d) => {
                  const on = when.days.includes(d);
                  return (
                    <button key={d} type="button" className={on ? "on" : ""} aria-pressed={on}
                      onClick={() => set({ days: on ? when.days.filter((x) => x !== d) : [...when.days, d] })}>{DAYS[d]}</button>
                  );
                })}
              </div>
            </div>
          )}
          {when.mode === "monthly" && (
            <label className="field">Day of month
              <input type="number" min={1} max={31} value={when.dom} onChange={(e) => set({ dom: Math.min(31, Math.max(1, Number(e.target.value) || 1)) })} style={{ width: 110 }} />
            </label>
          )}
          {(when.mode === "daily" || when.mode === "weekly" || when.mode === "monthly") && (
            <label className="field">At
              <input type="time" value={when.time} onChange={(e) => set({ time: e.target.value || "00:00" })} />
            </label>
          )}
          {when.mode === "cron" && (
            <label className="field" style={{ flex: 1, minWidth: 220 }}>Cron expression
              <span className="hint">minute hour day-of-month month day-of-week, or @hourly, @daily, @weekly, @monthly</span>
              <input value={when.cron} onChange={(e) => set({ cron: e.target.value })} className="mono" spellCheck={false} placeholder="0 9 * * 1-5" />
            </label>
          )}
          {usesClock && (
            <label className="field">Time zone
              <select value={tz} onChange={(e) => setTz(e.target.value)}>
                {!zones.includes(tz) && <option value={tz}>{tz}</option>}
                {zones.map((z) => <option key={z} value={z}>{z}</option>)}
              </select>
            </label>
          )}
        </div>

        {preview.error ? (
          <div className="alert alert-critical" role="alert"><strong>✕ Not a valid schedule</strong> {(preview.error as Error).message}</div>
        ) : (
          <div className="alert alert-info" aria-live="polite">
            <div className="stack" style={{ gap: 4, flex: 1 }}>
              <div><strong>{preview.data?.description ?? "…"}</strong> <span className="muted mono">{cron}</span></div>
              {preview.data && (
                <div className="secondary" style={{ fontSize: 12.5 }}>
                  Next runs: {preview.data.upcoming.slice(0, 3).map((t, i) => (
                    <span key={t}>{i > 0 ? " - " : ""}{zonedTime(t, zone)}{i === 0 ? ` (${until(t, now)})` : ""}</span>
                  ))}
                </div>
              )}
            </div>
          </div>
        )}

        <label className="check">
          <input type="checkbox" checked={skipMissed} onChange={(e) => setSkipMissed(e.target.checked)} />
          <span>
            Skip a run that was missed while the server was down
            <span className="muted"> (otherwise it runs once when the server is back)</span>
          </span>
        </label>

        <div className="stack" style={{ gap: 6 }}>
          <span className="secondary" style={{ fontSize: 12.5, fontWeight: 500 }}>
            Inputs of each run <span className="muted" style={{ fontWeight: 400 }}>· text may use <code>{"{schedule.date}"}</code> and <code>{"{schedule.time}"}</code></span>
          </span>
          {wf ? (
            <InputsForm
              key={workflow}
              schema={wf.spec.manifest.inputs_schema}
              initialValues={existing?.spec.workflow === workflow ? existing.spec.inputs : undefined}
              onSubmit={submit}
              pending={save.isPending}
              error={err ?? save.error}
              submitLabel={existing ? "Save schedule" : "Create schedule"}
              footer={<button type="button" className="btn" onClick={onClose}>Cancel</button>}
            />
          ) : (
            <Empty>Deploy a workflow first: <code>flyke deploy file.py:Workflow</code></Empty>
          )}
        </div>
      </div>
    </Modal>
  );
}
