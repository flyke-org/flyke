import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useConnection, useScope } from "../api/context";
import { EmitDialog } from "../components/forms";
import { ago, dateTime } from "../components/format";
import { Card, Empty, ErrorBox, Pager, usePaged } from "../components/ui";

export function Events() {
  const { client } = useConnection();
  const scope = useScope();
  const [filter, setFilter] = useState("");
  const [emitting, setEmitting] = useState(false);
  const events = useQuery({ queryKey: [scope, "events", filter], queryFn: () => client.listEvents(filter), refetchInterval: 3000 });
  const { rows, pager } = usePaged(events.data, filter);

  return (
    <>
      <div className="page-head">
        <div>
          <h1>Events</h1>
          <div className="subtitle">Events resume runs waiting in <code>wait_for()</code> and fire <code>on.event()</code> triggers</div>
        </div>
        <button className="btn btn-primary" onClick={() => setEmitting(true)}>⚡ Emit event</button>
      </div>
      <div className="row gap-sm">
        <input placeholder="Filter by exact event name (e.g. deploy.approved)" value={filter} onChange={(e) => setFilter(e.target.value)} style={{ width: 360 }} />
      </div>
      <ErrorBox error={events.error} />
      <Card className="flush">
        {events.data?.length === 0 ? <Empty>No events.</Empty> : (
          <><div className="table-wrap">
            <table className="table">
              <thead><tr><th>Event</th><th>Payload</th><th>Id</th><th className="num">When</th></tr></thead>
              <tbody>
                {rows.map((e) => (
                    <tr key={e.metadata.name}>
                      <td className="mono"><button className="btn btn-ghost btn-sm mono" onClick={() => setFilter(e.name)}>{e.name}</button></td>
                      <td className="mono secondary" style={{ maxWidth: 520, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{JSON.stringify(e.payload)}</td>
                      <td className="mono muted">{e.metadata.name}</td>
                      <td className="num muted" title={dateTime(e.metadata.createdAt)}>{ago(e.metadata.createdAt)}</td>
                    </tr>
                ))}
              </tbody>
            </table>
          </div>
          <Pager {...pager} /></>
        )}
      </Card>
      {emitting && <EmitDialog onClose={() => setEmitting(false)} />}
    </>
  );
}
