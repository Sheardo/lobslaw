import { useEffect } from "react";
import { Link } from "react-router-dom";
import { api, type InboxItem } from "../api";
import { Page } from "../App";
import { Avatar, Empty, Err, Spinner, Status, useLoad, when } from "../components/ui";
import { botVars } from "../theme";

/** What the team is doing, newest first.
 *
 * Built from the inboxes rather than from sessions, because the
 * question this answers is "what is the team doing" — and a session
 * index answers "what conversations exist", which stops being the same
 * thing the moment bots work items nobody chatted about.
 */
export function Activity() {
  const { data, error, loading, reload } = useLoad(() => api.activity(100));

  // A queue that only updates when you press a button is a queue you
  // stop believing. Ten seconds is cheap and makes a drain finishing
  // feel live.
  useEffect(() => {
    const t = setInterval(reload, 10_000);
    return () => clearInterval(t);
  }, [reload]);

  const working = (data ?? []).filter((i) => i.status === "claimed").length;
  const queued = (data ?? []).filter((i) => i.status === "pending").length;

  return (
    <Page
      title="Activity"
      sub={data ? `${working} working · ${queued} queued · ${data.length} total` : "Every bot's queue."}
      action={<button className="btn ghost sm" onClick={reload} disabled={loading}>Refresh</button>}
    >
      {error && <Err error={error} />}
      {!error && loading && !data && <Spinner />}
      {!error && data?.length === 0 && (
        <Empty title="Nothing has happened yet"
          hint="Assign a bot some work from its page, or ask the coordinator to." />
      )}
      {!error && data && data.length > 0 && (
        <div className="feed">{data.map((i) => <Row key={`${i.recipient}/${i.id}`} item={i} />)}</div>
      )}
    </Page>
  );
}

function Row({ item }: { item: InboxItem }) {
  const outcome = item.error || item.result;
  return (
    <Link to={`/bots/${item.recipient}`}>
      {/* The bot's colour runs down the left edge. Cheaper to scan than
          any badge: you find the devops rows without reading a word. */}
      <div className="item" style={botVars(item.recipient)}>
        <Avatar id={item.recipient} size={30} />
        <div className="grow">
          <div className="who">
            <b>{item.recipient}</b>
            <span className="meta">
              {item.kind}
              {item.sender && item.sender !== "operator" ? ` · from ${item.sender}` : ""}
            </span>
          </div>
          <div className="ttl">{item.subject || "(no subject)"}</div>
          {outcome && <div className={item.error ? "body bad" : "body"}>{outcome}</div>}
        </div>
        <div className="right">
          <Status status={item.status} />
          <span className="meta">{when(item.completed_at ?? item.created_at)}</span>
          {item.attempts > 1 && <span className="meta" style={{ color: "var(--warn)" }}>{item.attempts} attempts</span>}
        </div>
      </div>
    </Link>
  );
}
