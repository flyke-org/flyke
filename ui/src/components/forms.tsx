import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";
import { useConnection, useScope } from "../api/context";
import type { JSONSchema, Workflow } from "../api/types";
import { ErrorBox, Modal } from "./ui";

function kind(s: JSONSchema): string {
  if (s.enum) return "enum";
  const t = Array.isArray(s.type) ? s.type[0] : s.type;
  return t ?? "any";
}

function initial(s: JSONSchema): string {
  if (s.default === undefined) return "";
  if (kind(s) === "array" && Array.isArray(s.default) && s.default.every((v) => typeof v !== "object")) return s.default.join(", ");
  if (typeof s.default === "object") return JSON.stringify(s.default);
  return String(s.default);
}

function parse(s: JSONSchema, raw: string): unknown {
  const k = kind(s);
  const v = raw.trim();
  switch (k) {
    case "boolean": return v === "true";
    case "integer": {
      const n = Number(v);
      if (!Number.isInteger(n)) throw new Error(`expected an integer, got "${raw}"`);
      return n;
    }
    case "number": {
      const n = Number(v);
      if (Number.isNaN(n)) throw new Error(`expected a number, got "${raw}"`);
      return n;
    }
    case "array":
      if (v.startsWith("[")) return JSON.parse(v);
      return v ? v.split(",").map((x) => {
        const item = x.trim();
        return s.items && ["integer", "number"].includes(kind(s.items)) ? Number(item) : item;
      }) : [];
    case "object":
    case "any":
      return v ? JSON.parse(v) : undefined;
    default:
      return raw;
  }
}

/** Form generated from a workflow's inputs JSON Schema. */
export function StartRunForm({ workflow, version, onDone }: {
  workflow: Pick<Workflow, "metadata"> & { spec: Pick<Workflow["spec"], "manifest"> };
  /** Run this registered version instead of the current one. */
  version?: string;
  onDone?: () => void;
}) {
  const { client } = useConnection();
  const scope = useScope();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const start = useMutation({
    mutationFn: (inputs: Record<string, unknown>) => client.startRun(workflow.metadata.name, inputs, version),
    onSuccess: (run) => {
      qc.invalidateQueries({ queryKey: [scope, "runs"] });
      onDone?.();
      navigate(`/runs/${run.metadata.name}`);
    },
  });
  return (
    <InputsForm schema={workflow.spec.manifest.inputs_schema} pending={start.isPending} error={start.error}
      onSubmit={(inputs) => start.mutate(inputs)} />
  );
}

/** Inputs form for a JSON Schema; `onSubmit` receives the parsed values. */
export function InputsForm({ schema, onSubmit, pending, error: submitError, submitLabel = "▶ Start run", footer, initialValues }: {
  schema: JSONSchema;
  /** Values to start from (editing), instead of the schema defaults. */
  initialValues?: Record<string, unknown>;
  onSubmit: (inputs: Record<string, unknown>) => void;
  pending?: boolean;
  error?: unknown;
  submitLabel?: string;
  footer?: React.ReactNode;
}) {
  const props = Object.entries(schema.properties ?? {});
  const required = new Set(schema.required ?? []);
  const [values, setValues] = useState<Record<string, string>>(() =>
    Object.fromEntries(props.map(([k, s]) => [k, initialValues && k in initialValues ? initial({ ...s, default: initialValues[k] }) : initial(s)])),
  );
  const [error, setError] = useState<Error | null>(null);

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    try {
      const inputs: Record<string, unknown> = {};
      for (const [k, s] of props) {
        const raw = values[k] ?? initial(s);
        if (raw === "" && !required.has(k) && kind(s) !== "boolean") continue;
        if (raw === "" && required.has(k)) throw new Error(`${k} is required`);
        inputs[k] = parse(s, raw);
      }
      onSubmit(inputs);
    } catch (err) {
      setError(err as Error);
    }
  };

  return (
    <form className="stack" onSubmit={submit}>
      {props.length === 0 && <p className="muted" style={{ margin: 0 }}>This workflow takes no inputs.</p>}
      <div className="form-grid">
        {props.map(([name, s]) => {
          const k = kind(s);
          const value = values[name] ?? initial(s);
          const set = (v: string) => setValues((prev) => ({ ...prev, [name]: v }));
          const label = <span>{name}{required.has(name) ? " *" : ""} <span className="hint">· {k === "enum" ? "choice" : k}</span></span>;
          if (k === "boolean") {
            return (
              <label key={name} className="check" style={{ alignSelf: "end", paddingBottom: 6 }}>
                <input type="checkbox" checked={value === "true"} onChange={(e) => set(String(e.target.checked))} />
                {name}
              </label>
            );
          }
          if (k === "enum") {
            return (
              <label key={name} className="field">{label}
                <select value={value} onChange={(e) => set(e.target.value)}>
                  {!required.has(name) && s.default === undefined && <option value="">—</option>}
                  {s.enum!.map((o) => <option key={String(o)} value={String(o)}>{String(o)}</option>)}
                </select>
              </label>
            );
          }
          return (
            <label key={name} className="field">{label}
              <input
                value={value}
                onChange={(e) => set(e.target.value)}
                placeholder={k === "array" ? "comma,separated or JSON" : k === "object" ? "{ JSON }" : ""}
                inputMode={k === "integer" || k === "number" ? "decimal" : undefined}
              />
            </label>
          );
        })}
      </div>
      <ErrorBox error={error ?? submitError} />
      <div className="row gap-sm end">
        {footer}
        <button className="btn btn-primary" type="submit" disabled={pending}>{submitLabel}</button>
      </div>
    </form>
  );
}

