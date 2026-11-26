// Follows a run's log stream (Server-Sent Events) with fetch, so the bearer
// token can be sent as a header. Resumes after the last id on reconnect.

import { headers, type Connection } from "./client";
import type { LogEntry, Run } from "./types";

export interface FollowHandlers {
  onLog: (e: LogEntry) => void;
  onEnd: (r: Run) => void;
  onError?: (err: Error) => void;
}

export function followLogs(conn: Connection, runId: string, h: FollowHandlers): () => void {
  const ctrl = new AbortController();
  let after = 0;
  let ended = false;

  const once = async () => {
    const url = `/api/v1/namespaces/${encodeURIComponent(conn.namespace)}/runs/${encodeURIComponent(runId)}/logs?follow=true&after=${after}`;
    const resp = await fetch(url, { headers: { ...headers(conn), Accept: "text/event-stream" }, signal: ctrl.signal });
    if (!resp.ok || !resp.body) throw new Error(`log stream: ${resp.status}`);
    const reader = resp.body.pipeThrough(new TextDecoderStream()).getReader();
    let buf = "";
    for (;;) {
      const { value, done } = await reader.read();
      if (done) return;
      buf += value;
      let sep: number;
      while ((sep = buf.indexOf("\n\n")) >= 0) {
        const block = buf.slice(0, sep);
        buf = buf.slice(sep + 2);
        let event = "message";
        const data: string[] = [];
        for (const line of block.split("\n")) {
          if (line.startsWith("event:")) event = line.slice(6).trim();
          else if (line.startsWith("data:")) data.push(line.slice(5).replace(/^ /, ""));
        }
        if (!data.length) continue;
        const payload = JSON.parse(data.join("\n"));
        if (event === "log") {
          after = Math.max(after, payload.id);
          h.onLog(payload as LogEntry);
        } else if (event === "end") {
          ended = true;
          h.onEnd(payload as Run);
          return;
        }
      }
    }
  };

  (async () => {
    let failures = 0;
    while (!ended && !ctrl.signal.aborted) {
      try {
        await once();
        failures = 0;
      } catch (e) {
        if (ctrl.signal.aborted) return;
        if (++failures > 20) {
          h.onError?.(e as Error);
          return;
        }
      }
      if (!ended) await new Promise((r) => setTimeout(r, Math.min(1000 * failures, 5000) || 500));
    }
  })();

  return () => ctrl.abort();
}
