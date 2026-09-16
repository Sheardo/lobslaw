import { useState } from "react";
import { Link, useParams } from "react-router-dom";
import { api, type Bot, type InboxItem, type InboxKind, type TranscriptMessage } from "../api";
import { Page } from "../App";
import { Empty, Err, Spinner, Status, useLoad, when } from "../components/ui";
import { Mascot } from "../components/Mascot";
import { botVars } from "../theme";

export function BotDetail({ onChanged }: { onChanged: () => void }) {
  const { botId = "" } = useParams();
  const { data: bot, error, loading, reload } = useLoad(() => api.getBot(botId), [botId]);
  const [tab, setTab] = useState<"inbox" | "settings">("inbox");

  if (error) return <Page title="Bot"><Err error={error} /></Page>;
  if (loading && !bot) return <Spinner />;
  if (!bot) return null;

  return (
    <div style={botVars(bot.id)}>
      <Page
        title={
          <span className="row">
            <Mascot id={bot.id} size={34} dim={!bot.enabled} />
            <span style={{ color: "var(--bot-fg)" }}>{bot.display_name || bot.id}</span>
            {bot.is_coordinator && <span className="tag" style={{ color: "var(--brand)" }}>coordinator</span>}
            {!bot.enabled && <span className="tag" style={{ color: "var(--warn)" }}>disabled</span>}
          </span>
        }
        sub={bot.description || `bot:${bot.id}`}
      >
        <div className="tabs">
          <button className={tab === "inbox" ? "tab on" : "tab"} onClick={() => setTab("inbox")}>Inbox</button>
          <button className={tab === "settings" ? "tab on" : "tab"} onClick={() => setTab("settings")}>Settings</button>
        </div>
        {tab === "inbox"
          ? <Inbox botId={bot.id} />
          : <Settings bot={bot} onSaved={() => { reload(); onChanged(); }} />}
      </Page>
    </div>
  );
}

const STATUSES = ["all", "pending", "claimed", "done", "failed", "cancelled"];

function Inbox({ botId }: { botId: string }) {
  const [status, setStatus] = useState("all");
  const [assigning, setAssigning] = useState(false);
  const { data, error, loading, reload } = useLoad(() => api.listInbox(botId, status), [botId, status]);

  return (
    <div className="col gap">
      <div className="row" style={{ justifyContent: "space-between" }}>
        <div className="chips">
          {STATUSES.map((s) => (
            <button key={s} className={status === s ? "chip on" : "chip"} onClick={() => setStatus(s)}>{s}</button>
          ))}
        </div>
        <div className="row gap-sm">
          <button className="btn ghost sm" onClick={reload} disabled={loading}>Refresh</button>
          <button className="btn primary sm" onClick={() => setAssigning((v) => !v)}>
            {assigning ? "Cancel" : "Assign work"}
          </button>
        </div>
      </div>

      {assigning && <Assign botId={botId} onDone={() => { setAssigning(false); reload(); }} />}
      {error && <Err error={error} />}
      {!error && loading && !data && <Spinner />}
      {!error && data?.length === 0 && (
        <Empty title="Nothing in this queue" hint="Assign it something, or let another bot hand it work." />
      )}
      {!error && data && data.length > 0 && (
        <div className="feed">
          {data.map((i) => <Row key={i.id} botId={botId} item={i} onChanged={reload} />)}
        </div>
      )}
    </div>
  );
}