function payloadFrom(rows: [string, string][]): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const [k, v] of rows) {
    if (!k.trim()) continue;
    try {
      const parsed = JSON.parse(v);
      out[k.trim()] = typeof parsed === "string" ? v : parsed;
    } catch {
      out[k.trim()] = v;
    }
  }
  return out;
}

/** Publishes an event. Used for approvals: prefilled from a run's wait. */
export function EmitDialog({ name: initialName = "", fields = {}, onClose }: {
  name?: string; fields?: Record<string, unknown>; onClose: () => void;
}) {
  const { client } = useConnection();
  const scope = useScope();
  const qc = useQueryClient();
  const [name, setName] = useState(initialName);
  const [rows, setRows] = useState<[string, string][]>(() => {
    const r = Object.entries(fields).map(([k, v]): [string, string] => [k, typeof v === "string" ? v : JSON.stringify(v)]);
    return [...r, ["", ""]];
  });
  const emit = useMutation({
    mutationFn: () => client.emit(name.trim(), payloadFrom(rows)),
    onSuccess: () => qc.invalidateQueries({ queryKey: [scope] }),
  });

  return (
    <Modal title="Emit event" onClose={onClose}>
      <form className="stack" onSubmit={(e) => { e.preventDefault(); emit.mutate(); }}>
        <label className="field">Event name
          <input value={name} onChange={(e) => setName(e.target.value)} placeholder="deploy.approved" autoFocus={!initialName} required />
        </label>
        <div className="stack" style={{ gap: 6 }}>
          <span className="secondary" style={{ fontSize: 12.5 }}>Payload fields</span>
          {rows.map(([k, v], i) => (
            <div className="row gap-sm" key={i}>
              <input placeholder="key" value={k} style={{ width: "35%" }}
                onChange={(e) => setRows((r) => r.map((x, j) => (j === i ? [e.target.value, x[1]] : x)))} />
              <input placeholder="value" value={v} className="grow" autoFocus={!!initialName && i === rows.length - 1}
                onChange={(e) => setRows((r) => {
                  const next = r.map((x, j): [string, string] => (j === i ? [x[0], e.target.value] : x));
                  return i === r.length - 1 ? [...next, ["", ""]] : next;
                })} />
            </div>
          ))}
        </div>
        <ErrorBox error={emit.error} />
        {emit.data && (
          <div className="alert alert-info">
            ✓ Published <code>{emit.data.name}</code> —{" "}
            {emit.data.matched?.length ? <>resumed/started {emit.data.matched.join(", ")}</> : "no run matched"}
          </div>
        )}
        <div className="row gap-sm end">
          <button type="button" className="btn" onClick={onClose}>Close</button>
          <button type="submit" className="btn btn-primary" disabled={emit.isPending || !name.trim()}>Emit</button>
        </div>
      </form>
    </Modal>
  );
}
