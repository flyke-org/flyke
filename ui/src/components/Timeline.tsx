// Gantt-style view of a run: one row per task run, plus durable waits.
// Bar color = state (status palette, with the state named in the tooltip and
// in the task table below, so color never carries meaning alone).

import { useMemo, useRef, useState } from "react";
import type { HistoryEvent, Run, TaskRun } from "../api/types";
import { duration, shortType, span } from "./format";
import { taskStatus, useNow } from "./ui";

interface Row {
  key: string;
  label: string;
  sub?: string;
  kind: "task" | "compensation" | "wait";
  queuedFrom?: number;
  start?: number;
  end?: number;
  open: boolean;
  status: string;
  task?: TaskRun;
  wait?: { name: string; outcome: string };
}

function niceStep(rangeMs: number): number {
  const target = rangeMs / 5;
  const steps = [10, 20, 50, 100, 200, 500, 1e3, 2e3, 5e3, 1e4, 15e3, 3e4, 6e4, 12e4, 3e5, 6e5, 9e5, 18e5, 36e5, 72e5, 216e5, 432e5, 864e5];
  return steps.find((s) => s >= target) ?? steps[steps.length - 1];
}

export function Timeline({ run, tasks, history }: { run: Run; tasks: TaskRun[]; history: HistoryEvent[] }) {
  const live = !run.status.finishedAt;
  const now = useNow(live ? 500 : 60_000);
  const wrap = useRef<HTMLDivElement>(null);
  const [hover, setHover] = useState<{ row: Row; x: number; y: number } | null>(null);

  const { rows, t0, t1 } = useMemo(() => {
    const t0 = Date.parse(run.status.startedAt ?? run.metadata.createdAt);
    const end = run.status.finishedAt ? Date.parse(run.status.finishedAt) : now;
    const rows: Row[] = tasks
      .filter((t) => t.kind !== "step")
      .map((t) => ({
        key: t.id,
        label: t.kind === "compensation" ? `↶ ${t.name}` : t.name,
        sub: t.kind === "compensation" ? "compensation" : `#${t.ref}`,
        kind: t.kind as "task" | "compensation",
        queuedFrom: Date.parse(t.createdAt),
        start: t.startedAt ? Date.parse(t.startedAt) : undefined,
        end: t.finishedAt ? Date.parse(t.finishedAt) : undefined,
        open: !t.finishedAt && (t.phase === "Running" || t.phase === "Scheduled" || t.phase === "Unknown"),
        status: taskStatus(t.phase),
        task: t,
      }));
    // Durable waits from history: WaitStarted → EventReceived / WaitTimedOut.
    for (const ev of history) {
      if (ev.type !== "WaitStarted" && ev.type !== "TimerStarted") continue;
      const done = history.find((h) => h.ref === ev.ref && ["EventReceived", "WaitTimedOut", "TimerFired"].includes(h.type));
      rows.push({
        key: `wait-${ev.ref}`,
        label: ev.type === "TimerStarted" ? "⏲ sleep" : `◷ ${ev.name}`,
        sub: `#${ev.ref}`,
        kind: "wait",
        start: Date.parse(ev.time),
        end: done ? Date.parse(done.time) : undefined,
        open: !done && !run.status.finishedAt,
        status: done?.type === "WaitTimedOut" ? "critical" : "waiting",
        wait: { name: ev.name ?? "", outcome: done ? done.type : "waiting" },
      });
    }
    const order = (r: Row) => r.queuedFrom ?? r.start ?? 0;
    const ref = (r: Row) => r.task?.ref ?? Number(r.sub?.slice(1)) ?? 0;
    rows.sort((a, b) => order(a) - order(b) || ref(a) - ref(b));
    const lastEnd = Math.max(end, ...rows.map((r) => r.end ?? (r.open ? now : 0)));
    return { rows, t0, t1: Math.max(lastEnd, t0 + 1000) };
  }, [run, tasks, history, now]);

  if (!rows.length) return <div className="empty">No tasks scheduled yet.</div>;

  const range = t1 - t0;
  const pct = (t: number) => `${Math.max(0, Math.min(100, ((t - t0) / range) * 100))}%`;
  const width = (a: number, b: number) => `${Math.max(0.4, ((b - a) / range) * 100)}%`;
  const step = niceStep(range);
  const ticks: number[] = [];
  for (let t = 0; t <= range; t += step) ticks.push(t);

  const show = (row: Row) => (e: React.MouseEvent | React.FocusEvent) => {
    const box = wrap.current?.getBoundingClientRect();
    const target = (e.currentTarget as HTMLElement).getBoundingClientRect();
    if (!box) return;
    const x = "clientX" in e ? e.clientX - box.left : target.left - box.left + target.width / 2;
    setHover({ row, x: Math.min(x + 12, box.width - 330), y: target.bottom - box.top + 6 });
  };

  return (
    <div className="timeline" ref={wrap} onMouseLeave={() => setHover(null)}>
      <div className="tl-axis">
        <div />
        <div className="tl-ticks">
          {ticks.map((t) => (
            <span key={t} className="tl-tick" style={{ left: pct(t0 + t) }}>{t === 0 ? "0" : `+${duration(t)}`}</span>
          ))}
        </div>
      </div>
      {rows.map((row) => {
        const end = row.end ?? (row.open ? now : row.start);
        return (
          <div className="tl-row" key={row.key}>
            <div className="tl-label" title={row.label}>
              <span>{row.label}</span>
              <span className="muted mono">{row.sub}</span>
            </div>
            <div className="tl-track">
              {ticks.map((t) => <span key={t} className="tl-gridline" style={{ left: pct(t0 + t) }} />)}
              {row.kind !== "wait" && row.queuedFrom !== undefined && (row.start ?? end ?? row.queuedFrom) > row.queuedFrom && (
                <span
                  className={`tl-bar ${row.task && row.task.attempt > 1 ? "retrying" : "queued"}`}
                  title={row.task && row.task.attempt > 1 ? `${row.task.attempt - 1} earlier attempt(s) + backoff` : "queued"}
                  style={{ left: pct(row.queuedFrom), width: width(row.queuedFrom, row.start ?? end ?? now) }}
                />
              )}
              {row.kind === "wait" && row.start !== undefined && end !== undefined && (
                <span className="tl-wait" style={{ left: pct(row.start), width: width(row.start, end) }} />
              )}
              {row.start !== undefined && end !== undefined && (
                <button
                  type="button"
                  className={`tl-bar ${row.status}`}
                  aria-label={`${row.label}: ${row.task?.phase ?? row.wait?.outcome}`}
                  style={row.kind === "wait"
                    ? { left: pct(row.end ?? end), width: "4px" }
                    : { left: pct(row.start), width: width(row.start, end) }}
                  onMouseMove={show(row)}
                  onFocus={show(row)}
                  onBlur={() => setHover(null)}
                />
              )}
            </div>
          </div>
        );
      })}
      <div className="legend" aria-hidden>
        <span><i style={{ background: "var(--good)" }} />✓ succeeded</span>
        <span><i style={{ background: "var(--info)" }} />● running</span>
        <span><i style={{ background: "var(--critical)" }} />✕ failed</span>
        <span><i style={{ background: "var(--warning)" }} />? unknown outcome</span>
        <span><i className="tl-bar queued" style={{ position: "static" }} />queued</span>
        <span><i className="tl-bar retrying" style={{ position: "static" }} />earlier attempts + backoff</span>
        <span><i style={{ background: "var(--waiting)" }} />◷ durable wait</span>
      </div>
      {hover && <Tip row={hover.row} x={hover.x} y={hover.y} now={now} />}
    </div>
  );
}

