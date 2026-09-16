package gateway

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func consoleServer(t *testing.T, token string) *Server {
	t.Helper()
	key, err := DeriveConsoleKey([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("DeriveConsoleKey: %v", err)
	}
	return NewServer(RESTConfig{
		Bots:         newFakeBots(),
		ConsoleToken: token,
		ConsoleKey:   key,
		RequireAuth:  true,
		DefaultScope: "public",
	}, nil)
}

func login(t *testing.T, s *Server, token string) *http.Cookie {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/login",
		strings.NewReader(`{"token":"`+token+`"}`))
	rec := httptest.NewRecorder()
	s.handleLogin(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login status = %d: %s", rec.Code, rec.Body)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == consoleCookieName {
			return c
		}
	}
	t.Fatal("login set no session cookie")
	return nil
}

// The whole point: a console on a deployment that requires auth has a
// way in. Before this existed, enabling the console on a non-loopback
// bind correctly demanded require_auth and then left nothing able to
// satisfy it.
func TestLoginLetsTheConsoleReachAnAuthenticatedAPI(t *testing.T) {
	t.Parallel()
	s := consoleServer(t, "correct-horse")

	// Without a cookie the API is shut, which is the point of
	// require_auth.
	rec := httptest.NewRecorder()
	s.handleBots(rec, httptest.NewRequest(http.MethodGet, "/v1/bots", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status without a session = %d, want 401", rec.Code)
	}

	cookie := login(t, s, "correct-horse")

	req := httptest.NewRequest(http.MethodGet, "/v1/bots", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	s.handleBots(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status with a session = %d, want 200: %s", rec.Code, rec.Body)
	}
}

func TestLoginRefusesAWrongToken(t *testing.T) {
	t.Parallel()
	s := consoleServer(t, "correct-horse")

	rec := httptest.NewRecorder()
	s.handleLogin(rec, httptest.NewRequest(http.MethodPost, "/v1/auth/login",
		strings.NewReader(`{"token":"battery-staple"}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Error("a failed login set a cookie")
	}
}

// A prefix of the real token must not be accepted — the comparison is
// constant-time precisely so a shared secret reachable over the
// network is not a timing oracle.
func TestLoginRefusesAPrefixOfTheToken(t *testing.T) {
	t.Parallel()
	s := consoleServer(t, "correct-horse")
	rec := httptest.NewRecorder()
	s.handleLogin(rec, httptest.NewRequest(http.MethodPost, "/v1/auth/login",
		strings.NewReader(`{"token":"correct"}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for a prefix", rec.Code)
	}
}

// With nothing configured, say what to set. A bare 401 to somebody who
// has no way of knowing a token was never configured is a dead end.
func TestLoginWithNothingConfiguredSaysWhatToSet(t *testing.T) {
	t.Parallel()
	s := NewServer(RESTConfig{Bots: newFakeBots()}, nil)
	rec := httptest.NewRecorder()
	s.handleLogin(rec, httptest.NewRequest(http.MethodPost, "/v1/auth/login",
		strings.NewReader(`{"token":"anything"}`)))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "token_ref") {
		t.Errorf("the response does not name the setting: %s", rec.Body)
	}
}

// The cookie must not be readable by a script that gets into the page,
// and must not ride along on a cross-site request.
func TestSessionCookieIsHttpOnlyAndSameSiteStrict(t *testing.T) {
	t.Parallel()
	cookie := login(t, consoleServer(t, "tok"), "tok")
	if !cookie.HttpOnly {
		t.Error("the session cookie is readable from document.cookie")
	}
	if cookie.SameSite != http.SameSiteStrictMode {
		t.Errorf("SameSite = %v, want Strict", cookie.SameSite)
	}
	// Secure would make it undeliverable over a plain-HTTP loopback
	// console, which is the supported single-machine setup.
	if cookie.Secure {
		t.Error("Secure was set on a plaintext request; the cookie would never be sent")
	}
}

func TestTamperedTokenIsRejected(t *testing.T) {
	t.Parallel()
	s := consoleServer(t, "tok")
	cookie := login(t, s, "tok")

	// Re-sign nothing: flip the payload and keep the signature.
	body, sig, _ := strings.Cut(cookie.Value, ".")
	encoded, realSig, _ := strings.Cut(sig, ".")
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	var info consoleSessionInfo
	if err := json.Unmarshal(payload, &info); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	info.Scope = "root"
	forgedPayload, _ := json.Marshal(info)
	forged := body + "." + base64.RawURLEncoding.EncodeToString(forgedPayload) + "." + realSig

	req := httptest.NewRequest(http.MethodGet, "/v1/bots", nil)
	req.AddCookie(&http.Cookie{Name: consoleCookieName, Value: forged})
	rec := httptest.NewRecorder()
	s.handleBots(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 — a re-scoped payload was accepted", rec.Code)
	}
}

// The TTL is the revocation: there is no list to check, so a token
// past its expiry has to be refused on its own contents.
func TestExpiredTokenIsRejected(t *testing.T) {
	t.Parallel()
	key, err := DeriveConsoleKey([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("DeriveConsoleKey: %v", err)
	}
	expired, err := mintConsoleToken(key, consoleSessionInfo{
		Subject: consoleSubject, Scope: "owner",
		Expires: time.Now().Add(-time.Minute).Unix(),
	})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if _, err := verifyConsoleToken(key, expired); err == nil {
		t.Fatal("an expired token verified")
	} else if !strings.Contains(err.Error(), "sign in again") {
		t.Errorf("error does not say what to do: %v", err)
	}
}

// A cookie minted by one node has to be accepted by another, or a
// console behind a load balancer logs you out every other request.
func TestAnyNodeWithTheClusterKeyAcceptsTheCookie(t *testing.T) {
	t.Parallel()
	nodeA := consoleServer(t, "tok")
	nodeB := consoleServer(t, "tok")
	cookie := login(t, nodeA, "tok")

	req := httptest.NewRequest(http.MethodGet, "/v1/bots", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	nodeB.handleBots(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("a sibling node rejected the cookie: %d %s", rec.Code, rec.Body)
	}
}

// A different cluster key must not verify. The derivation is what
// scopes a session to one deployment.
func TestACookieFromAnotherClusterIsRejected(t *testing.T) {
	t.Parallel()
	other, err := DeriveConsoleKey([]byte("ffffffffffffffffffffffffffffffff"))
	if err != nil {
		t.Fatalf("DeriveConsoleKey: %v", err)
	}
	foreign, err := mintConsoleToken(other, consoleSessionInfo{
		Subject: consoleSubject, Scope: "owner",
		Expires: time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/bots", nil)
	req.AddCookie(&http.Cookie{Name: consoleCookieName, Value: foreign})
	rec := httptest.NewRecorder()
	consoleServer(t, "tok").handleBots(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for a foreign cluster's cookie", rec.Code)
	}
}

// The signing key must not be the memory key itself: a flaw that
// leaked one must not hand over the other.
func TestSigningKeyIsDerivedNotTheMemoryKey(t *testing.T) {
	t.Parallel()
	memoryKey := []byte("0123456789abcdef0123456789abcdef")
	derived, err := DeriveConsoleKey(memoryKey)
	if err != nil {
		t.Fatalf("DeriveConsoleKey: %v", err)
	}
	if string(derived) == string(memoryKey) {
		t.Error("the console signs with the at-rest encryption key itself")
	}
	if len(derived) != 32 {
		t.Errorf("derived key is %d bytes", len(derived))
	}
}

func TestLogoutClearsTheCookie(t *testing.T) {
	t.Parallel()
	s := consoleServer(t, "tok")
	rec := httptest.NewRecorder()
	s.handleLogin(rec, httptest.NewRequest(http.MethodDelete, "/v1/auth/login", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == consoleCookieName && c.MaxAge >= 0 {
			t.Errorf("logout did not expire the cookie: MaxAge=%d", c.MaxAge)
		}
	}
}

// whoami is how the console decides whether to show a login form
// rather than a wall of 401s.
func TestWhoamiReportsSignedInState(t *testing.T) {
	t.Parallel()
	s := consoleServer(t, "tok")

	rec := httptest.NewRecorder()
	s.handleWhoami(rec, httptest.NewRequest(http.MethodGet, "/v1/auth/whoami", nil))
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out["authenticated"] != false {
		t.Error("whoami claimed a signed-out request was authenticated")
	}
	if out["login_available"] != true {
		t.Error("whoami hid an available login form")
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/auth/whoami", nil)
	req.AddCookie(login(t, s, "tok"))
	rec = httptest.NewRecorder()
	s.handleWhoami(rec, req)
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out["authenticated"] != true {
		t.Error("whoami did not recognise a valid session")
	}
	// Signing in has to grant something. The unauthenticated scope
	// would make the console look broken rather than restricted.
	if out["scope"] != "owner" {
		t.Errorf("scope = %v, want owner", out["scope"])
	}
}
