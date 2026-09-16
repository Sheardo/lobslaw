import { NavLink, Navigate, Route, Routes, useLocation } from "react-router-dom";
import { api } from "./api";
import { LoginGate } from "./components/LoginGate";
import { Avatar, useLoad } from "./components/ui";
import { Activity } from "./routes/Activity";
import { BotDetail } from "./routes/BotDetail";
import { Chat } from "./routes/Chat";
import { Config } from "./routes/Config";
import { NewBot } from "./routes/NewBot";

export function App() {
  return <LoginGate><Shell /></LoginGate>;
}

/** The roster is the frame, not a page.
 *
 * A console about a TEAM should show the team at all times. Putting
 * the bots behind a nav link made "who works here" a click away and
 * every other screen anonymous, which is most of why the first version
 * read as a CRUD admin panel rather than something with people in it.
 */
function Shell() {
  const { data: bots, reload } = useLoad(() => api.listBots());
  const { pathname } = useLocation();

  return (
    <div className="shell">
      <aside className="side">
        <div className="brand"><i />lobslaw</div>

        <nav className="nav">
          <NavLink to="/activity" className={({ isActive }) => (isActive ? "on" : "")}>Activity</NavLink>
          <NavLink to="/chat" className={({ isActive }) => (isActive ? "on" : "")}>Chat</NavLink>
          <NavLink to="/config" className={({ isActive }) => (isActive ? "on" : "")}>Config</NavLink>
        </nav>

        <h6>Team</h6>
        <div className="roster">
          {(bots ?? []).map((b) => {
            const on = pathname === `/bots/${b.id}`;
            return (
              <NavLink key={b.id} to={`/bots/${b.id}`}
                className={`${on ? "on" : ""} ${b.enabled ? "" : "off"}`}>
                <Avatar id={b.id} name={b.display_name} size={26} dim={!b.enabled} />
                <span className="nm">{b.display_name || b.id}</span>
                {b.is_coordinator && <i className="lead" title="coordinator" />}
              </NavLink>
            );
          })}
          <NavLink to="/bots/new" className="newbot">
            <span className="plus">+</span>
            <span className="nm">New bot</span>
          </NavLink>
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
          <Route path="/bots/new" element={<NewBot onCreated={reload} />} />
          <Route path="/bots/:botId" element={<BotDetail onChanged={reload} />} />
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