function Tip({ row, x, y, now }: { row: Row; x: number; y: number; now: number }) {
  const t = row.task;
  return (
    <div className="tooltip" style={{ left: Math.max(0, x), top: y }}>
      <strong>{row.label}</strong>
      <dl className="kv" style={{ marginTop: 6 }}>
        {t ? (
          <>
            <dt>State</dt><dd>{t.phase}</dd>
            <dt>Attempt</dt><dd>{t.attempt}{t.attempt > 1 ? ` (${t.attempt - 1} failed before)` : ""}</dd>
            <dt>Worker</dt><dd>{t.worker || "—"}</dd>
            <dt>{t.attempt > 1 ? "Before this attempt" : "Queued"}</dt><dd>{t.startedAt ? span(t.createdAt, t.startedAt) : span(t.createdAt, undefined, now)}</dd>
            <dt>Ran</dt><dd>{span(t.startedAt, t.finishedAt, now)}</dd>
            {t.failure && <><dt>Error</dt><dd>{shortType(t.failure.type)}: {t.failure.message}</dd></>}
          </>
        ) : (
          <>
            <dt>Waiting for</dt><dd>{row.wait?.name || "timer"}</dd>
            <dt>Outcome</dt><dd>{row.wait?.outcome}</dd>
            <dt>Duration</dt><dd>{row.start !== undefined ? duration((row.end ?? now) - row.start) : "—"}</dd>
          </>
        )}
      </dl>
    </div>
  );
}