function Row({ botId, item, onChanged }: { botId: string; item: InboxItem; onChanged: () => void }) {
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<Error | null>(null);
  const [full, setFull] = useState<InboxItem | null>(null);

  async function expand() {
    const next = !open;
    setOpen(next);
    // The listing omits bodies on purpose — sending every body in a
    // hundred-item queue is how a page becomes a megabyte.
    if (next && !full) {
      try { setFull(await api.readItem(botId, item.id)); } catch (e) { setError(e as Error); }
    }
  }
  async function act(action: "retry" | "cancel") {
    setBusy(true); setError(null);
    try { await api.actOnItem(botId, item.id, action); onChanged(); }
    catch (e) { setError(e as Error); } finally { setBusy(false); }
  }

  const d = full ?? item;
  return (
    <div className="item" style={{ display: "block", padding: "15px 18px" }}>
      <div className="row" style={{ alignItems: "flex-start", justifyContent: "space-between" }}>
        <div className="grow" style={{ cursor: "pointer" }} onClick={expand}>
          <div className="who">
            <Status status={item.status} />
            <span className="meta">{item.kind}{item.priority !== 0 ? ` · p${item.priority}` : ""}</span>
          </div>
          <div className="ttl">{item.subject || "(no subject)"}</div>
          <div className="meta" style={{ marginTop: 3 }}>
            from {item.sender} · {when(item.created_at)}
            {item.attempts > 1 && ` · ${item.attempts} attempts`}
          </div>
        </div>
        <div className="row gap-sm">
          {(item.status === "failed" || item.status === "cancelled") && (
            <button className="btn sm" onClick={() => act("retry")} disabled={busy}>Retry</button>
          )}
          {item.status === "pending" && (
            <button className="btn ghost sm" onClick={() => act("cancel")} disabled={busy}>Cancel</button>
          )}
        </div>
      </div>

      {error && <div style={{ marginTop: 12 }}><Err error={error} /></div>}

      {open && (
        <div className="col gap" style={{ marginTop: 16, paddingTop: 16, borderTop: "1px solid var(--edge)" }}>
          <Block label="Asked" body={d.body} />
          {d.result && <Block label="Result" body={d.result} />}
          {/* A failed item keeps its error, visibly. A task that
              vanished quietly is the failure the queue exists to
              prevent. */}
          {d.error && <Block label="Error" body={d.error} bad />}
          {d.session_id && <Transcript sessionId={d.session_id} />}
        </div>
      )}
    </div>
  );
}

function Block({ label, body, bad }: { label: string; body?: string; bad?: boolean }) {
  if (!body) return null;
  return (
    <div>
      <div className="lbl" style={bad ? { color: "var(--bad)" } : undefined}>{label}</div>
      <div style={{ fontSize: 13.5, lineHeight: 1.7, marginTop: 6, whiteSpace: "pre-wrap",
        color: bad ? "var(--bad)" : "var(--mid)" }}>{body}</div>
    </div>
  );
}

