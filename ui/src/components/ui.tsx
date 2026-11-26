// Small shared building blocks. Status always ships as icon + label, never
// color alone (status colors are reserved for state).

import { useEffect, useState, type ReactNode } from "react";
import type { RunPhase, TaskRun } from "../api/types";

export type Status = "good" | "info" | "waiting" | "warning" | "serious" | "critical" | "muted";

const RUN_STATUS: Record<RunPhase, [Status, string]> = {
  Pending: ["info", "○"],
  Queued: ["waiting", "◷"],
  Running: ["info", "●"],
  Waiting: ["waiting", "◷"],
  Paused: ["warning", "❚❚"],
  Compensating: ["serious", "↶"],
  Succeeded: ["good", "✓"],
  Failed: ["critical", "✕"],
  TimedOut: ["critical", "⌛"],
  Cancelled: ["muted", "⊘"],
  Skipped: ["muted", "»"],
};

const TASK_STATUS: Record<TaskRun["phase"], [Status, string, string]> = {
  Pending: ["waiting", "○", "Pending"],
  Scheduled: ["info", "◔", "Scheduled"],
  Running: ["info", "●", "Running"],
  Succeeded: ["good", "✓", "Succeeded"],
  Failed: ["critical", "✕", "Failed"],
  Unknown: ["warning", "?", "Unknown outcome"],
  Cancelled: ["muted", "⊘", "Cancelled"],
};

export function runStatus(p: RunPhase): Status {
  return RUN_STATUS[p]?.[0] ?? "muted";
}

export function taskStatus(p: TaskRun["phase"]): Status {
  return TASK_STATUS[p]?.[0] ?? "muted";
}

export function PhaseBadge({ phase }: { phase: RunPhase }) {
  const [status, icon] = RUN_STATUS[phase] ?? ["muted", "·"];
  return (
    <span className={`badge badge-${status}`}>
      <span aria-hidden className={phase === "Running" ? "pulse" : undefined}>{icon}</span>
      {phase}
    </span>
  );
}

export function TaskBadge({ phase }: { phase: TaskRun["phase"] }) {
  const [status, icon, label] = TASK_STATUS[phase] ?? ["muted", "·", phase];
  return (
    <span className={`badge badge-${status}`}>
      <span aria-hidden>{icon}</span>
      {label}
    </span>
  );
}

export function Card({ title, actions, children, className }: { title?: ReactNode; actions?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <section className={`card ${className ?? ""}`}>
      {(title || actions) && (
        <header className="card-head">
          {title && <h2>{title}</h2>}
          {actions && <div className="row gap-sm">{actions}</div>}
        </header>
      )}
      {children}
    </section>
  );
}

export function Empty({ children }: { children: ReactNode }) {
  return <div className="empty">{children}</div>;
}

export function ErrorBox({ error }: { error: unknown }) {
  if (!error) return null;
  return <div className="alert alert-critical" role="alert"><strong>✕ Error</strong> {(error as Error).message}</div>;
}

export function Json({ value }: { value: unknown }) {
  if (value === undefined) return <span className="muted">—</span>;
  return <pre className="json">{JSON.stringify(value, null, 2)}</pre>;
}

export function Labels({ labels }: { labels?: Record<string, string> }) {
  const entries = Object.entries(labels ?? {}).sort(([a], [b]) => a.localeCompare(b));
  if (!entries.length) return <span className="muted">—</span>;
  return (
    <span className="chips">
      {entries.map(([k, v]) => (
        <span className="chip" key={k}>
          <span className="muted">{k}=</span>{v}
        </span>
      ))}
    </span>
  );
}

export function CopyButton({ text, label = "Copy" }: { text: string; label?: string }) {
  const [done, setDone] = useState(false);
  return (
    <button
      type="button"
      className="btn btn-ghost btn-sm"
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(text);
          setDone(true);
          setTimeout(() => setDone(false), 1500);
        } catch {
          /* clipboard unavailable */
        }
      }}
    >
      {done ? "✓ Copied" : label}
    </button>
  );
}

