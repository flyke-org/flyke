import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";
import { useConnection, useScope } from "../api/context";
import type { AccessToken, Namespace } from "../api/types";
import { ago, dateTime } from "../components/format";
import { Card, Confirm, CopyButton, Empty, ErrorBox, Modal, Pager, usePaged } from "../components/ui";
import { useNamespaces, useWhoAmI } from "./hooks";

/** Tenancy: the admin manages every namespace here; a tenant manages the
 *  access tokens of its own. */
export function Namespaces() {
  const me = useWhoAmI();
  const admin = !!me.data?.admin;
  return (
    <>
      <div className="page-head">
        <div>
          <h1>{admin ? "Namespaces" : "Access"}</h1>
          <div className="subtitle">
            {admin
              ? "Each namespace is a tenant: its workflows, runs, events, secrets, code, pools and workers are invisible to the others."
              : "Access tokens let your team's CLI, CI and scripts use this namespace, and nothing else."}
          </div>
        </div>
      </div>
      {me.data && !me.data.authRequired && (
        <div className="alert alert-warning">
          <span>
            <strong>This control plane has no admin token, so its API is open:</strong> every caller is an admin and namespaces are not
            isolated from each other. Start it with <code>--admin-token</code> to enforce tenancy.
          </span>
        </div>
      )}
      {admin && <NamespaceTable />}
      <Tokens />
    </>
  );
}

