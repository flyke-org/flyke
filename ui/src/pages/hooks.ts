import { useQuery } from "@tanstack/react-query";
import { useConnection, useScope } from "../api/context";
import { TERMINAL, type HistoryEvent, type Run } from "../api/types";

export const POLL = 2000;

export function useRuns(q: { workflow?: string; version?: string; phase?: string; limit?: number; retryOf?: string } = {}) {
  const { client } = useConnection();
  const scope = useScope();
  return useQuery({ queryKey: [scope, "runs", q], queryFn: () => client.listRuns(q), refetchInterval: POLL });
}

export function useWorkers() {
  const { client } = useConnection();
  const scope = useScope();
  return useQuery({ queryKey: [scope, "workers"], queryFn: () => client.listWorkers(), refetchInterval: 3000 });
}

/** Who the current token is. Admins can use every namespace. */
export function useWhoAmI() {
  const { client, conn } = useConnection();
  return useQuery({ queryKey: ["whoami", conn.token], queryFn: () => client.whoami(), refetchInterval: 5000, retry: false });
}

/** Namespaces the caller can see: all of them for the admin, otherwise its own. */
export function useNamespaces() {
  const { client, conn } = useConnection();
  return useQuery({ queryKey: ["namespaces", conn.token], queryFn: () => client.listNamespaces(), refetchInterval: 10_000 });
}

export function useWorkflows() {
  const { client } = useConnection();
  const scope = useScope();
  return useQuery({ queryKey: [scope, "workflows"], queryFn: () => client.listWorkflows(), refetchInterval: 10_000 });
}

export function useWorkflowVersions(name: string) {
  const { client } = useConnection();
  const scope = useScope();
  return useQuery({
    queryKey: [scope, "workflow-versions", name], queryFn: () => client.listWorkflowVersions(name),
    enabled: !!name, refetchInterval: 10_000,
  });
}

export function isLive(run?: Run): boolean {
  return !!run && !TERMINAL.includes(run.status.phase);
}

/** The event a run is currently waiting for, if any (from its history). */
export function openWait(history: HistoryEvent[]): { ref: number; name: string; where: Record<string, unknown> } | null {
  for (let i = history.length - 1; i >= 0; i--) {
    const ev = history[i];
    if (ev.type !== "WaitStarted") continue;
    const resolved = history.some((h) => h.ref === ev.ref && (h.type === "EventReceived" || h.type === "WaitTimedOut"));
    if (!resolved) return { ref: ev.ref ?? 0, name: ev.name ?? "", where: (ev.payload as Record<string, unknown>) ?? {} };
  }
  return null;
}

export function useRunHistory(id: string, live: boolean) {
  const { client } = useConnection();
  const scope = useScope();
  return useQuery({
    queryKey: [scope, "run", id, "history"],
    queryFn: () => client.runHistory(id),
    refetchInterval: live ? POLL : false,
  });
}
