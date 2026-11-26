import { useEffect, useState } from "react";
import { flushSync } from "react-dom";

export type Theme = "system" | "light" | "dark";
export const THEMES: Theme[] = ["system", "light", "dark"];

function apply(theme: Theme) {
  const root = document.documentElement;
  if (theme === "system") root.removeAttribute("data-theme");
  else root.setAttribute("data-theme", theme);
}

/**
 * The theme and a setter. Where the browser supports view transitions, the
 * new theme is revealed as a circle growing from `origin` (see styles.css).
 */
export function useTheme(): [Theme, (t: Theme, origin?: { x: number; y: number }) => void] {
  const [theme, setTheme] = useState<Theme>(() => {
    try {
      return (localStorage.getItem("flyke.theme") as Theme) || "system";
    } catch {
      return "system";
    }
  });
  useEffect(() => {
    apply(theme);
    try {
      localStorage.setItem("flyke.theme", theme);
    } catch {
      /* storage unavailable */
    }
  }, [theme]);

  const change = (next: Theme, origin?: { x: number; y: number }) => {
    const calm = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    if (next === theme || calm || !document.startViewTransition) {
      setTheme(next);
      return;
    }
    const root = document.documentElement;
    if (origin) {
      root.style.setProperty("--vt-x", `${origin.x}px`);
      root.style.setProperty("--vt-y", `${origin.y}px`);
    } else {
      root.style.removeProperty("--vt-x");
      root.style.removeProperty("--vt-y");
    }
    document.startViewTransition(() => {
      flushSync(() => setTheme(next));
      apply(next);
    });
  };
  return [theme, change];
}
