// Thin typed client over the REST API. The UI uses exactly the same API as
// the CLI; nothing here talks to workers.

import type {
  AccessToken, FlykeEvent, HistoryEvent, JoinToken, List, LogEntry, Namespace, PlaygroundInfo, PlaygroundProject, Pool, Resources, RetryRequest, Run, Schedule, SchedulePreview, ScheduleSpec, Secret, SecretSpec, SecretType,
  TaskRun, WhoAmI, Worker, Workflow, WorkflowVersion,
} from "./types";

export class ApiError extends Error {
  constructor(public status: number, public reason: string, message: string) {
    super(message);
  }
}

export interface Connection {
  namespace: string;
  token: string;
}

export function headers(conn: Connection, json = false): HeadersInit {
  const h: Record<string, string> = {};
  if (conn.token) h["Authorization"] = `Bearer ${conn.token}`;
  if (json) h["Content-Type"] = "application/json";
  return h;
}

async function request<T>(conn: Connection, method: string, path: string, body?: unknown): Promise<T> {
  let resp: Response;
  try {
    resp = await fetch(path, {
      method,
      headers: headers(conn, body !== undefined),
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  } catch (e) {
    throw new ApiError(0, "Unreachable", `Cannot reach the control plane (${(e as Error).message})`);
  }
  if (!resp.ok) {
    let reason = resp.statusText;
    let message = `${resp.status} ${resp.statusText}`;
    try {
      const st = await resp.json();
      reason = st.reason ?? reason;
      message = st.message ?? message;
    } catch {
      /* not a Status body */
    }
    throw new ApiError(resp.status, reason, message);
  }
  if (resp.status === 204) return undefined as T;
  return (await resp.json()) as T;
}

const enc = encodeURIComponent;

export function api(conn: Connection) {
  const ns = (p: string) => `/api/v1/namespaces/${enc(conn.namespace)}${p}`;
  const fleet = (shared: boolean, p: string) => (shared ? `/api/v1${p}` : ns(p));
  const get = <T>(p: string) => request<T>(conn, "GET", p);
  const post = <T>(p: string, b?: unknown) => request<T>(conn, "POST", p, b);
  return {
    health: async () => (await fetch("/healthz")).ok,

    listRuns: (q: { workflow?: string; version?: string; phase?: string; limit?: number; retryOf?: string } = {}) => {
      const params = new URLSearchParams();
      if (q.workflow) params.set("workflow", q.workflow);
      if (q.version) params.set("version", q.version);
      if (q.retryOf) params.set("retryOf", q.retryOf);
      if (q.phase) params.set("phase", q.phase);
      params.set("limit", String(q.limit ?? 200));
      return get<List<Run>>(ns(`/runs?${params}`)).then((l) => l.items);
    },
    getRun: (id: string) => get<Run>(ns(`/runs/${enc(id)}`)),
    runTasks: (id: string) => get<List<TaskRun>>(ns(`/runs/${enc(id)}/tasks`)).then((l) => l.items ?? []),
    runHistory: (id: string) => get<List<HistoryEvent>>(ns(`/runs/${enc(id)}/history`)).then((l) => l.items ?? []),
    runLogs: (id: string) => get<List<LogEntry>>(ns(`/runs/${enc(id)}/logs`)).then((l) => l.items ?? []),
    cancelRun: (id: string) => post<Run>(ns(`/runs/${enc(id)}/cancel`)),
    resumeRun: (id: string) => post<Run>(ns(`/runs/${enc(id)}/resume`)),
    retryRun: (id: string, req: RetryRequest) => post<Run>(ns(`/runs/${enc(id)}/retry`), req),
    resolveTask: (id: string, ref: number, outcome: string, result?: unknown, message?: string) =>
      post<Run>(ns(`/runs/${enc(id)}/tasks/${ref}/resolve`), { outcome, result, message }),

    listWorkflows: () => get<List<Workflow>>(ns("/workflows")).then((l) => l.items ?? []),
    getWorkflow: (name: string) => get<Workflow>(ns(`/workflows/${enc(name)}`)),
    startRun: (workflow: string, inputs: Record<string, unknown>, version?: string) =>
      post<Run>(ns(`/workflows/${enc(workflow)}/runs`), { inputs, version }),
    listWorkflowVersions: (name: string) =>
      get<List<WorkflowVersion>>(ns(`/workflows/${enc(name)}/versions`)).then((l) => l.items ?? []),
    activateWorkflowVersion: (name: string, version: string) =>
      post<Workflow>(ns(`/workflows/${enc(name)}/versions/${enc(version)}/activate`)),

    listSchedules: () => get<List<Schedule>>(ns("/schedules")).then((l) => l.items ?? []),
    putSchedule: (name: string, spec: ScheduleSpec) => request<Schedule>(conn, "PUT", ns(`/schedules/${enc(name)}`), spec),
    deleteSchedule: (name: string) => request<void>(conn, "DELETE", ns(`/schedules/${enc(name)}`)),
    scheduleAction: (name: string, action: "pause" | "resume") => post<Schedule>(ns(`/schedules/${enc(name)}/${action}`)),
    triggerSchedule: (name: string) => post<Run>(ns(`/schedules/${enc(name)}/trigger`)),
    previewSchedule: (cron: string, tz: string) => post<SchedulePreview>(ns("/schedules/preview"), { cron, tz }),

    listEvents: (name = "") => get<List<FlykeEvent>>(ns(`/events?name=${enc(name)}&limit=100`)).then((l) => l.items ?? []),
    emit: (name: string, payload: Record<string, unknown>) => post<FlykeEvent>(ns("/events"), { name, payload }),

    // Workers and pools belong to a namespace. `shared` addresses the shared
    // ones (no namespace) through the cluster routes, which need the admin.
    listWorkers: () => get<List<Worker>>(ns("/workers")).then((l) => l.items ?? []),
    workerAction: (name: string, action: "cordon" | "uncordon" | "drain", shared = false) =>
      post<Worker>(fleet(shared, `/workers/${enc(name)}/${action}`)),
    deleteWorker: (name: string, shared = false) => request<void>(conn, "DELETE", fleet(shared, `/workers/${enc(name)}`)),

    listPools: () => get<List<Pool>>(ns("/pools")).then((l) => l.items ?? []),
    putPool: (name: string, spec: { selector?: Record<string, string>; maxConcurrentTasks?: number; resourceProfiles?: Record<string, Resources> }, shared = false) =>
      request<Pool>(conn, "PUT", fleet(shared, `/pools/${enc(name)}`), spec),
    deletePool: (name: string, shared = false) => request<void>(conn, "DELETE", fleet(shared, `/pools/${enc(name)}`)),
    createJoinToken: (pool: string, ttlSeconds: number, maxUses: number, shared = false) =>
      post<JoinToken>(fleet(shared, `/pools/${enc(pool)}/join-tokens`), { ttlSeconds, maxUses }),

    // Tenancy: who the token is, the namespaces it can see, and the
    // namespace's own access tokens.
    whoami: () => get<WhoAmI>("/api/v1/whoami"),
    listNamespaces: () => get<List<Namespace>>("/api/v1/namespaces").then((l) => l.items ?? []),
    createNamespace: (name: string, spec: Namespace["spec"]) => post<Namespace>("/api/v1/namespaces", { name, ...spec }),
    updateNamespace: (name: string, spec: Namespace["spec"]) => request<Namespace>(conn, "PUT", `/api/v1/namespaces/${enc(name)}`, spec),
    deleteNamespace: (name: string) => request<void>(conn, "DELETE", `/api/v1/namespaces/${enc(name)}`),
    listAccessTokens: () => get<List<AccessToken>>(ns("/tokens")).then((l) => l.items ?? []),
    createAccessToken: (name: string, ttlSeconds: number) => post<AccessToken>(ns("/tokens"), { name, ttlSeconds }),
    deleteAccessToken: (id: string) => request<void>(conn, "DELETE", ns(`/tokens/${enc(id)}`)),

    // Shared secrets live at /api/v1/secrets; Private ones under the namespace.
    // Listing the namespace returns its Private secrets plus the Shared ones.
    listSecrets: () => get<List<Secret>>(ns("/secrets")).then((l) => l.items ?? []),
    putSecret: (type: SecretType, key: string, spec: SecretSpec) =>
      request<Secret>(conn, "PUT", type === "Shared" ? `/api/v1/secrets/${enc(key)}` : ns(`/secrets/${enc(key)}`), { ...spec, type }),
    deleteSecret: (type: SecretType, key: string) =>
      request<void>(conn, "DELETE", type === "Shared" ? `/api/v1/secrets/${enc(key)}` : ns(`/secrets/${enc(key)}`)),

    // The playground is dev-only: projects live on the control plane's disk.
    playgroundInfo: () => get<PlaygroundInfo>("/api/v1/playground"),
    listProjects: () => get<List<PlaygroundProject>>("/api/v1/playground/projects").then((l) => l.items ?? []),
    getProject: (name: string) => get<PlaygroundProject>(`/api/v1/playground/projects/${enc(name)}`),
    saveProject: (name: string, files: Record<string, string>) =>
      request<PlaygroundProject>(conn, "PUT", `/api/v1/playground/projects/${enc(name)}`, { files }),
    deleteProject: (name: string) => request<void>(conn, "DELETE", `/api/v1/playground/projects/${enc(name)}`),
    runProject: (name: string, entrypoint: string, inputs: Record<string, unknown>) =>
      post<Run>(ns(`/playground/projects/${enc(name)}/runs`), { entrypoint, inputs }),
  };
}

export type Api = ReturnType<typeof api>;
