import { useEffect, useMemo, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useConnection, useScope } from "../api/context";
import { followLogs } from "../api/sse";
import { TERMINAL, type LogEntry, type Run } from "../api/types";
import { clock, fieldValue } from "./format";
import { Empty } from "./ui";

function origin(e: LogEntry): string {
  if (e.source === "system") return "flyke";
  if (e.source === "workflow") return "workflow";
  return e.attempt && e.attempt > 1 ? `${e.task}#${e.attempt}` : e.task ?? "task";
}

/** Live logs. `source` / `onSourceChange` optionally control the task filter
 *  (e.g. so the diagram inspector can jump to one task's logs). */
export function LogViewer({ run, source: sourceProp, onSourceChange }: { run: Run; source?: string; onSourceChange?: (s: string) => void }) {
  const { conn, client } = useConnection();
  const scope = useScope();
  const qc = useQueryClient();
  const [entries, setEntries] = useState<LogEntry[]>([]);
  const [sourceState, setSourceState] = useState("all");
  const source = sourceProp ?? sourceState;
  const setSource = onSourceChange ?? setSourceState;
  const [showDebug, setShowDebug] = useState(false);
  const [showSystem, setShowSystem] = useState(true);
  const [query, setQuery] = useState("");
  const [follow, setFollow] = useState(true);
  const [state, setState] = useState<"streaming" | "ended" | "error">("streaming");
  const box = useRef<HTMLDivElement>(null);
  const id = run.metadata.name;
  const finished = TERMINAL.includes(run.status.phase);

  useEffect(() => {
    setEntries([]);
    setState("streaming");
    if (finished) {
      // Finished runs: one fetch, no stream to keep open.
      let alive = true;
      client.runLogs(id).then(
        (all) => alive && (setEntries(all), setState("ended")),
        () => alive && setState("error"),
      );
      return () => {
        alive = false;
      };
    }
    const seen = new Set<number>();
    let pending: LogEntry[] = [];
    let raf = 0;
    const flush = () => {
      raf = 0;
      const batch = pending;
      pending = [];
      setEntries((prev) => prev.concat(batch));
    };
    const stop = followLogs(conn, id, {
      onLog: (e) => {
        if (seen.has(e.id)) return;
        seen.add(e.id);
        pending.push(e);
        if (!raf) raf = requestAnimationFrame(flush);
      },
      onEnd: () => {
        setState("ended");
        qc.invalidateQueries({ queryKey: [scope, "run", id] });
      },
      onError: () => setState("error"),
    });
    return () => {
      stop();
      if (raf) cancelAnimationFrame(raf);
    };
  }, [conn, client, id, qc, scope, finished]);

  const sources = useMemo(() => {
    const s = new Set<string>();
    entries.forEach((e) => e.source !== "system" && s.add(e.source === "workflow" ? "workflow" : e.task ?? ""));
    if (source !== "all") s.add(source);
    return [...s].sort();
  }, [entries, source]);

  const visible = useMemo(() => {
    const q = query.toLowerCase();
    return entries.filter((e) => {
      if (!showDebug && e.level === "debug") return false;
      if (!showSystem && e.source === "system") return false;
      if (source !== "all") {
        const key = e.source === "workflow" ? "workflow" : e.task;
        if (e.source === "system" ? !showSystem || (e.task && e.task !== source) : key !== source) return false;
      }
      if (q && !(`${e.message} ${JSON.stringify(e.fields ?? {})}`.toLowerCase().includes(q))) return false;
      return true;
    });
  }, [entries, source, showDebug, showSystem, query]);

  useEffect(() => {
    if (follow && box.current) box.current.scrollTop = box.current.scrollHeight;
  }, [visible, follow]);

  return (
    <div className="stack">
      <div className="row gap-sm wrap">
        <select value={source} onChange={(e) => setSource(e.target.value)} aria-label="Filter by task">
          <option value="all">All sources</option>
          {sources.map((s) => <option key={s} value={s}>{s}</option>)}
        </select>
        <input placeholder="Search logs…" value={query} onChange={(e) => setQuery(e.target.value)} style={{ width: 220 }} />
        <label className="check"><input type="checkbox" checked={showSystem} onChange={(e) => setShowSystem(e.target.checked)} /> control-plane events</label>
        <label className="check"><input type="checkbox" checked={showDebug} onChange={(e) => setShowDebug(e.target.checked)} /> debug</label>
        <label className="check"><input type="checkbox" checked={follow} onChange={(e) => setFollow(e.target.checked)} /> follow</label>
        <span className="grow" />
        <span className="muted" style={{ fontSize: 12 }}>
          {state === "streaming" ? <><span className="pulse">●</span> live</> : state === "ended" ? "stream ended" : "stream lost"} - {visible.length} lines
        </span>
      </div>
      <div className="logs" ref={box} onWheel={() => box.current && box.current.scrollTop + box.current.clientHeight < box.current.scrollHeight - 40 && setFollow(false)}>
        {visible.length === 0 ? (
          <Empty>{state === "streaming" ? "Waiting for logs…" : "No log lines match."}</Empty>
        ) : (
          visible.map((e) => (
            <div className="log-line" key={e.id}>
              <span className="log-time">{clock(e.time)}</span>
              <span className={`log-origin ${e.source === "system" ? "system" : ""}`} title={e.worker ? `on ${e.worker}` : undefined}>{origin(e)}</span>
              <span className={`log-level log-level-${e.level}`}>{e.level === "warning" ? "WARN" : e.level.toUpperCase().slice(0, 4)}</span>
              <span className={`log-msg ${e.source === "system" ? "system" : ""}`}>
                {e.message}
                {e.fields && Object.keys(e.fields).length > 0 && (
                  <span className="log-fields">
                    {Object.entries(e.fields).sort(([a], [b]) => a.localeCompare(b)).map(([k, v]) => `${k}=${fieldValue(v)}`).join(" ")}
                  </span>
                )}
              </span>
            </div>
          ))
        )}
      </div>
    </div>
  );
}
