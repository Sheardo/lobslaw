import { useEffect, useRef, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { api, streamBotChat } from "../api";
import { Err, Spinner, useLoad } from "../components/ui";
import { Mascot } from "../components/Mascot";
import { botVars } from "../theme";

interface Line { from: "me" | string; text: string; notice?: boolean }

/** Chat as the hero surface rather than a box on a page.
 *
 * Full height with a pinned picker and a sticky composer, because this
 * is the thing people keep open. The first version put it in the same
 * padded column as every admin screen, which made talking to your
 * assistant feel like filling in a form.
 */
export function Chat() {
  const [params, setParams] = useSearchParams();
  const { data: bots, error, loading } = useLoad(() => api.listBots());
  const [lines, setLines] = useState<Line[]>([]);
  const [draft, setDraft] = useState("");
  const [busy, setBusy] = useState(false);
  const [working, setWorking] = useState(false);
  const [sendErr, setSendErr] = useState<Error | null>(null);
  const end = useRef<HTMLDivElement>(null);

  // Falls through to the first bot rather than "": a chat addressed to
  // nobody has an empty placeholder and sends nowhere, which reads as
  // broken rather than as unconfigured.
  const sel = params.get("bot") ?? bots?.find((b) => b.is_coordinator)?.id ?? bots?.[0]?.id ?? "";
  const bot = bots?.find((b) => b.id === sel);

  useEffect(() => { end.current?.scrollIntoView({ behavior: "smooth" }); }, [lines, working]);

  async function send() {
    const text = draft.trim();
    if (!text || !sel) return;
    setLines((p) => [...p, { from: "me", text }]);
    setDraft(""); setBusy(true); setSendErr(null);
    try {
      await streamBotChat(sel, text, (event, data) => {
        if (event === "working") setWorking(true);
        if (event === "reply") {
          setWorking(false);
          setLines((p) => [...p, { from: sel, text: String(data.text ?? "") }]);
        }
        if (event === "needs_confirmation") {
          setWorking(false);
          setLines((p) => [...p, { from: sel, notice: true,
            text: `Needs a confirmation (${String(data.reason ?? "")}). ${String(data.note ?? "")}` }]);
        }
        if (event === "error") {
          setWorking(false);
          setSendErr(new Error(String(data.message ?? "the turn failed")));
        }
      });
    } catch (e) { setSendErr(e as Error); }
    finally { setBusy(false); setWorking(false); }
  }

  if (error) return <div className="wrap"><Err error={error} /></div>;
  if (loading && !bots) return <Spinner />;

  return (
    <div className="chat">
      {/* Who you are talking to, pinned. A chat where the other party
          is only named in a dropdown is one you send the wrong message
          into. */}
      <div className="picker">
        {(bots ?? []).map((b) => (
          <button key={b.id} style={botVars(b.id)}
            className={b.id === sel ? "pick on" : "pick"}
            onClick={() => {
              setParams({ bot: b.id });
              // A new bot is a new conversation. Carrying the old
              // transcript across shows a history the bot you are now
              // talking to has never seen.
              setLines([]); setSendErr(null);
            }}>
            <Mascot id={b.id} size={22} dim={!b.enabled} />
            {b.display_name || b.id}
          </button>
        ))}
      </div>

      <div className="thread">
        <div className="thread-in">
          {lines.length === 0 && !working && (
            <div className="opening">
              <Mascot id={sel || "coordinator"} size={54} />
              <div className="nm">{bot?.display_name || sel}</div>
              <div className="desc">{bot?.description || "Ask it something."}</div>
            </div>
          )}
          {lines.map((l, i) => <Msg key={i} line={l} name={bots?.find((b) => b.id === l.from)?.display_name} />)}
          {working && (
            <div className="msg" style={botVars(sel)}>
              <Mascot id={sel} size={30} />
              <div className="dots"><i /><i /><i /></div>
            </div>
          )}
          {sendErr && <Err error={sendErr} />}
          <div ref={end} />
        </div>
      </div>

      <div className="composer">
        <div className="composer-in">
          <textarea className="ta" rows={1} value={draft}
            placeholder={`Message ${bot?.display_name || sel}…`}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); void send(); }
            }} />
          <button className="btn primary" onClick={send} disabled={busy || !draft.trim() || !sel}
            style={{ height: 44 }}>Send</button>
        </div>
      </div>
    </div>
  );
}

function Msg({ line, name }: { line: Line; name?: string }) {
  if (line.from === "me") {
    return <div className="msg me"><div className="bubble">{line.text}</div></div>;
  }
  return (
    <div className="msg" style={botVars(line.from)}>
      <Mascot id={line.from} size={30} />
      <div className="grow">
        <div className="from">{name || line.from}</div>
        <div className={line.notice ? "txt notice" : "txt"}>{line.text}</div>
      </div>
    </div>
  );
}
