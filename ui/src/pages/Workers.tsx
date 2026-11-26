import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useConnection, useScope } from "../api/context";
import type { Worker } from "../api/types";
import { ago, bytes } from "../components/format";
import { Card, Confirm, Empty, ErrorBox, Labels, Meter, Pager, useNow, usePaged } from "../components/ui";
import { useWhoAmI, useWorkers } from "./hooks";
import { PoolsSection } from "./Pools";

/** Shared workers have no namespace; only the admin manages them. */
const isShared = (w: Worker) => !w.metadata.namespace;

function WorkerStatus({ w }: { w: Worker }) {
  if (w.status.phase !== "Ready") return <span className="badge badge-critical">✕ NotReady</span>;
  if (w.spec.draining) return <span className="badge badge-serious">⇣ Draining</span>;
  if (w.spec.unschedulable) return <span className="badge badge-warning">⊘ Cordoned</span>;
  return <span className="badge badge-good">✓ Ready</span>;
}

/** A pool name with its scope underneath: shared across namespaces, or owned by one. */
export function PoolCell({ name, namespace }: { name: string; namespace?: string }) {
  return (
    <div className="pool-cell">
      <span className="mono">{name || "—"}</span>
      {namespace
        ? <span className="scope scope-own" title="Its workers only run this namespace's work">▣ {namespace}</span>
        : <span className="scope scope-shared" title="Run by the admin; also runs work of other namespaces">◎ shared</span>}
    </div>
  );
}

export function Workers() {
  const workers = useWorkers();
  const { client } = useConnection();
  const scope = useScope();
  const qc = useQueryClient();
  const now = useNow(1000);
  const admin = !!useWhoAmI().data?.admin;
  const { rows, pager } = usePaged(workers.data);
  const [confirm, setConfirm] = useState<{ w: Worker; action: "drain" | "remove" } | null>(null);
  const act = useMutation({
    mutationFn: async ({ w, action }: { w: Worker; action: "cordon" | "uncordon" | "drain" | "remove" }): Promise<void> => {
      if (action === "remove") await client.deleteWorker(w.metadata.name, isShared(w));
      else await client.workerAction(w.metadata.name, action, isShared(w));
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: [scope, "workers"] }),
  });

  return (
    <>
      <div className="page-head">
        <div>
          <h1>Workers</h1>
          <div className="subtitle">The workers that run this namespace's work. They connect out to the control plane and are grouped into pools, listed below.</div>
        </div>
      </div>
      <ErrorBox error={workers.error ?? act.error} />
      <Card className="flush" title="Workers">
        {workers.data?.length === 0 ? (
          <Empty>No workers registered. Use <strong>+ Add worker</strong> on a pool below to get a join command for a host.</Empty>
        ) : (
          <><div className="table-wrap">
            <table className="table">
              <thead><tr><th>Worker</th><th>Status</th><th>Pool</th><th>Slots busy</th><th>Capacity</th><th>Runtimes</th><th>Labels</th><th className="num">Heartbeat</th><th className="actions">Actions</th></tr></thead>
              <tbody>
                {rows.map((w) => (
                  <tr key={w.metadata.name}>
                    <td>
                      <div className="mono">{w.metadata.name}</div>
                      <div className="cell-sub">{w.status.hostname} · {w.status.agentVersion}</div>
                    </td>
                    <td><WorkerStatus w={w} /></td>
                    <td><PoolCell name={w.status.pool} namespace={w.metadata.namespace} /></td>
                    <td><Meter used={w.status.slots - w.status.freeSlots} total={w.status.slots} label="busy / total slots" /></td>
                    <td className="secondary nowrap">{w.status.capacity.cpu ?? "—"} cpu · {bytes(w.status.capacity.memoryBytes)}</td>
                    <td>{w.status.runtimes.length ? <span className="chips">{w.status.runtimes.map((r) => <span key={r} className="chip">{r}</span>)}</span> : <span className="muted">—</span>}</td>
                    <td><Labels labels={w.metadata.labels} /></td>
                    <td className="num muted">{ago(w.status.lastHeartbeat, now)}</td>
                    <td className="actions">
                      {admin || !isShared(w) ? (
                        <span className="row gap-sm end">
                          {w.spec.unschedulable
                            ? <button className="btn btn-sm" onClick={() => act.mutate({ w, action: "uncordon" })}>Uncordon</button>
                            : <button className="btn btn-sm" onClick={() => act.mutate({ w, action: "cordon" })} title="Stop scheduling new work">Cordon</button>}
                          <button className="btn btn-sm" onClick={() => setConfirm({ w, action: "drain" })}>Drain</button>
                          <button className="btn btn-sm btn-danger" onClick={() => setConfirm({ w, action: "remove" })}>Remove</button>
                        </span>
                      ) : <span className="cell-note">managed by the admin</span>}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <Pager {...pager} /></>
        )}
      </Card>
      <PoolsSection />
      {confirm && (
        <Confirm
          title={confirm.action === "drain" ? `Drain ${confirm.w.metadata.name}?` : `Remove ${confirm.w.metadata.name}?`}
          action={confirm.action === "drain" ? "Drain" : "Remove worker"}
          danger={confirm.action === "remove"}
          body={confirm.action === "drain"
            ? "The worker finishes its running work, accepts nothing new, then exits."
            : "Its credential is revoked immediately and its in-flight work is treated as lost (retried, or paused for at-most-once tasks)."}
          onConfirm={() => act.mutate({ w: confirm.w, action: confirm.action })}
          onClose={() => setConfirm(null)}
        />
      )}
    </>
  );
}