/** Re-renders every `ms` so relative times stay fresh. */
export function useNow(ms = 1000): number {
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), ms);
    return () => clearInterval(t);
  }, [ms]);
  return now;
}

export function Modal({ title, onClose, children }: { title: string; onClose: () => void; children: ReactNode }) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);
  return (
    <div className="modal-backdrop" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className="modal" role="dialog" aria-modal aria-label={title}>
        <header className="card-head">
          <h2>{title}</h2>
          <button className="btn btn-ghost btn-sm" onClick={onClose} aria-label="Close">✕</button>
        </header>
        {children}
      </div>
    </div>
  );
}

export function Confirm({ title, body, action, danger, onConfirm, onClose }: {
  title: string; body: ReactNode; action: string; danger?: boolean; onConfirm: () => void; onClose: () => void;
}) {
  return (
    <Modal title={title} onClose={onClose}>
      <div className="stack">
        <div>{body}</div>
        <div className="row gap-sm end">
          <button className="btn" onClick={onClose}>Cancel</button>
          <button className={`btn ${danger ? "btn-danger" : "btn-primary"}`} onClick={() => { onConfirm(); onClose(); }}>{action}</button>
        </div>
      </div>
    </Modal>
  );
}

export function Meter({ used, total, label }: { used: number; total: number; label: string }) {
  const pct = total > 0 ? Math.min(100, (used / total) * 100) : 0;
  return (
    <span className="meter" title={label}>
      <span className="meter-track"><span className="meter-fill" style={{ width: `${pct}%` }} /></span>
      <span className="tabular">{used}/{total}</span>
    </span>
  );
}

const PAGE_SIZES = [10, 25, 50, 100];

export type PagerState = { page: number; pages: number; size: number; total: number; setPage: (p: number) => void; setSize: (s: number) => void };

/** Client-side pagination of an already-fetched list. `resetKey` sends the
 *  pager back to the first page when it changes (e.g. a new filter). */
export function usePaged<T>(items: T[] | undefined, resetKey: unknown = undefined, initialSize = 25): { rows: T[]; pager: PagerState } {
  const [size, setSize] = useState(initialSize);
  const [page, setPage] = useState(0);
  useEffect(() => setPage(0), [resetKey]);
  const all = items ?? [];
  const pages = Math.max(1, Math.ceil(all.length / size));
  const current = Math.min(page, pages - 1);
  return {
    rows: all.slice(current * size, (current + 1) * size),
    pager: { page: current, pages, size, total: all.length, setPage, setSize: (s) => { setSize(s); setPage(0); } },
  };
}

/** Footer for a paginated table; hidden while everything fits on the smallest page. */
export function Pager({ page, pages, size, total, setPage, setSize }: PagerState) {
  if (total <= PAGE_SIZES[0]) return null;
  const from = page * size + 1;
  const to = Math.min(total, (page + 1) * size);
  return (
    <nav className="pager" aria-label="Pagination">
      <span className="pager-range">
        <strong>{from}–{to}</strong> of <strong>{total}</strong>
      </span>
      <label className="pager-size">
        Rows per page
        <select value={size} onChange={(e) => setSize(Number(e.target.value))}>
          {PAGE_SIZES.map((s) => <option key={s} value={s}>{s}</option>)}
        </select>
      </label>
      <span className="pager-nav">
        <button type="button" className="btn btn-sm btn-ghost" disabled={page === 0} onClick={() => setPage(0)} aria-label="First page">«</button>
        <button type="button" className="btn btn-sm btn-ghost" disabled={page === 0} onClick={() => setPage(page - 1)} aria-label="Previous page">‹</button>
        <span className="pager-page">Page {page + 1} of {pages}</span>
        <button type="button" className="btn btn-sm btn-ghost" disabled={page >= pages - 1} onClick={() => setPage(page + 1)} aria-label="Next page">›</button>
        <button type="button" className="btn btn-sm btn-ghost" disabled={page >= pages - 1} onClick={() => setPage(pages - 1)} aria-label="Last page">»</button>
      </span>
    </nav>
  );
}
