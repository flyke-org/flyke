import { createContext, useContext, useMemo, useState, type ReactNode } from "react";
import { api, type Api, type Connection } from "./client";

const KEY = "flyke.connection";

function load(): Connection {
  try {
    const raw = localStorage.getItem(KEY);
    if (raw) return { namespace: "default", token: "", ...JSON.parse(raw) };
  } catch {
    /* storage unavailable */
  }
  return { namespace: "default", token: "" };
}

interface Ctx {
  conn: Connection;
  client: Api;
  setConn: (c: Connection) => void;
}

const ConnectionContext = createContext<Ctx | null>(null);

export function ConnectionProvider({ children }: { children: ReactNode }) {
  const [conn, setState] = useState<Connection>(load);
  const value = useMemo<Ctx>(
    () => ({
      conn,
      client: api(conn),
      setConn: (c) => {
        setState(c);
        try {
          localStorage.setItem(KEY, JSON.stringify(c));
        } catch {
          /* storage unavailable */
        }
      },
    }),
    [conn],
  );
  return <ConnectionContext.Provider value={value}>{children}</ConnectionContext.Provider>;
}

export function useConnection(): Ctx {
  const ctx = useContext(ConnectionContext);
  if (!ctx) throw new Error("useConnection outside ConnectionProvider");
  return ctx;
}

/** Query key scoped to the current namespace + token. */
export function useScope(): string {
  const { conn } = useConnection();
  return `${conn.namespace}|${conn.token ? "auth" : "anon"}`;
}
