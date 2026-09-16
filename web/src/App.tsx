import { NavLink, Navigate, Route, Routes, useLocation } from "react-router-dom";
import { api, type InboxItem } from "./api";
import { LoginGate } from "./components/LoginGate";
import { Mascot } from "./components/Mascot";
import { useLoad, when } from "./components/ui";
import { Activity } from "./routes/Activity";
import { BotDetail } from "./routes/BotDetail";
import { Chat } from "./routes/Chat";
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

        <nav className="nav">
          <NavLink to="/activity" className={({ isActive }) => (isActive ? "on" : "")}>Activity</NavLink>
          <NavLink to="/chat" className={({ isActive }) => (isActive ? "on" : "")}>Chat</NavLink>
        </nav>

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
          {/* Activity first. The question somebody opens this to answer
              is "what is the team doing"; the sidebar already answers
              "who exists", permanently. */}
          <Route path="/" element={<Navigate to="/activity" replace />} />
          <Route path="/activity" element={<Activity />} />
          <Route path="/chat" element={<Chat />} />
          <Route path="/config" element={<Config />} />
          <Route path="/bots/new" element={<NewBot onCreated={refresh} />} />
          <Route path="/bots/:botId" element={<BotDetail onChanged={refresh} />} />
          <Route path="*" element={<div className="empty"><b>Nothing here</b><span>That page does not exist.</span></div>} />
        </Routes>
      </main>
    </div>
  );
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
