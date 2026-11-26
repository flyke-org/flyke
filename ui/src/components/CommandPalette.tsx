// ⌘K / Ctrl+K: jump to a page, a workflow or a recent run, or switch theme.

import { useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { useRuns, useWorkflows } from "../pages/hooks";
import { Icon, type IconName } from "./icons";
import { THEMES, type Theme } from "./theme";

type Item = { group: string; label: string; hint?: string; icon: IconName; run: () => void };

export type PaletteNav = { to: string; label: string; icon: IconName };

const THEME_LABEL: Record<Theme, [string, IconName]> = {
  system: ["Match system theme", "auto"],
  light: ["Light theme", "sun"],
  dark: ["Dark theme", "moon"],
};

export function CommandPalette({ nav, setTheme, onClose }: { nav: PaletteNav[]; setTheme: (t: Theme) => void; onClose: () => void }) {
  const navigate = useNavigate();
  const workflows = useWorkflows();
  const runs = useRuns({ limit: 500 });
  const [query, setQuery] = useState("");
  const [cursor, setCursor] = useState(0);
  const list = useRef<HTMLDivElement>(null);

  const items = useMemo(() => {
    const go = (to: string) => () => navigate(to);
    const all: Item[] = [
      ...nav.map((n) => ({ group: "Go to", label: n.label, icon: n.icon, run: go(n.to) })),
      ...(workflows.data ?? []).map((w) => ({ group: "Workflows", label: w.metadata.name, icon: "workflows" as const, run: go(`/workflows/${w.metadata.name}`) })),
      ...(runs.data ?? []).map((r) => ({ group: "Runs", label: r.metadata.name, hint: `${r.spec.workflow} - ${r.status.phase}`, icon: "run" as const, run: go(`/runs/${r.metadata.name}`) })),
      ...THEMES.map((t) => ({ group: "Theme", label: THEME_LABEL[t][0], icon: THEME_LABEL[t][1], run: () => setTheme(t) })),
    ];
    const words = query.toLowerCase().split(/\s+/).filter(Boolean);
    const match = (i: Item) => words.every((w) => `${i.group} ${i.label} ${i.hint ?? ""}`.toLowerCase().includes(w));
    const hits = all.filter(match);
    // Runs are many: show a handful unless the query narrows them down.
    let shown = 0;
    return hits.filter((i) => i.group !== "Runs" || shown++ < (words.length ? 8 : 5));
  }, [nav, workflows.data, runs.data, query, navigate, setTheme]);

  const active = Math.min(cursor, items.length - 1);
  useEffect(() => {
    list.current?.querySelector(".cmdk-item.on")?.scrollIntoView({ block: "nearest" });
  }, [active, items]);

  const pick = (i?: Item) => {
    if (!i) return;
    onClose();
    i.run();
  };
  const onKey = (e: React.KeyboardEvent) => {
    if (e.key === "ArrowDown") { e.preventDefault(); setCursor(items.length ? (active + 1) % items.length : 0); }
    else if (e.key === "ArrowUp") { e.preventDefault(); setCursor(items.length ? (active - 1 + items.length) % items.length : 0); }
    else if (e.key === "Enter") { e.preventDefault(); pick(items[active]); }
    else if (e.key === "Escape") { e.preventDefault(); onClose(); }
  };

  return (
    <div className="modal-backdrop cmdk-backdrop" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className="cmdk" role="dialog" aria-modal aria-label="Command palette" onKeyDown={onKey}>
        <div className="cmdk-input">
          <Icon name="search" />
          <input
            autoFocus placeholder="Search pages, workflows, runs…" value={query} aria-label="Search"
            role="combobox" aria-expanded aria-controls="cmdk-list" aria-activedescendant={items.length ? `cmdk-${active}` : undefined}
            onChange={(e) => { setQuery(e.target.value); setCursor(0); }}
          />
          <kbd>esc</kbd>
        </div>
        <div className="cmdk-list" id="cmdk-list" role="listbox" ref={list}>
          {items.length === 0 && <div className="empty">Nothing matches “{query}”.</div>}
          {items.map((item, i) => (
            <div key={`${item.group}/${item.label}`}>
              {item.group !== items[i - 1]?.group && <div className="cmdk-group">{item.group}</div>}
              <button
                type="button" id={`cmdk-${i}`} role="option" aria-selected={i === active} tabIndex={-1}
                className={`cmdk-item ${i === active ? "on" : ""}`}
                onMouseMove={() => i !== active && setCursor(i)} onClick={() => pick(item)}
              >
                <Icon name={item.icon} />
                <span className="grow">{item.label}</span>
                {item.hint && <span className="hint">{item.hint}</span>}
              </button>
            </div>
          ))}
        </div>
        <div className="cmdk-foot">
          <span><kbd>↑</kbd><kbd>↓</kbd> navigate</span>
          <span><kbd>↵</kbd> open</span>
        </div>
      </div>
    </div>
  );
}
