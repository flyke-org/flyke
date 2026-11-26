import { Fragment, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import Editor, { type OnMount } from "@monaco-editor/react";
import { useConnection, useScope } from "../api/context";
import type { PlaygroundError, PlaygroundProject, PlaygroundWorkflow } from "../api/types";
import { InputsForm } from "../components/forms";
import { LogViewer } from "../components/LogViewer";
import { WorkflowFlow } from "../components/RunFlow";
import { Icon } from "../components/icons";
import { languageOf, monaco, registerFlykeLanguageFeatures } from "../components/monaco";
import { download, zip } from "../components/zip";
import { Card, Confirm, Empty, ErrorBox, Json, Modal, PhaseBadge } from "../components/ui";
import { isLive } from "./hooks";

type Files = Record<string, string>;

const STARTER: Files = {
  "main.py": `"""Edit me, then press ▶ Run (⌘/Ctrl+Enter). ⌘/Ctrl+S saves.

Type "workflow" or "task" for snippets.
"""

import asyncio

from flyke import Retry, task, workflow

from helpers import shout


@workflow
class Hello:
    """Greets someone in several languages, in parallel."""

    name: str = "world"
    languages: list[str] = ["en", "fr", "es"]

    @task(retry=Retry(attempts=3))
    async def greet(self, lang: str) -> str:
        word = {"en": "hello", "fr": "bonjour", "es": "hola"}.get(lang, "hi")
        self.log.info("greeting", lang=lang, name=self.name)
        return f"{word} {self.name}"

    async def run(self) -> list[str]:
        greetings = await asyncio.gather(*(self.greet(l) for l in self.languages))
        return [shout(g) for g in greetings]
`,
  "helpers.py": `def shout(text: str) -> str:
    return text.upper() + "!"
`,
};

const LAST_PROJECT = "flyke.playground.project";

function remember(key: string, value?: string): string | null {
  try {
    if (value !== undefined) localStorage.setItem(key, value);
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

type Side = "run" | "diagram" | "manifest";

/** What you were doing in a project: unsaved edits included. */
type Session = { files: Files; active: string; entrypoint: string; runId: string | null; side: Side };

// Kept in memory across page changes, and in sessionStorage across reloads
// of this tab, so leaving the playground never loses code.
const sessions = new Map<string, Session>();
const sessionKey = (project: string) => `flyke.playground.session.${project}`;

function readSession(project: string): Session | null {
  const mem = sessions.get(project);
  if (mem) return mem;
  try {
    const s = JSON.parse(sessionStorage.getItem(sessionKey(project)) ?? "null");
    return s && typeof s.files === "object" ? (s as Session) : null;
  } catch {
    return null;
  }
}

function writeSession(project: string, s: Session) {
  sessions.set(project, s);
  try {
    sessionStorage.setItem(sessionKey(project), JSON.stringify(s));
  } catch {
    /* storage unavailable or full: the in-memory copy still covers navigation */
  }
}

function dropSession(project: string) {
  sessions.delete(project);
  try {
    sessionStorage.removeItem(sessionKey(project));
  } catch {
    /* storage unavailable */
  }
}

const SHOW_FILES = "flyke.playground.files";

function sameFiles(a: Files, b: Files): boolean {
  const ka = Object.keys(a);
  return ka.length === Object.keys(b).length && ka.every((k) => a[k] === b[k]);
}

/** The light or dark editor theme, following the app's theme switch and the OS setting. */
function useEditorTheme(): string {
  const read = () => {
    const forced = document.documentElement.getAttribute("data-theme");
    if (forced) return forced === "dark";
    return window.matchMedia("(prefers-color-scheme: dark)").matches;
  };
  const [dark, setDark] = useState(read);
  useEffect(() => {
    const mq = window.matchMedia("(prefers-color-scheme: dark)");
    const update = () => setDark(read());
    mq.addEventListener("change", update);
    const obs = new MutationObserver(update);
    obs.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
    return () => {
      mq.removeEventListener("change", update);
      obs.disconnect();
    };
  }, []);
  return dark ? "flyke-dark" : "flyke-light";
}

export default function Playground() {
  const { client } = useConnection();
  const info = useQuery({ queryKey: ["playground", "info"], queryFn: () => client.playgroundInfo(), staleTime: 30_000 });

  if (info.error) return <ErrorBox error={info.error} />;
  if (!info.data) return <Empty>Loading…</Empty>;
  if (!info.data.enabled) {
    return (
      <>
        <div className="page-head"><div><h1>Playground</h1></div></div>
        <Card>
          <div className="stack">
            <p style={{ margin: 0 }}>The playground is not available on this control plane.</p>
            <p className="secondary" style={{ margin: 0 }}>{info.data.reason}</p>
          </div>
        </Card>
      </>
    );
  }
  return <ProjectGate />;
}

/** Picks the project from the URL, the last one used, or offers to create one. */
function ProjectGate() {
  const { project } = useParams();
  const { client } = useConnection();
  const navigate = useNavigate();
  const projects = useQuery({ queryKey: ["playground", "projects"], queryFn: () => client.listProjects() });

  useEffect(() => {
    if (project || !projects.data?.length) return;
    const last = remember(LAST_PROJECT);
    const pick = projects.data.find((p) => p.name === last) ?? projects.data[0];
    navigate(`/playground/${pick.name}`, { replace: true });
  }, [project, projects.data, navigate]);

  if (projects.error) return <ErrorBox error={projects.error} />;
  if (!projects.data) return <Empty>Loading…</Empty>;
  if (!project) {
    if (projects.data.length) return <Empty>Loading…</Empty>;
    return (
      <>
        <div className="page-head"><div><h1>Playground</h1>
          <div className="subtitle">Write workflows in the browser and run them on this control plane's workers.</div></div></div>
        <Card><NewProjectForm initial="hello" /></Card>
      </>
    );
  }
  return <Workspace key={project} project={project} projects={projects.data} />;
}

function NewProjectForm({ initial = "", onDone }: { initial?: string; onDone?: () => void }) {
  const { client } = useConnection();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [name, setName] = useState(initial);
  const create = useMutation({
    mutationFn: async () => {
      const n = name.trim();
      const existing = await client.listProjects();
      if (existing.some((p) => p.name === n)) throw new Error(`a project named ${n} already exists`);
      return client.saveProject(n, STARTER);
    },
    onSuccess: (p) => {
      qc.invalidateQueries({ queryKey: ["playground", "projects"] });
      onDone?.();
      navigate(`/playground/${p.name}`);
    },
  });
  return (
    <form className="stack" onSubmit={(e) => { e.preventDefault(); create.mutate(); }}>
      <label className="field">Project name <span className="hint">lowercase letters, digits, - and _</span>
        <input value={name} onChange={(e) => setName(e.target.value.toLowerCase())} pattern="[a-z0-9][a-z0-9_\-]{0,62}" required autoFocus />
      </label>
      <ErrorBox error={create.error} />
      <div className="row gap-sm end">
        {onDone && <button type="button" className="btn" onClick={onDone}>Cancel</button>}
        <button className="btn btn-primary" type="submit" disabled={create.isPending}>Create project</button>
      </div>
    </form>
  );
}

function Workspace({ project, projects }: { project: string; projects: PlaygroundProject[] }) {
  const { client } = useConnection();
  const scope = useScope();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const theme = useEditorTheme();

  // Where you left off in this project, if you were here before.
  const [restored] = useState(() => readSession(project));
  const [files, setFiles] = useState<Files | null>(null);
  const [saved, setSaved] = useState<Files>({});
  const [discovery, setDiscovery] = useState<PlaygroundProject["discovery"]>();
  const [active, setActive] = useState("");
  const [entrypoint, setEntrypoint] = useState(restored?.entrypoint ?? "");
  const [runId, setRunId] = useState<string | null>(restored?.runId ?? null);
  const [side, setSide] = useState<Side>(restored?.side ?? "run");
  const [showFiles, setShowFiles] = useState(() => remember(SHOW_FILES) !== "0");
  const [dialog, setDialog] = useState<"new-project" | "delete-project" | null>(null);
  const editorRef = useRef<Parameters<OnMount>[0] | null>(null);

  useEffect(() => void remember(LAST_PROJECT, project), [project]);

  const load = useQuery({
    queryKey: ["playground", "project", project],
    queryFn: () => client.getProject(project),
    // Always read what is saved on the control plane when (re)opening, never a
    // cached copy from before the last save.
    staleTime: 0,
    gcTime: 0,
    refetchOnWindowFocus: false,
  });
  useEffect(() => {
    if (!load.data || files) return;
    const onServer = load.data.files ?? {};
    // Unsaved edits from an earlier visit win over the saved copy.
    const f = restored?.files ?? onServer;
    const names = Object.keys(f).sort();
    setFiles(f);
    setSaved(onServer);
    setDiscovery(load.data.discovery);
    setActive(restored && f[restored.active] !== undefined ? restored.active : names.find((n) => n === "main.py") ?? names[0] ?? "");
  }, [load.data, files, restored]);

  useEffect(() => {
    if (files) writeSession(project, { files, active, entrypoint, runId, side });
  }, [project, files, active, entrypoint, runId, side]);

  const dirty = files !== null && !sameFiles(files, saved);
  const workflows = discovery?.workflows ?? [];
  const errors = discovery?.errors ?? [];
  const selected: PlaygroundWorkflow | undefined = workflows.find((w) => w.entrypoint === entrypoint) ?? workflows[0];
  useEffect(() => {
    if (selected && selected.entrypoint !== entrypoint) setEntrypoint(selected.entrypoint);
  }, [selected, entrypoint]);

  const save = useMutation({
    mutationFn: (f: Files) => client.saveProject(project, f),
    onSuccess: (p, f) => {
      setSaved(f);
      setDiscovery(p.discovery);
      qc.invalidateQueries({ queryKey: ["playground", "projects"] });
    },
  });

  const start = useMutation({
    mutationFn: async (inputs: Record<string, unknown>) => {
      if (!files || !selected) throw new Error("no workflow to run");
      if (dirty) {
        const p = await save.mutateAsync(files);
        if (!p.discovery?.workflows.some((w) => w.entrypoint === selected.entrypoint)) {
          throw new Error(`${selected.entrypoint} no longer loads; see the problems list`);
        }
      }
      return client.runProject(project, selected.entrypoint, inputs);
    },
    onSuccess: (run) => {
      setRunId(run.metadata.name);
      setSide("run");
      qc.invalidateQueries({ queryKey: [scope, "runs"] });
      qc.invalidateQueries({ queryKey: [scope, "workflows"] });
    },
  });

  const run = useQuery({
    queryKey: [scope, "run", runId],
    queryFn: () => client.getRun(runId!),
    enabled: !!runId,
    refetchInterval: (q) => (isLive(q.state.data) ? 1000 : false),
  });

  // Latest handlers for editor keybindings (registered once on mount).
  const actions = useRef({ save: () => {}, run: () => {} });
  actions.current.save = () => files && dirty && save.mutate(files);
  const formRef = useRef<HTMLDivElement>(null);
  actions.current.run = () => formRef.current?.querySelector("form")?.requestSubmit();

  const onMount: OnMount = (editor, m) => {
    editorRef.current = editor;
    registerFlykeLanguageFeatures();
    // The editor font is a web font: measure again once it has loaded.
    void document.fonts?.ready.then(() => monaco.editor.remeasureFonts());
    editor.addCommand(m.KeyMod.CtrlCmd | m.KeyCode.KeyS, () => actions.current.save());
    editor.addCommand(m.KeyMod.CtrlCmd | m.KeyCode.Enter, () => actions.current.run());
  };

  // Ctrl/Cmd+S outside the editor too (and never the browser's "save page").
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "s") {
        e.preventDefault();
        actions.current.save();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  useEffect(() => {
    if (!dirty) return;
    const warn = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty]);

  const modelPath = useCallback((file: string) => `file:///playground/${project}/${file}`, [project]);

  // Import errors from the last save become editor markers.
  useEffect(() => {
    if (!files) return;
    for (const name of Object.keys(files)) {
      const model = monaco.editor.getModel(monaco.Uri.parse(modelPath(name)));
      if (!model) continue;
      const markers = errors.filter((e) => e.file === name).map((e) => {
        const line = Math.min(Math.max(e.line ?? 1, 1), model.getLineCount());
        return {
          severity: monaco.MarkerSeverity.Error, message: e.error,
          startLineNumber: line, endLineNumber: line, startColumn: 1, endColumn: model.getLineMaxColumn(line),
        };
      });
      monaco.editor.setModelMarkers(model, "flyke", markers);
    }
  }, [errors, files, active, modelPath]);

  // Dispose this project's models when leaving it.
  useEffect(() => () => {
    for (const model of monaco.editor.getModels()) {
      if (model.uri.toString().startsWith(`file:///playground/${project}/`)) model.dispose();
    }
  }, [project]);

  const jumpTo = (e: PlaygroundError) => {
    setActive(e.file);
    setTimeout(() => {
      const ed = editorRef.current;
      if (!ed || !e.line) return;
      ed.revealLineInCenter(e.line);
      ed.setPosition({ lineNumber: e.line, column: 1 });
      ed.focus();
    }, 0);
  };

  const remove = useMutation({
    mutationFn: () => client.deleteProject(project),
    onSuccess: () => {
      dropSession(project);
      qc.invalidateQueries({ queryKey: ["playground", "projects"] });
      navigate("/playground", { replace: true });
    },
  });

  if (load.error) return <ErrorBox error={load.error} />;
  if (!files) return <Empty>Loading…</Empty>;

  // Export what is saved, and only when it loads cleanly.
  const exportBlocked = dirty ? "Save your changes first"
    : errors.length ? "Fix the problems first"
    : workflows.length === 0 ? "No workflow to export yet"
    : "";
  const toggleFiles = () => setShowFiles((v) => {
    remember(SHOW_FILES, v ? "0" : "1");
    return !v;
  });

  const status = save.isPending ? "Saving…" : save.error ? "Save failed" : dirty ? "Unsaved changes" : "Saved";

  return (
    <div className="pg">
      <div className="pg-toolbar">
        <h1 className="pg-title">Playground</h1>
        <button className={`btn btn-sm ${showFiles ? "" : "btn-ghost"}`} onClick={toggleFiles} aria-pressed={showFiles}
          title={showFiles ? "Hide the file list" : "Show the file list"}>
          <Icon name="panel" /> Files
        </button>
        <select aria-label="Project" value={project} onChange={(e) => navigate(`/playground/${e.target.value}`)}>
          {projects.map((p) => <option key={p.name} value={p.name}>{p.name}</option>)}
        </select>
        <button className="btn btn-sm" onClick={() => setDialog("new-project")}>New project</button>
        <button className="btn btn-sm btn-ghost" onClick={() => setDialog("delete-project")}>Delete</button>
        <span className={`pg-status ${dirty ? "dirty" : ""}`} role="status">{status}</span>
        <div className="grow" />
        <span className="muted pg-keys">⌘/Ctrl+S save - ⌘/Ctrl+Enter run</span>
        <button className="btn btn-sm" onClick={() => download(zip(saved, project), `${project}.zip`)} disabled={!!exportBlocked}
          title={exportBlocked || `Download ${project}.zip: the project's files, ready for flyke run or flyke deploy`}>
          <Icon name="download" /> Export
        </button>
        <button className="btn btn-sm" onClick={() => save.mutate(files)} disabled={!dirty || save.isPending}>Save</button>
      </div>
      <ErrorBox error={save.error} />

      <div className={`pg-body ${showFiles ? "" : "no-files"}`}>
        {showFiles && (
          <FileTree files={files} active={active} saved={saved} errors={errors}
            onOpen={setActive}
            onChange={(next, open) => { setFiles(next); if (open !== undefined) setActive(open); }} />
        )}

        <div className="pg-editor">
          {active && files[active] !== undefined ? (
            <Editor
              path={modelPath(active)}
              language={languageOf(active)}
              value={files[active]}
              theme={theme}
              onMount={onMount}
              onChange={(v) => setFiles((f) => (f ? { ...f, [active]: v ?? "" } : f))}
              options={{
                fontSize: 13, minimap: { enabled: false }, scrollBeyondLastLine: false, tabSize: 4,
                insertSpaces: true, automaticLayout: true, renderWhitespace: "selection", fixedOverflowWidgets: true,
                fontFamily: "'Geist Mono Variable', 'JetBrains Mono', ui-monospace, 'SF Mono', Menlo, Consolas, monospace",
              }}
            />
          ) : <Empty>Create a file to start.</Empty>}
        </div>

        <aside className="pg-side">
          <section className="pg-panel">
            <div className="pg-panel-head">
              <h2>Workflow</h2>
              {dirty && <span className="muted" style={{ fontSize: 12 }}>from last save</span>}
            </div>
            {workflows.length === 0 ? (
              <p className="muted" style={{ margin: 0 }}>No <code>@workflow</code> class found{errors.length ? " (see problems below)" : ""}.</p>
            ) : (
              <select aria-label="Workflow" value={selected?.entrypoint} onChange={(e) => setEntrypoint(e.target.value)}>
                {workflows.map((w) => <option key={w.entrypoint} value={w.entrypoint}>{w.manifest.name} - {w.entrypoint}</option>)}
              </select>
            )}
            {selected?.manifest.description && <p className="secondary" style={{ margin: 0 }}>{selected.manifest.description}</p>}
            {selected && (
              <div ref={formRef}>
                <InputsForm key={selected.entrypoint} schema={selected.manifest.inputs_schema}
                  pending={start.isPending} error={start.error} submitLabel="▶ Run"
                  onSubmit={(inputs) => start.mutate(inputs)} />
              </div>
            )}
          </section>

          {errors.length > 0 && (
            <section className="pg-panel">
              <div className="pg-panel-head"><h2>Problems <span className="count">{errors.length}</span></h2></div>
              <ul className="pg-problems">
                {errors.map((e, i) => (
                  <li key={i}>
                    <button className="pg-problem" onClick={() => jumpTo(e)}>
                      <span className="mono">{e.file}{e.line ? `:${e.line}` : ""}</span>
                      <span>{e.error}</span>
                    </button>
                  </li>
                ))}
              </ul>
            </section>
          )}

          <section className="pg-panel pg-grow">
            <div className="tabs">
              {(["run", "diagram", "manifest"] as const).map((t) => (
                <button key={t} className={side === t ? "on" : ""} onClick={() => setSide(t)}>
                  {t === "run" ? "Last run" : t === "diagram" ? "Diagram" : "Manifest"}
                </button>
              ))}
            </div>
            {side === "diagram" && (selected ? <WorkflowFlow manifest={selected.manifest} /> : <Empty>No workflow.</Empty>)}
            {side === "manifest" && (selected ? <Json value={selected.manifest} /> : <Empty>No workflow.</Empty>)}
            {side === "run" && (
              !runId ? <Empty>Runs you start here show up here, with live logs.</Empty>
                : run.error ? <ErrorBox error={run.error} />
                : !run.data ? <Empty>Starting…</Empty>
                : (
                  <div className="stack" style={{ gap: 10, minHeight: 0 }}>
                    <div className="row gap-sm wrap">
                      <PhaseBadge phase={run.data.status.phase} />
                      <Link className="mono" to={`/runs/${run.data.metadata.name}`}>{run.data.metadata.name}</Link>
                      <span className="muted" style={{ fontSize: 12 }}>{run.data.spec.version}</span>
                    </div>
                    {run.data.status.message && !isLive(run.data) && run.data.status.phase !== "Succeeded" && (
                      <div className="alert alert-critical" style={{ fontSize: 12.5 }}>{run.data.status.message}</div>
                    )}
                    {run.data.status.phase === "Succeeded" && run.data.status.result !== undefined && <Json value={run.data.status.result} />}
                    <div className="pg-logs"><LogViewer run={run.data} /></div>
                  </div>
                )
            )}
          </section>
        </aside>
      </div>

      {dialog === "new-project" && (
        <Modal title="New project" onClose={() => setDialog(null)}>
          <NewProjectForm onDone={() => setDialog(null)} />
        </Modal>
      )}
      {dialog === "delete-project" && (
        <Confirm title={`Delete ${project}?`} danger action="Delete project"
          body={<>The project's files are deleted from the control plane. Workflows already registered from it and their runs are kept.</>}
          onConfirm={() => remove.mutate()} onClose={() => setDialog(null)} />
      )}
    </div>
  );
}

const FILE_RE = /^[A-Za-z0-9_][A-Za-z0-9_.-]*(\/[A-Za-z0-9_][A-Za-z0-9_.-]*)*$/;

function FileTree({ files, saved, active, errors, onOpen, onChange }: {
  files: Files; saved: Files; active: string; errors: PlaygroundError[];
  onOpen: (f: string) => void;
  onChange: (files: Files, open?: string) => void;
}) {
  const [editing, setEditing] = useState<{ from: string | null; value: string } | null>(null);
  const [problem, setProblem] = useState("");
  const [deleting, setDeleting] = useState<string | null>(null);
  const names = useMemo(() => Object.keys(files).sort(), [files]);
  const broken = new Set(errors.map((e) => e.file));

  const commit = () => {
    if (!editing) return;
    const to = editing.value.trim();
    if (!to || to === editing.from) return setEditing(null);
    if (!FILE_RE.test(to) || to.split("/").includes("__pycache__")) return setProblem("letters, digits, _ - . and / only");
    if (files[to] !== undefined) return setProblem(`${to} exists`);
    const next = { ...files };
    if (editing.from === null) {
      next[to] = "";
    } else {
      next[to] = next[editing.from];
      delete next[editing.from];
    }
    setEditing(null);
    setProblem("");
    onChange(next, to);
  };

  const input = (
    <li>
      <input className="pg-file-input" autoFocus value={editing?.value ?? ""} placeholder="path/to/file.py"
        onChange={(e) => { setEditing((s) => s && { ...s, value: e.target.value }); setProblem(""); }}
        onBlur={commit}
        onKeyDown={(e) => {
          if (e.key === "Enter") commit();
          if (e.key === "Escape") { setEditing(null); setProblem(""); }
        }} />
      {problem && <div className="pg-file-problem">{problem}</div>}
    </li>
  );

  return (
    <nav className="pg-files" aria-label="Files">
      <div className="pg-panel-head">
        <h2>Files</h2>
        <button className="btn btn-ghost btn-sm" title="New file" aria-label="New file" onClick={() => setEditing({ from: null, value: "" })}>＋</button>
      </div>
      <ul>
        {names.map((n) => editing?.from === n ? <Fragment key={n}>{input}</Fragment> : (
          <li key={n}>
            <div className={`pg-file ${n === active ? "on" : ""}`}>
              <button className="pg-file-name" onClick={() => onOpen(n)} onDoubleClick={() => setEditing({ from: n, value: n })} title={`${n} (double-click to rename)`}>
                {broken.has(n) && <span className="pg-dot critical" aria-label="has problems" />}
                <span className="mono">{n}</span>
                {saved[n] !== files[n] && <span className="pg-dot" aria-label="unsaved" />}
              </button>
              <button className="pg-file-x" aria-label={`Delete ${n}`} title="Delete file" onClick={() => setDeleting(n)}>✕</button>
            </div>
          </li>
        ))}
        {editing?.from === null && input}
      </ul>
      {deleting && (
        <Confirm title={`Delete ${deleting}?`} danger action="Delete"
          body="The file is removed from the project when you next save."
          onConfirm={() => {
            const next = { ...files };
            delete next[deleting];
            onChange(next, deleting === active ? Object.keys(next).sort()[0] ?? "" : undefined);
          }}
          onClose={() => setDeleting(null)} />
      )}
    </nav>
  );
}