function NamespaceTable() {
  const { client, conn, setConn } = useConnection();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const namespaces = useNamespaces();
  const { rows, pager } = usePaged(namespaces.data);
  const [editing, setEditing] = useState<Namespace | "new" | null>(null);
  const [deleting, setDeleting] = useState<Namespace | null>(null);
  const remove = useMutation({
    mutationFn: (name: string) => client.deleteNamespace(name),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["namespaces"] }),
  });

  return (
    <>
      <ErrorBox error={namespaces.error ?? remove.error} />
      <Card title="All namespaces" className="flush"
        actions={<button className="btn btn-sm btn-primary" onClick={() => setEditing("new")}>+ New namespace</button>}>
        <div className="table-wrap">
          <table className="table">
            <thead>
              <tr>
                <th>Namespace</th><th>Workers</th><th className="num">Workflows</th><th className="num">Active runs</th>
                <th className="num">Own workers</th><th className="num">Tokens</th><th className="num">Created</th><th className="actions">Actions</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((n) => (
                <tr key={n.metadata.name}>
                  <td>
                    <div className="mono">
                      {n.metadata.name}
                      {n.metadata.name === conn.namespace && <span className="badge badge-info" style={{ marginLeft: 8 }}>viewing</span>}
                    </div>
                    {n.spec.description && <div className="cell-sub">{n.spec.description}</div>}
                  </td>
                  <td>
                    {n.spec.sharedWorkers
                      ? <span className="badge badge-outline" title="Its work may also run on the shared pools">◎ own + shared</span>
                      : <span className="badge badge-good" title="Its code only runs on its own workers">▣ dedicated only</span>}
                  </td>
                  <td className="num">{n.status.workflows}</td>
                  <td className="num">{n.status.activeRuns}</td>
                  <td className="num">{n.status.workers}</td>
                  <td className="num">{n.status.tokens}</td>
                  <td className="num muted">{ago(n.metadata.createdAt)}</td>
                  <td className="actions">
                    <span className="row gap-sm end">
                      <button className="btn btn-sm" disabled={n.metadata.name === conn.namespace}
                        onClick={() => { setConn({ ...conn, namespace: n.metadata.name }); navigate("/"); }}>Open</button>
                      <button className="btn btn-sm" onClick={() => setEditing(n)}>Edit</button>
                      <button className="btn btn-sm btn-danger" disabled={n.metadata.name === "default"}
                        title={n.metadata.name === "default" ? "The default namespace cannot be deleted" : undefined}
                        onClick={() => setDeleting(n)}>Delete</button>
                    </span>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <Pager {...pager} />
      </Card>
      {editing && <NamespaceForm existing={editing === "new" ? undefined : editing} onClose={() => setEditing(null)} />}
      {deleting && (
        <Confirm title={`Delete namespace ${deleting.metadata.name}?`} danger action="Delete namespace"
          body={<>
            This permanently deletes everything the tenant owns: <strong>{deleting.status.workflows}</strong> workflow(s) and all their runs
            ({deleting.status.activeRuns} still active), its secrets, <strong>{deleting.status.tokens}</strong> access token(s), its pools
            and <strong>{deleting.status.workers}</strong> worker(s). Its tokens and workers stop working at once.
          </>}
          onConfirm={() => remove.mutate(deleting.metadata.name)} onClose={() => setDeleting(null)} />
      )}
    </>
  );
}

function NamespaceForm({ existing, onClose }: { existing?: Namespace; onClose: () => void }) {
  const { client } = useConnection();
  const qc = useQueryClient();
  const [name, setName] = useState(existing?.metadata.name ?? "");
  const [description, setDescription] = useState(existing?.spec.description ?? "");
  const [sharedWorkers, setSharedWorkers] = useState(existing?.spec.sharedWorkers ?? false);
  const save = useMutation({
    mutationFn: () => {
      const spec = { description: description.trim(), sharedWorkers };
      return existing ? client.updateNamespace(existing.metadata.name, spec) : client.createNamespace(name.trim(), spec);
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["namespaces"] });
      onClose();
    },
  });
  return (
    <Modal title={existing ? `Edit ${existing.metadata.name}` : "New namespace"} onClose={onClose}>
      <form className="stack" onSubmit={(e) => { e.preventDefault(); save.mutate(); }}>
        {!existing && (
          <label className="field">Name <span className="hint">lowercase letters, digits and -</span>
            <input value={name} onChange={(e) => setName(e.target.value.toLowerCase())} placeholder="team-payments" required
              pattern="[a-z0-9]([\-a-z0-9]{0,61}[a-z0-9])?" autoFocus />
          </label>
        )}
        <label className="field">Description <span className="hint">optional</span>
          <input value={description} onChange={(e) => setDescription(e.target.value)} maxLength={512} placeholder="Payments team" />
        </label>
        <label className="check">
          <input type="checkbox" checked={sharedWorkers} onChange={(e) => setSharedWorkers(e.target.checked)} />
          Allow this namespace to run on shared pools
        </label>
        <p className="muted" style={{ margin: 0, fontSize: 12.5 }}>
          {sharedWorkers
            ? "Its tasks may run on shared workers, in the same worker processes as other namespaces that share them. Use this for teams that trust each other."
            : "Its code and secrets only reach workers that joined one of its own pools. The team adds workers from Pools."}
        </p>
        <ErrorBox error={save.error} />
        <div className="row gap-sm end">
          <button type="button" className="btn" onClick={onClose}>Cancel</button>
          <button type="submit" className="btn btn-primary" disabled={save.isPending}>{existing ? "Save" : "Create namespace"}</button>
        </div>
      </form>
    </Modal>
  );
}

