export function ago(iso?: string, now = Date.now()): string {
  if (!iso) return "—";
  const s = Math.max(0, Math.round((now - Date.parse(iso)) / 1000));
  if (s < 60) return `${s}s ago`;
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
  return `${Math.floor(s / 86400)}d ago`;
}

/** A future time relative to now: "in 5m", "in 3h 20m", "in 2d". */
export function until(iso?: string, now = Date.now()): string {
  if (!iso) return "—";
  const s = Math.round((Date.parse(iso) - now) / 1000);
  if (s <= 0) return "now";
  if (s < 60) return `in ${s}s`;
  if (s < 3600) return `in ${Math.floor(s / 60)}m`;
  if (s < 172800) return `in ${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`;
  return `in ${Math.floor(s / 86400)}d`;
}

/** A time shown in a given IANA zone, e.g. "Fri 13 Nov, 03:00 CET". */
export function zonedTime(iso: string, tz?: string): string {
  try {
    return new Date(iso).toLocaleString("en-GB", {
      timeZone: tz || "UTC", weekday: "short", day: "numeric", month: "short", hour: "2-digit", minute: "2-digit", timeZoneName: "short",
    });
  } catch {
    return dateTime(iso);
  }
}

export function duration(ms: number): string {
  if (!isFinite(ms) || ms < 0) return "—";
  if (ms < 1000) return `${Math.round(ms)}ms`;
  const s = ms / 1000;
  const trim = (n: number, d: number) => String(Number(n.toFixed(d)));
  if (s < 60) return `${trim(s, s < 10 ? 2 : 1)}s`;
  const join = (a: string, b: number, unit: string) => (b ? `${a} ${b}${unit}` : a);
  const m = Math.floor(s / 60);
  if (m < 60) return join(`${m}m`, Math.round(s % 60), "s");
  const h = Math.floor(m / 60);
  if (h < 48) return join(`${h}h`, m % 60, "m");
  return join(`${Math.floor(h / 24)}d`, h % 24, "h");
}

export function span(start?: string, end?: string, now = Date.now()): string {
  if (!start) return "—";
  return duration((end ? Date.parse(end) : now) - Date.parse(start));
}

export function clock(iso: string): string {
  const d = new Date(iso);
  return d.toLocaleTimeString(undefined, { hour12: false }) + "." + String(d.getMilliseconds()).padStart(3, "0");
}

export function dateTime(iso?: string): string {
  if (!iso) return "—";
  return new Date(iso).toLocaleString(undefined, { hour12: false });
}

export function bytes(n?: number): string {
  if (!n) return "—";
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(v < 10 && i > 0 ? 1 : 0)} ${units[i]}`;
}

export function shortType(t: string): string {
  return t.split(".").pop() ?? t;
}

export function fieldValue(v: unknown): string {
  if (typeof v === "string") return /\s/.test(v) ? JSON.stringify(v) : v;
  return JSON.stringify(v);
}
