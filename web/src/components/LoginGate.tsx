import { useCallback, useEffect, useState, type ReactNode } from "react";
import { ApiError, api, isUnavailable } from "../api";
import { Err, Spinner } from "./ui";

export function LoginGate({ children }: { children: ReactNode }) {
  const [userId, setUserId] = useState<string | null>(null);
  const [needLogin, setNeedLogin] = useState(false);
  const [error, setError] = useState<Error | null>(null);

  const refresh = useCallback(() => {
    api.session()
      .then((s) => { setUserId(s.user_id); setNeedLogin(false); setError(null); })
      .catch((e: Error) => {
        if (e instanceof ApiError && e.status === 401) {
          setNeedLogin(true);
          setUserId(null);
          setError(null);
          return;
        }
        setError(e);
      });
  }, []);
  useEffect(refresh, [refresh]);

  if (error) {
    return (
      <div className="center">
        <div style={{ maxWidth: 420 }}>
          {isUnavailable(error)
            ? <Unavailable />
            : <Err error={error} />}
        </div>
      </div>
    );
  }
  if (needLogin) return <SignIn onIn={refresh} />;
  if (!userId) return <div className="center"><Spinner /></div>;
  return <>{children}</>;
}

function Unavailable() {
  return (
    <div className="empty">
      <b>Console unavailable</b>
      <span>The node did not answer. It is not gone — try again when it is reachable.</span>
    </div>
  );
}

function SignIn({ onIn }: { onIn: () => void }) {
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
        <div className="brand" style={{ padding: "0 0 20px" }}>lobslaw</div>
        <div className="col gap">
          <div className="field">
            <label htmlFor="jwt">JWT</label>
            <input id="jwt" className="in" type="password" value={token} autoFocus
              onChange={(e) => setToken(e.target.value)}
              onKeyDown={(e) => { if (e.key === "Enter") void submit(); }} />
            <div className="hint">A Bearer token for an enrolled [[user]]. There is no self-signup.</div>
          </div>
          {error && <Err error={error} />}
          <button className="btn primary" onClick={() => void submit()} disabled={busy || !token}>Sign in</button>
        </div>
      </div>
    </div>
  );
}