function Tokens() {
  const { client, conn } = useConnection();
  const scope = useScope();
  const qc = useQueryClient();
  const tokens = useQuery({ queryKey: [scope, "tokens"], queryFn: () => client.listAccessTokens() });
  const [creating, setCreating] = useState(false);
  const [revoking, setRevoking] = useState<AccessToken | null>(null);
  const refresh = () => {
    qc.invalidateQueries({ queryKey: [scope, "tokens"] });
    qc.invalidateQueries({ queryKey: ["namespaces"] });
  };
  const revoke = useMutation({ mutationFn: (id: string) => client.deleteAccessToken(id), onSuccess: refresh });
  const now = Date.now();
  const tok = usePaged(tokens.data);

  return (
    <>
      <ErrorBox error={tokens.error ?? revoke.error} />
      <Card title={<>Access tokens of <span className="mono">{conn.namespace}</span></>} className="flush"
        actions={<button className="btn btn-sm btn-primary" onClick={() => setCreating(true)}>+ New token</button>}>
        {tokens.data?.length === 0 ? (
          <Empty>No access tokens. Create one for the team's CLI, CI or scripts: it only works in <strong>{conn.namespace}</strong>.</Empty>
        ) : (
          <><div className="table-wrap">
            <table className="table">
              <thead><tr><th>Name</th><th>ID</th><th className="num">Created</th><th>Expires</th><th className="actions">Actions</th></tr></thead>
              <tbody>
                {tok.rows.map((t) => {
                  const expired = !!t.expiresAt && Date.parse(t.expiresAt) < now;
                  return (
                    <tr key={t.id}>
                      <td>{t.name}</td>
                      <td className="mono secondary">{t.id}</td>
                      <td className="num muted">{ago(t.createdAt)}</td>
                      <td>{expired ? <span className="badge badge-critical">✕ expired</span> : t.expiresAt ? dateTime(t.expiresAt) : <span className="muted">never</span>}</td>
                      <td className="actions"><button className="btn btn-sm btn-danger" onClick={() => setRevoking(t)}>Revoke</button></td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
          <Pager {...tok.pager} /></>
        )}
      </Card>
      {creating && <NewToken onClose={() => { setCreating(false); refresh(); }} />}
      {revoking && (
        <Confirm title={`Revoke ${revoking.name}?`} danger action="Revoke token"
          body="Anything using this token loses access immediately."
          onConfirm={() => revoke.mutate(revoking.id)} onClose={() => setRevoking(null)} />
      )}
    </>
  );
}

function NewToken({ onClose }: { onClose: () => void }) {
  const { client, conn } = useConnection();
  const [name, setName] = useState("");
  const [ttl, setTtl] = useState(0);
  const create = useMutation({ mutationFn: () => client.createAccessToken(name.trim(), ttl) });
  const tok = create.data;
  const usage = tok ? `export FLYKE_SERVER=${window.location.origin}\nexport FLYKE_NAMESPACE=${tok.namespace}\nexport FLYKE_TOKEN=${tok.token}` : "";
  return (
    <Modal title={`New access token for ${conn.namespace}`} onClose={onClose}>
      {!tok ? (
        <form className="stack" onSubmit={(e) => { e.preventDefault(); create.mutate(); }}>
          <div className="form-grid">
            <label className="field">Name <span className="hint">who or what uses it</span>
              <input value={name} onChange={(e) => setName(e.target.value)} placeholder="ci-deploy" required maxLength={100} autoFocus />
            </label>
            <label className="field">Expires
              <select value={ttl} onChange={(e) => setTtl(Number(e.target.value))}>
                <option value={0}>never</option><option value={86400}>in 24 hours</option><option value={2592000}>in 30 days</option>
                <option value={7776000}>in 90 days</option><option value={31536000}>in 1 year</option>
              </select>
            </label>
          </div>
          <ErrorBox error={create.error} />
          <div className="row gap-sm end">
            <button type="button" className="btn" onClick={onClose}>Cancel</button>
            <button type="submit" className="btn btn-primary" disabled={create.isPending}>Create token</button>
          </div>
        </form>
      ) : (
        <div className="stack">
          <div className="alert alert-warning"><span><strong>Shown once.</strong> It gives full access to namespace <strong>{tok.namespace}</strong> and to nothing else.</span></div>
          <div className="stack" style={{ gap: 6 }}>
            <div className="row between"><strong>Token</strong><CopyButton text={tok.token ?? ""} /></div>
            <pre className="json" style={{ overflowWrap: "anywhere", whiteSpace: "pre-wrap" }}>{tok.token}</pre>
          </div>
          <div className="stack" style={{ gap: 6 }}>
            <div className="row between"><strong>Use it with the CLI</strong><CopyButton text={usage} /></div>
            <pre className="json">{usage}</pre>
          </div>
          <div className="row end"><button className="btn" onClick={onClose}>Done</button></div>
        </div>
      )}
    </Modal>
  );
}
