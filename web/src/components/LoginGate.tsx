import { useCallback, useEffect, useState } from "react";
import { api, type Whoami } from "../api";
import { Err, Spinner } from "./ui";

/** Decides whether to show the console or a way in.
 *
 * It asks the node rather than inferring from a 401, because three
 * states need three screens: signed in, signed out with a way in, and
 * signed out with NO way in — the last being a deployment where
 * require_auth is on and no console token was configured. Rendering
 * that as "wrong password" would send somebody hunting for a password
 * that does not exist.
 */
export function LoginGate({ children }: { children: React.ReactNode }) {
  const [who, setWho] = useState<Whoami | null>(null);
  const [error, setError] = useState<Error | null>(null);

  const refresh = useCallback(() => {
    api.whoami().then(setWho).catch((e: Error) => setError(e));
  }, []);
  useEffect(refresh, [refresh]);

  if (error) return <div className="center"><div style={{ maxWidth: 420 }}><Err error={error} /></div></div>;
  if (!who) return <div className="center"><Spinner /></div>;
  if (who.authenticated) return <>{children}</>;
  return <SignIn who={who} onIn={refresh} />;
}

function SignIn({ who, onIn }: { who: Whoami; onIn: () => void }) {
  const [token, setToken] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<Error | null>(null);

  async function submit() {
    setBusy(true); setError(null);
    try { await api.login(token); setToken(""); onIn(); }
    catch (e) { setError(e as Error); }
    finally { setBusy(false); }
  }

  return (
    <div className="center">
      <div className="card pad" style={{ width: 380 }}>
        <div className="brand" style={{ padding: "0 0 20px" }}><i />lobslaw</div>
        {who.login_available ? (
          <div className="col gap">
            <div className="field">
              <label>Console token</label>
              <input className="in" type="password" value={token} autoFocus
                onChange={(e) => setToken(e.target.value)}
                onKeyDown={(e) => { if (e.key === "Enter") void submit(); }} />
              <div className="hint">The value behind [gateway.ui] token_ref.</div>
            </div>
            {error && <Err error={error} />}
            <button className="btn primary" onClick={submit} disabled={busy || !token}>Sign in</button>
          </div>
        ) : (
          /* No token configured. "Wrong password" here would send
             somebody hunting for one that does not exist. */
          <div className="col gap-sm">
            <p style={{ color: "var(--mid)", fontSize: 13.5, lineHeight: 1.6 }}>
              This node requires authentication but has no console token configured,
              so there is nothing to sign in with.
            </p>
            <pre className="card" style={{ padding: 12, fontFamily: "var(--mono)", fontSize: 12, color: "var(--mid)" }}>
{`[gateway.ui]
token_ref = "env:LOBSLAW_CONSOLE_TOKEN"`}
            </pre>
            <p style={{ color: "var(--low)", fontSize: 13, lineHeight: 1.6 }}>
              Set that and restart, or bind the gateway to 127.0.0.1 where the console
              needs no token at all.
            </p>
          </div>
        )}
      </div>
    </div>
  );
}
