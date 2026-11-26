import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useConnection, useScope } from "../api/context";
import type { JoinToken, Pool, Resources } from "../api/types";
import { bytes, dateTime } from "../components/format";
import { Card, CopyButton, Empty, ErrorBox, Labels, Modal, Pager, usePaged } from "../components/ui";
import { useWhoAmI } from "./hooks";
import { PoolCell } from "./Workers";

/** Shared pools have no namespace; only the admin manages them. */
const isShared = (p: Pool) => !p.metadata.namespace;

function parseSize(s: string): number {
  const m = /^(\d+(?:\.\d+)?)\s*(Ki|Mi|Gi|Ti|K|M|G|T)?$/i.exec(s.trim());
  if (!m) throw new Error(`invalid size "${s}" (e.g. 512Mi, 8Gi)`);
  const mult: Record<string, number> = { ki: 2 ** 10, mi: 2 ** 20, gi: 2 ** 30, ti: 2 ** 40, k: 1e3, m: 1e6, g: 1e9, t: 1e12 };
  return Math.round(parseFloat(m[1]) * (m[2] ? mult[m[2].toLowerCase()] : 1));
}

/** The pools card of the Workers page. */
export function PoolsSection() {
  const { client, conn } = useConnection();
  const scope = useScope();
  const pools = useQuery({ queryKey: [scope, "pools"], queryFn: () => client.listPools(), refetchInterval: 3000 });
  const [creating, setCreating] = useState(false);
  const [joining, setJoining] = useState<Pool | null>(null);
  const admin = !!useWhoAmI().data?.admin;
  const { rows, pager } = usePaged(pools.data);

  return (
    <>
      <ErrorBox error={pools.error} />
      <Card className="flush" title="Pools"
        actions={<button className="btn btn-sm btn-primary" onClick={() => setCreating(true)}>+ New pool</button>}>
        <p className="secondary" style={{ margin: "0 16px 12px", fontSize: 12.5 }}>
          Groups of workers selected by labels. Pools created here belong to namespace <strong>{conn.namespace}</strong>: their workers
          only run its work. Shared pools are run by the admin. Use <strong>+ Add worker</strong> on a pool to join a host.
        </p>
        {pools.data?.length === 0 ? <Empty>No pools yet. Create one, then add workers to it.</Empty> : (
          <><div className="table-wrap">
            <table className="table">
              <thead><tr><th>Pool</th><th>Selector</th><th className="num">Ready</th><th className="num">Free slots</th><th className="num">Max concurrent</th><th>Resource profiles</th><th className="actions">Actions</th></tr></thead>
              <tbody>
                {rows.map((p) => <PoolRow key={p.metadata.name} p={p} canManage={admin || !isShared(p)} onJoin={() => setJoining(p)} />)}
              </tbody>
            </table>
          </div>
          <Pager {...pager} /></>
        )}
      </Card>
      {creating && <CreatePool admin={admin} onClose={() => setCreating(false)} />}
      {joining && <JoinDialog pool={joining} onClose={() => setJoining(null)} />}
    </>
  );
}

function PoolRow({ p, canManage, onJoin }: { p: Pool; canManage: boolean; onJoin: () => void }) {
  const profiles = Object.entries(p.spec.resourceProfiles ?? {});
  return (
    <tr>
      <td><PoolCell name={p.metadata.name} namespace={p.metadata.namespace} /></td>
      <td><Labels labels={p.spec.selector} /></td>
      <td className="num">{p.status.readyWorkers}/{p.status.workers}</td>
      <td className="num">{p.status.freeSlots}</td>
      <td className="num">{p.spec.maxConcurrentTasks || <span className="muted">unlimited</span>}</td>
      <td>
        {profiles.length === 0 ? <span className="muted">—</span> : (
          <span className="chips">
            {profiles.map(([n, r]) => <span key={n} className="chip">{n}: {r.cpu ?? 0} cpu · {bytes(r.memoryBytes)}</span>)}
          </span>
        )}
      </td>
      <td className="actions">
        {canManage
          ? <button className="btn btn-sm btn-primary" onClick={onJoin}>+ Add worker</button>
          : <span className="cell-note">managed by the admin</span>}
      </td>
    </tr>
  );
}

