import { NavLink, Navigate, Route, Routes, useLocation } from "react-router-dom";
import { api, type Bot, type InboxItem } from "./api";
import { LoginGate } from "./components/LoginGate";
import { Mascot } from "./components/Mascot";
import { Spinner, useLoad, when } from "./components/ui";
import { BotRoom } from "./routes/BotRoom";
import { Config } from "./routes/Config";
import { NewBot } from "./routes/NewBot";

export function App() {
  return <LoginGate><Shell /></LoginGate>;
}

/** The sidebar is a conversation list, not a roster.
 *
 * Every row carries the bot's most recent line. That is what makes a
 * sidebar feel populated rather than like a nav menu, and it answers
 * "what is everyone up to" before you click anything — the first
 * version listed five names and left the question to a separate page.
 */
function Shell() {
  const { data: bots, reload } = useLoad(() => api.listBots());
  // One activity call feeds every preview. Per-bot requests would be
  // N round-trips for a sidebar that is not the point of the page.
  const { data: feed, reload: reloadFeed } = useLoad(() => api.activity(120));
  const { pathname } = useLocation();

  const latest = new Map<string, InboxItem>();
  for (const item of feed ?? []) {
    if (!latest.has(item.recipient)) latest.set(item.recipient, item);
  }

  const refresh = () => { reload(); reloadFeed(); };

  return (
    <div className="shell">
      <aside className="side">
        <div className="brand"><i />lobslaw</div>

        <h6>Team</h6>
        <div className="roster">
          {(bots ?? []).map((b) => {
            const on = pathname === `/bots/${b.id}`;
            const item = latest.get(b.id);
            return (
              <NavLink key={b.id} to={`/bots/${b.id}`}
                className={`${on ? "on" : ""} ${b.enabled ? "" : "off"}`}>
                <Mascot id={b.id} size={30} dim={!b.enabled} />
                <div className="txt">
                  <div className="nm">
                    <span style={{ overflow: "hidden", textOverflow: "ellipsis" }}>
                      {b.display_name || b.id}
                    </span>
                    {b.is_coordinator && <i className="lead" title="coordinator" />}
                  </div>
                  <div className="last">
                    {item
                      ? `${item.result || item.error || item.subject}`
                      : b.description || "Nothing yet"}
                  </div>
                </div>
              </NavLink>
            );
          })}
          <NavLink to="/bots/new" className="newbot">
            <span className="plus">+</span>
            <div className="txt"><div className="nm">New bot</div></div>
          </NavLink>
        </div>

        <div className="side-foot">
          <NavLink to="/config">⚙ Config</NavLink>
        </div>
      </aside>

      <main className="main">
        <Routes>
          {/* Chat-first: opening the console lands you in a
              conversation with the coordinator, and clicking a bot
              opens ITS conversation. There is no separate activity
              page — a bot's queue is woven into its own thread, where
              the ordering against what you said still means
              something. */}
          <Route path="/" element={<Landing bots={bots} />} />
          <Route path="/config" element={<Config />} />
          <Route path="/bots/new" element={<NewBot onCreated={refresh} />} />
          <Route path="/bots/:botId" element={<BotRoom onChanged={refresh} />} />
          <Route path="*" element={<div className="empty"><b>Nothing here</b><span>That page does not exist.</span></div>} />
        </Routes>
      </main>
    </div>
  );
}

/** Landing sends you straight to the coordinator, or to the first bot
 * on a node that somehow has no coordinator. Waiting on the roster
 * before redirecting avoids a flash of "nothing here" on first load.
 */
function Landing({ bots }: { bots: Bot[] | null }) {
  if (!bots) return <Spinner />;
  const first = bots.find((b) => b.is_coordinator) ?? bots[0];
  if (!first) return <Navigate to="/bots/new" replace />;
  return <Navigate to={`/bots/${first.id}`} replace />;
}

/** The common frame: a title, an optional action, and a body. Repeated
 * per-screen padding is how a console drifts into looking like several
 * applications. */
export function Page({ title, sub, action, children }: {
  title: React.ReactNode; sub?: React.ReactNode;
  action?: React.ReactNode; children: React.ReactNode;
}) {
  return (
    <div className="wrap">
      <div className="head">
        <div className="grow">
          <h1>{title}</h1>
          {sub && <div className="sub">{sub}</div>}
        </div>
        {action}
      </div>
      {children}
    </div>
  );
}

export { when };
