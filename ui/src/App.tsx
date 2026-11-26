import { lazy, Suspense, useCallback, useEffect, useState } from "react";
import { NavLink, Navigate, Route, Routes } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import type { ApiError } from "./api/client";
import { useConnection } from "./api/context";
import { CommandPalette } from "./components/CommandPalette";
import { Icon, Logo, type IconName } from "./components/icons";
import { THEMES, useTheme } from "./components/theme";
import { Empty, Modal } from "./components/ui";
import { Events } from "./pages/Events";
import { Overview } from "./pages/Overview";
import { RunDetail } from "./pages/RunDetail";
import { Secrets } from "./pages/Secrets";
import { Schedules } from "./pages/Schedules";
import { Runs } from "./pages/Runs";
import { WorkflowDetail, Workflows } from "./pages/Workflows";
import { Workers } from "./pages/Workers";
import { Namespaces } from "./pages/Namespaces";
import { useNamespaces, useWhoAmI } from "./pages/hooks";

// The playground bundles Monaco; load it only when visited.
const Playground = lazy(() => import("./pages/Playground"));

const NAV: { to: string; label: string; icon: IconName; end?: boolean }[] = [
  { to: "/", label: "Overview", icon: "overview", end: true },
  { to: "/runs", label: "Runs", icon: "runs" },
  { to: "/workflows", label: "Workflows", icon: "workflows" },
  { to: "/schedules", label: "Schedules", icon: "schedules" },
  { to: "/playground", label: "Playground", icon: "playground" },
  { to: "/events", label: "Events", icon: "events" },
  { to: "/secrets", label: "Secrets", icon: "secrets" },
  { to: "/workers", label: "Workers", icon: "workers" },
  { to: "/namespaces", label: "Namespaces", icon: "namespaces" },
];

const MAC = /Mac|iPhone|iPad/.test(navigator.platform);

function Settings({ onClose }: { onClose: () => void }) {
  const { conn, setConn } = useConnection();
  const [token, setToken] = useState(conn.token);
  return (
    <Modal title="Connection" onClose={onClose}>
      <form className="stack" onSubmit={(e) => { e.preventDefault(); setConn({ ...conn, token: token.trim() }); onClose(); }}>
        <label className="field">API token
          <span className="hint">
            a namespace <strong>access token</strong> (<code>flyke_at_…</code>, from <code>flyke tokens create</code>) or the control plane's
            {" "}<code>--admin-token</code>; stored in this browser only
          </span>
          <input type="password" value={token} onChange={(e) => setToken(e.target.value)} autoComplete="off" autoFocus />
        </label>
        <div className="row gap-sm end">
          <button type="button" className="btn" onClick={onClose}>Cancel</button>
          <button type="submit" className="btn btn-primary">Save</button>
        </div>
      </form>
    </Modal>
  );
}

function Connection({ onSettings }: { onSettings: () => void }) {
  const { conn, setConn } = useConnection();
  // A cheap authenticated call tells us reachability, auth status and what
  // the token can access.
  const me = useWhoAmI();
  const admin = !!me.data?.admin;
  const namespaces = useNamespaces();
  const err = me.error as ApiError | null;
  const state = me.isSuccess ? "ok" : err?.status === 401 ? "auth" : err ? "down" : "pending";
  useEffect(() => {
    if (state === "auth") onSettings();
  }, [state, onSettings]);
  // An access token works in exactly one namespace: follow it.
  const own = me.data && !me.data.admin ? me.data.namespace : undefined;
  useEffect(() => {
    if (own && own !== conn.namespace) setConn({ ...conn, namespace: own });
  }, [own, conn, setConn]);
  // The admin's namespace may have been deleted: fall back to one that exists.
  const names = namespaces.data?.map((n) => n.metadata.name);
  useEffect(() => {
    if (admin && names?.length && !names.includes(conn.namespace)) {
      setConn({ ...conn, namespace: names.includes("default") ? "default" : names[0] });
    }
  }, [admin, names?.join(","), conn, setConn]); // eslint-disable-line react-hooks/exhaustive-deps

  const label = { ok: admin ? "Admin" : `Token ${me.data?.token ?? ""}`, auth: "Token required", down: "Unreachable", pending: "Connecting…" }[state];
  const tone = { ok: admin ? "accent" : "good", auth: "warning", down: "critical", pending: "muted" }[state];
  const icon: IconName = state === "ok" ? (admin ? "shield" : "key") : state === "pending" ? "auto" : "alert";
  const title = {
    ok: admin ? "Signed in with the admin token: every namespace is visible" : "Signed in with a namespace access token",
    auth: "This control plane needs a token", down: "The control plane cannot be reached", pending: "Connecting to the control plane",
  }[state];
  return (
    <div className="ctx" data-tone={tone}>
      <button className="ctx-who" onClick={onSettings} title={`${title} - connection settings`}>
        <Icon name={icon} className={state === "pending" ? "pulse" : undefined} />
        <span className="ctx-who-label">{label}</span>
      </button>
      {admin && names && names.length > 0 ? (
        <label className="ctx-ns" title="Namespace (tenant) to view">
          <Icon name="namespaces" />
          <span className="ctx-ns-label">Namespace</span>
          <select value={conn.namespace} onChange={(e) => setConn({ ...conn, namespace: e.target.value })}>
            {names.map((n) => <option key={n} value={n}>{n}</option>)}
          </select>
          <Icon name="chevron" className="ctx-chevron" />
        </label>
      ) : (
        <span className="ctx-ns" title="Your access token works in this namespace only">
          <Icon name="namespaces" />
          <span className="ctx-ns-label">Namespace</span>
          <strong className="ctx-ns-value">{conn.namespace}</strong>
        </span>
      )}
    </div>
  );
}

