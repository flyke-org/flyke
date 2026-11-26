import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useConnection, useScope } from "../api/context";
import type { Secret, SecretType } from "../api/types";
import { ago, dateTime } from "../components/format";
import { Card, Confirm, CopyButton, Empty, ErrorBox, Modal, Pager, usePaged } from "../components/ui";
import { useWhoAmI } from "./hooks";

type Filter = "all" | SecretType;

const KEY_RE = /^[A-Za-z_][A-Za-z0-9_.-]{0,252}$/;

export function Secrets() {
  const { client, conn } = useConnection();
  const scope = useScope();
  const qc = useQueryClient();
  const secrets = useQuery({ queryKey: [scope, "secrets"], queryFn: () => client.listSecrets(), refetchInterval: 10000 });
  const [filter, setFilter] = useState<Filter>("all");
  const admin = !!useWhoAmI().data?.admin;
  const [editing, setEditing] = useState<Secret | "new" | null>(null);
  const [deleting, setDeleting] = useState<Secret | null>(null);
  const remove = useMutation({
    mutationFn: (s: Secret) => client.deleteSecret(s.type, s.key),
    onSuccess: () => qc.invalidateQueries({ queryKey: [scope, "secrets"] }),
  });

  const all = secrets.data ?? [];
  // A Private secret overrides a Shared one with the same key for this namespace.
  const overridden = useMemo(() => new Set(all.filter((s) => s.type === "Private").map((s) => s.key)), [all]);
  const filtered = all.filter((s) => filter === "all" || s.type === filter);
  const { rows, pager } = usePaged(filtered, filter);

  return (
    <>
      <div className="page-head">
        <div>
          <h1>Secrets</h1>
          <div className="subtitle">
            Values tasks read with <code>secrets["key"]</code>. Sensitive secrets are encrypted and never displayed; Shared ones are visible from every namespace.
          </div>
        </div>
        <button className="btn btn-primary" onClick={() => setEditing("new")}>+ New secret</button>
      </div>
      <div className="row between" style={{ marginBottom: 12 }}>
        <div className="segmented" role="radiogroup" aria-label="Filter by type">
          {(["all", "Private", "Shared"] as Filter[]).map((f) => (
            <button key={f} className={filter === f ? "on" : ""} aria-pressed={filter === f} onClick={() => setFilter(f)}>
              {f === "all" ? "All" : f === "Private" ? `Namespace ${conn.namespace}` : "Shared"}
            </button>
          ))}
        </div>
      </div>
      <ErrorBox error={secrets.error ?? remove.error} />
      <Card className="flush">
        {secrets.isSuccess && filtered.length === 0 ? (
          <Empty>
            {all.length === 0 ? <>No secrets yet. Create one, or run <code>flyke secrets set KEY VALUE</code>.</> : "No secrets match this filter."}
          </Empty>
        ) : (
          <><div className="table-wrap">
            <table className="table">
              <thead><tr><th>Key</th><th>Type</th><th>Kind</th><th>Value</th><th className="num">Updated</th><th className="actions">Actions</th></tr></thead>
              <tbody>
                {rows.map((s) => (
                  <tr key={`${s.type}/${s.key}`}>
                    <td className="mono">{s.key}</td>
                    <td>
                      {s.type === "Shared"
                        ? <span className="badge badge-info" title="Visible from every namespace">◎ Shared</span>
                        : <span className="badge badge-outline" title={`Only visible from namespace ${s.namespace}`}>Private</span>}
                      {s.type === "Shared" && overridden.has(s.key) && (
                        <span className="muted" style={{ marginLeft: 6, fontSize: 12 }} title={`A Private secret in ${conn.namespace} takes precedence`}>overridden</span>
                      )}
                    </td>
                    <td>
                      {s.sensitive
                        ? <span className="badge badge-warning">⚿ Sensitive</span>
                        : <span className="badge badge-muted">Variable</span>}
                    </td>
                    <td>
                      {s.sensitive ? <span className="muted" aria-label="hidden value">••••••••</span> : (
                        <span className="row gap-sm">
                          <span className="mono" style={{ maxWidth: 320, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }} title={s.value}>{s.value || <span className="muted">(empty)</span>}</span>
                          {s.value ? <CopyButton text={s.value} /> : null}
                        </span>
                      )}
                    </td>
                    <td className="num muted" title={dateTime(s.updatedAt)}>{ago(s.updatedAt)}</td>
                    <td className="actions">
                      {admin || s.type !== "Shared" ? (
                        <span className="row gap-sm end">
                          <button className="btn btn-sm" onClick={() => setEditing(s)}>Edit</button>
                          <button className="btn btn-sm btn-danger" onClick={() => setDeleting(s)}>Delete</button>
                        </span>
                      ) : <span className="muted" style={{ fontSize: 12 }}>managed by the admin</span>}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <Pager {...pager} /></>
        )}
      </Card>
      {editing && <SecretForm existing={editing === "new" ? undefined : editing} onClose={() => setEditing(null)} />}
      {deleting && (
        <Confirm
          title={`Delete ${deleting.key}?`}
          danger
          action="Delete"
          body={deleting.type === "Shared"
            ? <>This Shared secret is removed for <strong>every namespace</strong>. Runs that read it will fail.</>
            : <>Runs in namespace <strong>{conn.namespace}</strong> that read it will fail{all.some((s) => s.type === "Shared" && s.key === deleting.key) ? " — or fall back to the Shared secret with the same key" : ""}.</>}
          onConfirm={() => remove.mutate(deleting)}
          onClose={() => setDeleting(null)}
        />
      )}
    </>
  );
}

function SecretForm({ existing, onClose }: { existing?: Secret; onClose: () => void }) {
  const { client, conn } = useConnection();
  const scope = useScope();
  const qc = useQueryClient();
  const [key, setKey] = useState(existing?.key ?? "");
  const [type, setType] = useState<SecretType>(existing?.type ?? "Private");
  // Shared secrets are visible from every namespace: only the admin writes them.
  const admin = !!useWhoAmI().data?.admin;
  const [sensitive, setSensitive] = useState(existing?.sensitive ?? true);
  // Sensitive values are never sent to the browser: editing one means replacing it.
  const [value, setValue] = useState(existing && !existing.sensitive ? existing.value ?? "" : "");
  const [reveal, setReveal] = useState(false);
  const [err, setErr] = useState<Error | null>(null);
  const keepValue = !!existing?.sensitive && sensitive && value === "";

  const save = useMutation({
    mutationFn: () => client.putSecret(type, key.trim(), keepValue ? { sensitive } : { value, sensitive }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: [scope, "secrets"] });
      onClose();
    },
  });
  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    setErr(null);
    if (!KEY_RE.test(key.trim())) {
      setErr(new Error("Key: letters, digits, '_', '.' and '-' only, not starting with a digit."));
      return;
    }
    if (existing?.sensitive && !sensitive && value === "") {
      setErr(new Error("Enter the value to store it as a plain variable: the sensitive value cannot be read back."));
      return;
    }
    save.mutate();
  };
  const snippet = `from flyke import secrets\n\n${(key.trim() || "my_key").replace(/[^A-Za-z0-9_]/g, "_")} = secrets["${key.trim() || "my_key"}"]`;

  return (
    <Modal title={existing ? `Edit ${existing.key}` : "New secret"} onClose={onClose}>
      <form className="stack" onSubmit={submit}>
        <label className="field">Key
          <input value={key} onChange={(e) => setKey(e.target.value)} placeholder="infoblox_password" required disabled={!!existing} autoFocus={!existing} className="mono" />
        </label>
        <div className="stack" style={{ gap: 6 }}>
          <span className="secondary" style={{ fontSize: 12.5, fontWeight: 500 }}>Kind</span>
          <div className="segmented" role="radiogroup" aria-label="Kind">
            <button type="button" className={sensitive ? "on" : ""} aria-pressed={sensitive} onClick={() => setSensitive(true)}>⚿ Sensitive</button>
            <button type="button" className={!sensitive ? "on" : ""} aria-pressed={!sensitive} onClick={() => setSensitive(false)}>Variable</button>
          </div>
          <span className="muted" style={{ fontSize: 12 }}>
            {sensitive
              ? "Passwords, tokens, keys: encrypted at rest, never displayed again, masked in task logs."
              : "Plain configuration such as usernames or URLs: stored and displayed as-is."}
          </span>
        </div>
        {!existing && admin && (
          <div className="stack" style={{ gap: 6 }}>
            <span className="secondary" style={{ fontSize: 12.5, fontWeight: 500 }}>Visibility</span>
            <div className="segmented" role="radiogroup" aria-label="Visibility">
              <button type="button" className={type === "Private" ? "on" : ""} aria-pressed={type === "Private"} onClick={() => setType("Private")}>Namespace {conn.namespace}</button>
              <button type="button" className={type === "Shared" ? "on" : ""} aria-pressed={type === "Shared"} onClick={() => setType("Shared")}>◎ Shared (all namespaces)</button>
            </div>
          </div>
        )}
        <label className="field">Value
          {existing?.sensitive && <span className="hint">the current value is hidden; leave empty to keep it</span>}
          {sensitive ? (
            <span className="row gap-sm">
              <input type={reveal ? "text" : "password"} value={value} onChange={(e) => setValue(e.target.value)} autoComplete="new-password" style={{ flex: 1 }} className="mono"
                placeholder={existing?.sensitive ? "••••••••  (unchanged)" : ""} required={!existing?.sensitive} />
              <button type="button" className="btn btn-sm btn-ghost" onClick={() => setReveal((r) => !r)} aria-label={reveal ? "Hide value" : "Show value"}>{reveal ? "Hide" : "Show"}</button>
            </span>
          ) : (
            <textarea value={value} onChange={(e) => setValue(e.target.value)} rows={2} className="mono" spellCheck={false} />
          )}
        </label>
        <div className="stack" style={{ gap: 6 }}>
          <div className="row between"><span className="muted" style={{ fontSize: 12 }}>Read it inside a task</span><CopyButton text={snippet} /></div>
          <pre className="json" style={{ margin: 0 }}>{snippet}</pre>
        </div>
        <ErrorBox error={err ?? save.error} />
        <div className="row gap-sm end">
          <button type="button" className="btn" onClick={onClose}>Cancel</button>
          <button type="submit" className="btn btn-primary" disabled={save.isPending}>{existing ? "Save" : "Create secret"}</button>
        </div>
      </form>
    </Modal>
  );
}
