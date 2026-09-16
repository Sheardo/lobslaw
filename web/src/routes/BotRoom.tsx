import { useEffect, useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { api, streamBotChat, type Bot, type InboxItem, type TranscriptMessage } from "../api";
import { Mascot } from "../components/Mascot";
import { Err, Spinner, Status, useLoad, when } from "../components/ui";
import { botVars } from "../theme";

/** One bot, one room.
 *
 * Clicking a bot opens the CONVERSATION, not a settings form. That is
 * the whole shape of the change: the previous version made a bot
 * something you configure, with talking to it behind a separate page,
 * and the sidebar previews made that split read as a mistake.
 *
 * Its queue is woven into the same thread rather than living on a tab.
 * A bot that answered you at 14:02 and worked a scheduled task at
 * 14:05 did those things in one timeline, and splitting them across
 * two views makes you reconstruct the order by hand.
 */

type Entry =
  | { kind: "said"; from: "me" | "bot"; text: string; at: number; notice?: boolean }
  | { kind: "work"; item: InboxItem; at: number };

export function BotRoom({ onChanged }: { onChanged: () => void }) {
  const { botId = "" } = useParams();
  const { data: bot, error, loading, reload } = useLoad(() => api.getBot(botId), [botId]);
  const { data: work, reload: reloadWork } = useLoad(() => api.listInbox(botId, "all"), [botId]);
  const [said, setSaid] = useState<Entry[]>([]);
  const [draft, setDraft] = useState("");
  const [busy, setBusy] = useState(false);
  const [working, setWorking] = useState(false);
  const [sendErr, setSendErr] = useState<Error | null>(null);
  const [settings, setSettings] = useState(false);
  const end = useRef<HTMLDivElement>(null);

  // A new bot is a new room. Carrying the spoken lines across would
  // show a conversation the bot you just opened has never had.
  useEffect(() => { setSaid([]); setSendErr(null); setSettings(false); }, [botId]);

  // The queue moves without you. Polling keeps the thread honest
  // rather than frozen at whatever it was when you arrived.
  useEffect(() => {
    const t = setInterval(reloadWork, 8000);
    return () => clearInterval(t);
  }, [reloadWork]);

  useEffect(() => { end.current?.scrollIntoView({ behavior: "smooth" }); }, [said, work, working]);

  async function send() {
    const text = draft.trim();
    if (!text) return;
    setSaid((p) => [...p, { kind: "said", from: "me", text, at: Date.now() }]);
    setDraft(""); setBusy(true); setSendErr(null);
    try {
      await streamBotChat(botId, text, (event, data) => {
        if (event === "working") setWorking(true);
        if (event === "reply") {
          setWorking(false);
          setSaid((p) => [...p, { kind: "said", from: "bot", text: String(data.text ?? ""), at: Date.now() }]);
        }
        if (event === "needs_confirmation") {
          setWorking(false);
          setSaid((p) => [...p, { kind: "said", from: "bot", notice: true, at: Date.now(),
            text: `Needs a confirmation (${String(data.reason ?? "")}). ${String(data.note ?? "")}` }]);
        }
        if (event === "error") {
          setWorking(false);
          setSendErr(new Error(String(data.message ?? "the turn failed")));
        }
      });
      reloadWork();
    } catch (e) { setSendErr(e as Error); }
    finally { setBusy(false); setWorking(false); }
  }

  if (error) return <div className="wrap"><Err error={error} /></div>;
  if (loading && !bot) return <Spinner />;
  if (!bot) return null;

  // One timeline. Queue items and spoken lines interleave by time,
  // because that is the order they happened in.
  const thread: Entry[] = [
    ...(work ?? []).map((item) => ({
      kind: "work" as const, item,
      at: new Date(item.completed_at ?? item.created_at ?? 0).getTime(),
    })),
    ...said,
  ].sort((a, b) => a.at - b.at);

  return (
    <div className="chat" style={botVars(bot.id)}>
      <header className="room-head">
        <Mascot id={bot.id} size={30} dim={!bot.enabled} />
        <div className="grow">
          <div className="room-nm">
            {bot.display_name || bot.id}
            {bot.is_coordinator && <span className="tag brand">coordinator</span>}
            {!bot.enabled && <span className="tag warn">disabled</span>}
          </div>
          <div className="meta">{bot.description || `bot:${bot.id}`}</div>
        </div>
        <button className="btn ghost sm" onClick={() => setSettings((v) => !v)}>
          {settings ? "Close" : "Settings"}
        </button>
      </header>

      {settings ? (
        <div className="thread"><div className="thread-in">
          <Settings bot={bot} onSaved={() => { reload(); onChanged(); setSettings(false); }} />
        </div></div>
      ) : (
        <div className="thread">
          <div className="thread-in">
            {thread.length === 0 && !working && (
              <div className="opening">
                <Mascot id={bot.id} size={56} />
                <div className="nm">{bot.display_name || bot.id}</div>
                <div className="desc">{bot.description || "Ask it something."}</div>
              </div>
            )}
            {thread.map((e, i) =>
              e.kind === "said"
                ? <Said key={i} e={e} bot={bot} />
                : <Work key={e.item.id} item={e.item} botId={bot.id} onChanged={reloadWork} />)}
            {working && (
              <div className="msg">
                <Mascot id={bot.id} size={30} />
                <div className="dots"><i /><i /><i /></div>
              </div>
            )}
            {sendErr && <Err error={sendErr} />}
            <div ref={end} />
          </div>
        </div>
      )}

      {!settings && (
        <div className="composer">
          <div className="composer-in">
            <textarea className="ta" rows={1} value={draft}
              placeholder={`Message ${bot.display_name || bot.id}…`}
              onChange={(e) => setDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); void send(); }
              }} />
            <button className="btn primary" onClick={send} disabled={busy || !draft.trim()}
              style={{ height: 44 }}>Send</button>
          </div>
        </div>
      )}
    </div>
  );
}