export function App() {
  const [theme, setTheme] = useTheme();
  const [settings, setSettings] = useState(false);
  const [palette, setPalette] = useState(false);
  const openSettings = useCallback(() => setSettings(true), []);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setPalette((p) => !p);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);
  // Until we know, assume a tenant: admin-only entries appear, never vanish.
  const admin = !!useWhoAmI().data?.admin;
  const version = useQuery({ queryKey: ["version"], queryFn: async () => (await fetch("/version")).text(), staleTime: Infinity });

  const nav = NAV.filter((n) => admin || n.to !== "/playground").map((n) => (n.to === "/namespaces" && !admin ? { ...n, label: "Access" } : n));

  return (
    <div className="app">
      <aside className="sidebar">
        <div className="brand">
          <Logo className="brand-mark" />
          Flyke
        </div>
        <nav className="nav">
          {nav.map((n) => (
            <NavLink key={n.to} to={n.to} end={n.end}>
              <Icon name={n.icon} className="nav-icon" />{n.label}
            </NavLink>
          ))}
        </nav>
        <div className="sidebar-foot muted">
          <span>control plane {version.data?.trim() || ""}</span>
        </div>
      </aside>
      <div className="main">
        <header className="topbar">
          <button className="cmdk-trigger" onClick={() => setPalette(true)} aria-label="Search and jump to…">
            <Icon name="search" /><span>Search or jump to…</span><kbd>{MAC ? "⌘K" : "Ctrl K"}</kbd>
          </button>
          <Connection onSettings={openSettings} />
          <div className="segmented" role="radiogroup" aria-label="Theme">
            {THEMES.map((t) => (
              <button
                key={t} className={theme === t ? "on" : ""} aria-pressed={theme === t}
                title={t === "system" ? "Match system theme" : `${t === "light" ? "Light" : "Dark"} theme`}
                onClick={(e) => {
                  const r = e.currentTarget.getBoundingClientRect();
                  setTheme(t, { x: r.left + r.width / 2, y: r.top + r.height / 2 });
                }}
              >
                {t === "system" ? "Auto" : <Icon name={t === "light" ? "sun" : "moon"} />}
              </button>
            ))}
          </div>
        </header>
        <main className="content">
          <Routes>
            <Route path="/" element={<Overview />} />
            <Route path="/runs" element={<Runs />} />
            <Route path="/runs/:id" element={<RunDetail />} />
            <Route path="/workflows" element={<Workflows />} />
            <Route path="/workflows/:name" element={<WorkflowDetail />} />
            <Route path="/playground/:project?" element={<Suspense fallback={<Empty>Loading editor…</Empty>}><Playground /></Suspense>} />
            <Route path="/schedules" element={<Schedules />} />
            <Route path="/events" element={<Events />} />
            <Route path="/secrets" element={<Secrets />} />
            <Route path="/workers" element={<Workers />} />
            <Route path="/pools" element={<Navigate to="/workers" replace />} />
            <Route path="/namespaces" element={<Namespaces />} />
            <Route path="*" element={<Navigate to="/" replace />} />
          </Routes>
        </main>
      </div>
      {settings && <Settings onClose={() => setSettings(false)} />}
      {palette && <CommandPalette nav={nav} setTheme={setTheme} onClose={() => setPalette(false)} />}
    </div>
  );
}