function CreatePool({ admin, onClose }: { admin: boolean; onClose: () => void }) {
  const { client, conn } = useConnection();
  const [shared, setShared] = useState(false);
  const scope = useScope();
  const qc = useQueryClient();
  const [name, setName] = useState("");
  const [selector, setSelector] = useState("");
  const [max, setMax] = useState("");
  const [profiles, setProfiles] = useState<{ name: string; cpu: string; memory: string }[]>([{ name: "", cpu: "", memory: "" }]);
  const [err, setErr] = useState<Error | null>(null);
  const save = useMutation({
    mutationFn: (spec: Parameters<typeof client.putPool>[1]) => client.putPool(name.trim(), spec, shared),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: [scope, "pools"] });
      onClose();
    },
  });
  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    setErr(null);
    try {
      const sel: Record<string, string> = {};
      for (const part of selector.split(",").map((s) => s.trim()).filter(Boolean)) {
        const [k, v] = part.split("=");
        if (!k || v === undefined) throw new Error(`invalid selector "${part}" (use key=value)`);
        sel[k] = v;
      }
      const rp: Record<string, Resources> = {};
      for (const pr of profiles.filter((x) => x.name.trim())) {
        rp[pr.name.trim()] = { cpu: pr.cpu ? Number(pr.cpu) : undefined, memoryBytes: pr.memory ? parseSize(pr.memory) : undefined };
      }
      save.mutate({
        selector: Object.keys(sel).length ? sel : undefined,
        maxConcurrentTasks: max ? Number(max) : undefined,
        resourceProfiles: Object.keys(rp).length ? rp : undefined,
      });
    } catch (e2) {
      setErr(e2 as Error);
    }
  };
  return (
    <Modal title="New pool" onClose={onClose}>
      <form className="stack" onSubmit={submit}>
        <label className="field">Name <input value={name} onChange={(e) => setName(e.target.value)} placeholder="builders" required pattern="[a-z0-9]([-a-z0-9]*[a-z0-9])?" autoFocus /></label>
        {admin && (
          <label className="check">
            <input type="checkbox" checked={shared} onChange={(e) => setShared(e.target.checked)} />
            Shared pool <span className="muted">— serves every namespace allowed to use shared workers, instead of only {conn.namespace}</span>
          </label>
        )}
        <label className="field">Label selector <span className="hint">optional - default <code>flyke.io/pool=&lt;name&gt;</code></span>
          <input value={selector} onChange={(e) => setSelector(e.target.value)} placeholder="gpu=a10,region=eu" />
        </label>
        <label className="field">Max concurrent tasks <span className="hint">optional</span>
          <input value={max} onChange={(e) => setMax(e.target.value)} inputMode="numeric" placeholder="unlimited" />
        </label>
        <div className="stack" style={{ gap: 6 }}>
          <span className="secondary" style={{ fontSize: 12.5 }}>Resource profiles <span className="muted">— referenced by tasks, e.g. <code>@task(resources="heavy")</code></span></span>
          {profiles.map((pr, i) => (
            <div className="row gap-sm" key={i}>
              <input placeholder="heavy" value={pr.name} onChange={(e) => setProfiles((ps) => ps.map((x, j) => (j === i ? { ...x, name: e.target.value } : x)))} />
              <input placeholder="cpu" value={pr.cpu} style={{ width: 80 }} onChange={(e) => setProfiles((ps) => ps.map((x, j) => (j === i ? { ...x, cpu: e.target.value } : x)))} />
              <input placeholder="memory (8Gi)" value={pr.memory} style={{ width: 130 }} onChange={(e) => setProfiles((ps) => ps.map((x, j) => (j === i ? { ...x, memory: e.target.value } : x)))} />
            </div>
          ))}
          <button type="button" className="btn btn-ghost btn-sm" style={{ alignSelf: "start" }} onClick={() => setProfiles((p) => [...p, { name: "", cpu: "", memory: "" }])}>+ profile</button>
        </div>
        <ErrorBox error={err ?? save.error} />
        <div className="row gap-sm end">
          <button type="button" className="btn" onClick={onClose}>Cancel</button>
          <button type="submit" className="btn btn-primary" disabled={save.isPending}>Create pool</button>
        </div>
      </form>
    </Modal>
  );
}