function Said({ e, bot }: { e: Extract<Entry, { kind: "said" }>; bot: Bot }) {
  if (e.from === "me") {
    return <div className="msg me"><div className="bubble">{e.text}</div></div>;
  }
  return (
    <div className="msg">
      <Mascot id={bot.id} size={30} />
      <div className="grow">
        <div className="from">{bot.display_name || bot.id}</div>
        <div className={e.notice ? "txt notice" : "txt"}>{e.text}</div>
      </div>
    </div>
  );
}

/** A queue item, rendered inline in the thread.
 *
 * Visually distinct from a spoken line — indented, quieter, with its
 * own affordances — because "you asked me this" and "something made me
 * do this" are different events and flattening them would misrepresent
 * who started what.
 */
function Work({ item, botId, onChanged }: { item: InboxItem; botId: string; onChanged: () => void }) {
  const [open, setOpen] = useState(false);
  const [full, setFull] = useState<InboxItem | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<Error | null>(null);

  async function toggle() {
    const next = !open; setOpen(next);
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
    <div className="work">
      <div className="work-top" onClick={toggle}>
        <Status status={item.status} />
        <span className="work-ttl">{item.subject || "(no subject)"}</span>
        <span className="meta">
          {item.sender === "operator" ? "you assigned" : `from ${item.sender}`} · {when(item.created_at)}
          {item.attempts > 1 && ` · ${item.attempts} attempts`}
        </span>
        <span className="grow" />
        {(item.status === "failed" || item.status === "cancelled") && (
          <button className="btn sm" onClick={(e) => { e.stopPropagation(); act("retry"); }} disabled={busy}>Retry</button>
        )}
        {item.status === "pending" && (
          <button className="btn ghost sm" onClick={(e) => { e.stopPropagation(); act("cancel"); }} disabled={busy}>Cancel</button>
        )}
      </div>
      {item.result && !open && <div className="work-body">{item.result}</div>}
      {/* A failed item keeps its error, visibly. A task that vanished
          quietly is the failure the queue exists to prevent. */}
      {item.error && !open && <div className="work-body bad">{item.error}</div>}
      {error && <Err error={error} />}
      {open && (
        <div className="work-detail">
          <Block label="Asked" body={d.body} />
          {d.result && <Block label="Result" body={d.result} />}
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
      <div style={{ fontSize: 13.5, lineHeight: 1.7, marginTop: 5, whiteSpace: "pre-wrap",
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
        {open ? "▾" : "▸"} what it did
      </button>
      {error && <Err error={error} />}
      {open && msgs && (
        <div className="col gap-sm" style={{ marginTop: 8, paddingLeft: 12, borderLeft: "1px solid var(--edge)" }}>
          {msgs.map((m) => (
            <div key={m.seq}>
              <div className="lbl">{m.role}{m.tool_calls ? ` · ${m.tool_calls} tool calls` : ""}</div>
              <div style={{ fontSize: 13, color: "var(--mid)", whiteSpace: "pre-wrap", marginTop: 2 }}>
                {m.content || "(tool calls only)"}
              </div>
            </div>
          ))}
        </div>
      )}
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
    <div className="card pad col gap-lg">
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
        <Link to="/config"><button className="btn ghost sm">Node config →</button></Link>
      </div>
    </div>
  );
}