function Transcript({ sessionId }: { sessionId: string }) {
  const [open, setOpen] = useState(false);
  const [msgs, setMsgs] = useState<TranscriptMessage[] | null>(null);
  const [error, setError] = useState<Error | null>(null);

  async function toggle() {
    const next = !open; setOpen(next);
    if (next && !msgs) {
      try { setMsgs(await api.transcript(sessionId)); } catch (e) { setError(e as Error); }
    }
  }
  return (
    <div>
      <button className="btn ghost sm" style={{ padding: 0 }} onClick={toggle}>
        {open ? "▾" : "▸"} what the bot did
      </button>
      {error && <Err error={error} />}
      {open && msgs && (
        <div className="col gap-sm" style={{ marginTop: 10, paddingLeft: 12, borderLeft: "1px solid var(--edge)" }}>
          {msgs.map((m) => (
            <div key={m.seq}>
              <div className="lbl">{m.role}{m.tool_calls ? ` · ${m.tool_calls} tool calls` : ""}</div>
              <div style={{ fontSize: 13, color: "var(--mid)", whiteSpace: "pre-wrap", marginTop: 3 }}>
                {m.content || "(tool calls only)"}
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

const KINDS: InboxKind[] = ["task", "question", "fyi"];

function Assign({ botId, onDone }: { botId: string; onDone: () => void }) {
  const [subject, setSubject] = useState("");
  const [body, setBody] = useState("");
  const [kind, setKind] = useState<InboxKind>("task");
  const [priority, setPriority] = useState(0);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<Error | null>(null);

  async function submit() {
    setBusy(true); setError(null);
    try {
      await api.assign(botId, { subject: subject.trim(), body: body.trim(), kind, priority });
      setSubject(""); setBody(""); onDone();
    } catch (e) { setError(e as Error); } finally { setBusy(false); }
  }

  return (
    <div className="card pad col gap">
      <input className="in" value={subject} placeholder="Subject — optional, defaults to the first line"
        onChange={(e) => setSubject(e.target.value)} />
      <textarea className="ta" rows={3} value={body}
        placeholder="Deploy the staging branch and report what version is live."
        onChange={(e) => setBody(e.target.value)} />
      <div className="row gap-sm">
        <select className="in" style={{ width: 120 }} value={kind}
          onChange={(e) => setKind(e.target.value as InboxKind)}>
          {KINDS.map((k) => <option key={k} value={k}>{k}</option>)}
        </select>
        <input className="in" style={{ width: 90 }} type="number" value={priority}
          onChange={(e) => setPriority(Number(e.target.value) || 0)} />
        <button className="btn primary" onClick={submit} disabled={busy || !body.trim()}>Assign</button>
      </div>
      {error && <Err error={error} />}
    </div>
  );
}

function Settings({ bot, onSaved }: { bot: Bot; onSaved: () => void }) {
  const [f, setF] = useState({
    display_name: bot.display_name, description: bot.description,
    instructions: bot.instructions, tools: bot.tools.join(", "),
    may_message: bot.may_message.join(", "),
  });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<Error | null>(null);
  const list = (s: string) => s.split(",").map((x) => x.trim()).filter(Boolean);

  async function save(patch?: Partial<Bot>) {
    setBusy(true); setError(null);
    try {
      await api.updateBot(bot.id, patch ?? {
        display_name: f.display_name, description: f.description, instructions: f.instructions,
        tools: list(f.tools), may_message: list(f.may_message),
      });
      onSaved();
    } catch (e) { setError(e as Error); } finally { setBusy(false); }
  }

  return (
    <div className="card pad col gap-lg" style={{ maxWidth: 660 }}>
      <div className="field">
        <label>Display name</label>
        <input className="in" value={f.display_name} onChange={(e) => setF({ ...f, display_name: e.target.value })} />
      </div>
      <div className="field">
        <label>Description</label>
        <input className="in" value={f.description} onChange={(e) => setF({ ...f, description: e.target.value })} />
      </div>
      <div className="field">
        <label>Instructions</label>
        <textarea className="ta" rows={7} value={f.instructions}
          onChange={(e) => setF({ ...f, instructions: e.target.value })} />
        <div className="hint">Rides on every turn this bot takes. The role, not a task.</div>
      </div>
      <div className="field">
        <label>Tools</label>
        <input className="in mono" value={f.tools} placeholder="web_search, fetch_url, memory_search"
          onChange={(e) => setF({ ...f, tools: e.target.value })} />
        <div className="hint">
          Empty means every tool this node has. A tool left out is one this bot is never even
          shown — that is how you limit what it can do.
        </div>
      </div>
      <div className="field">
        <label>Can message</label>
        <input className="in mono" value={f.may_message} placeholder="engineering, devops"
          onChange={(e) => setF({ ...f, may_message: e.target.value })} />
        <div className="hint">Loops are refused — the error names the path.</div>
      </div>

      {error && <Err error={error} />}

      <div className="row gap-sm">
        <button className="btn primary" onClick={() => save()} disabled={busy}>Save</button>
        {/* The coordinator answers your Telegram messages and the API
            refuses to remove it, so the control is absent rather than
            present and failing. */}
        {!bot.is_coordinator && (
          <button className="btn" onClick={() => save({ enabled: !bot.enabled })} disabled={busy}>
            {bot.enabled ? "Disable" : "Enable"}
          </button>
        )}
        <div className="grow" />
        <Link to={`/chat?bot=${bot.id}`}><button className="btn ghost sm">Chat →</button></Link>
      </div>
    </div>
  );
}