function JoinDialog({ pool: p, onClose }: { pool: Pool; onClose: () => void }) {
  const pool = p.metadata.name;
  const { client } = useConnection();
  const [ttl, setTtl] = useState(3600);
  const [maxUses, setMaxUses] = useState(1);
  const create = useMutation({ mutationFn: () => client.createJoinToken(pool, ttl, maxUses, isShared(p)) });
  const url = window.location.origin;
  const tok: JoinToken | undefined = create.data;
  const pip = tok ? `flyke-worker join --url ${url} --token ${tok.token}` : "";
  const docker = tok ? `docker run -d --restart=always -v flyke-worker:/var/lib/flyke-worker \\\n  ghcr.io/flyke-org/worker-python join --url ${url} --token ${tok.token}` : "";

  return (
    <Modal title={`Add a worker to ${pool}`} onClose={onClose}>
      {!tok ? (
        <form className="stack" onSubmit={(e) => { e.preventDefault(); create.mutate(); }}>
          <p className="secondary" style={{ margin: 0 }}>
            {isShared(p)
              ? <>Workers that join <strong>{pool}</strong> are <strong>shared</strong>: they run work of every namespace allowed to use shared workers. </>
              : <>Workers that join <strong>{pool}</strong> only run work of namespace <strong>{p.metadata.namespace}</strong>. </>}
            A join token lets a host register itself once. The host then gets its own revocable credential; the token itself never becomes an identity.
          </p>
          <div className="form-grid">
            <label className="field">Valid for
              <select value={ttl} onChange={(e) => setTtl(Number(e.target.value))}>
                <option value={900}>15 minutes</option><option value={3600}>1 hour</option><option value={86400}>24 hours</option><option value={604800}>7 days</option>
              </select>
            </label>
            <label className="field">Max registrations
              <select value={maxUses} onChange={(e) => setMaxUses(Number(e.target.value))}>
                <option value={1}>1 host</option><option value={5}>5 hosts</option><option value={25}>25 hosts</option><option value={0}>unlimited</option>
              </select>
            </label>
          </div>
          <ErrorBox error={create.error} />
          <div className="row gap-sm end">
            <button type="button" className="btn" onClick={onClose}>Cancel</button>
            <button type="submit" className="btn btn-primary" disabled={create.isPending}>Create join token</button>
          </div>
        </form>
      ) : (
        <div className="stack">
          <div className="alert alert-warning"><strong>Shown once.</strong> Expires {dateTime(tok.expiresAt)} - {tok.maxUses ? `${tok.maxUses} registration(s)` : "unlimited registrations"}.</div>
          <div className="stack" style={{ gap: 6 }}>
            <div className="row between"><strong>On a Linux/macOS host</strong><CopyButton text={pip} /></div>
            <pre className="json">{pip}</pre>
          </div>
          <div className="stack" style={{ gap: 6 }}>
            <div className="row between"><strong>With Docker</strong><CopyButton text={docker} /></div>
            <pre className="json">{docker}</pre>
          </div>
          <p className="muted" style={{ margin: 0, fontSize: 12.5 }}>Add <code>--labels key=value</code> to label the worker. It will appear under Workers within seconds.</p>
          <div className="row end"><button className="btn" onClick={onClose}>Done</button></div>
        </div>
      )}
    </Modal>
  );
}
