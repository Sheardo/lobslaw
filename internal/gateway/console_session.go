package gateway

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/hkdf"

	"github.com/jmylchreest/lobslaw/pkg/types"
)

// The console's way in.
//
// An operator opening the web console has no bearer token and no way
// to get one: an external IdP would need an OIDC redirect flow, and a
// personal assistant should not require standing one up to look at its
// own bots. So the console exchanges a configured shared secret for a
// short-lived signed cookie.
//
// Deliberately NOT a JWT. A JWT's value is that a third party can
// verify it; this token never leaves lobslaw, so the format would buy
// nothing and cost a library's worth of algorithm-confusion footguns.
// It is an opaque signed blob.
//
// The cookie and the Authorization header are two TRANSPORTS for one
// credential concept, not two authorities: Server.authenticate is
// still the single place that decides whether a request is
// authenticated, and it consults both.

const (
	// consoleCookieName is the browser cookie. Prefixed so it cannot
	// collide with anything an operator's reverse proxy sets.
	consoleCookieName = "lobslaw_console"

	// consoleTokenVersion prefixes every token. A format change gets a
	// new version rather than a silent reinterpretation of old bytes —
	// the failure mode otherwise is a token that verifies under new
	// rules while meaning something from the old ones.
	consoleTokenVersion = "v1"

	// DefaultConsoleSessionTTL bounds a console login.
	//
	// Twelve hours: long enough for a working day without re-entering
	// the token, short enough that a cookie copied off a machine is not
	// a standing credential. There is no revocation list — the TTL is
	// the revocation, which is the trade a stateless token makes.
	DefaultConsoleSessionTTL = 12 * time.Hour
)

// consoleSessionInfo is the token's payload.
type consoleSessionInfo struct {
	Subject string `json:"sub"`
	Scope   string `json:"scope"`
	Expires int64  `json:"exp"`
}

// DeriveConsoleKey derives the token-signing key from the cluster
// MemoryKey.
//
// Derived rather than configured so there is no third secret for an
// operator to manage, and derived rather than USED DIRECTLY so the
// signing key and the at-rest encryption key are different bytes: a
// flaw that leaked one must not hand over the other. Every node in the
// cluster holds the same MemoryKey, which is what lets a cookie minted
// by one node be accepted by another — a console behind a load
// balancer would otherwise log you out on every other request.
func DeriveConsoleKey(memoryKey []byte) ([]byte, error) {
	if len(memoryKey) == 0 {
		return nil, errors.New("console session: no cluster key to derive a signing key from")
	}
	out := make([]byte, 32)
	r := hkdf.New(sha256.New, memoryKey, nil, []byte("lobslaw console session v1"))
	if _, err := r.Read(out); err != nil {
		return nil, fmt.Errorf("console session: derive signing key: %w", err)
	}
	return out, nil
}

// mintConsoleToken signs a session.
func mintConsoleToken(key []byte, info consoleSessionInfo) (string, error) {
	payload, err := json.Marshal(info)
	if err != nil {
		return "", err
	}
	body := consoleTokenVersion + "." + base64.RawURLEncoding.EncodeToString(payload)
	return body + "." + base64.RawURLEncoding.EncodeToString(signConsole(key, body)), nil
}

// verifyConsoleToken checks the signature and the expiry.
//
// The signature is checked BEFORE the payload is unmarshalled, so a
// forged token never reaches the JSON decoder. Cheap discipline, and
// the alternative is a parser reachable by anyone who can reach the
// port.
func verifyConsoleToken(key []byte, raw string) (*consoleSessionInfo, error) {
	// "v1.<payload>.<signature>" — the signature covers everything
	// before the LAST separator, so the version travels signed and a
	// forged token cannot claim to be a different format.
	cut := strings.LastIndexByte(raw, '.')
	if cut <= 0 || cut == len(raw)-1 {
		return nil, errors.New("console session: malformed token")
	}
	body, sig := raw[:cut], raw[cut+1:]

	want, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return nil, errors.New("console session: malformed signature")
	}
	if !hmac.Equal(want, signConsole(key, body)) {
		return nil, errors.New("console session: bad signature")
	}

	version, encoded, ok := strings.Cut(body, ".")
	if !ok || version != consoleTokenVersion {
		return nil, fmt.Errorf("console session: unsupported token version %q", version)
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, errors.New("console session: malformed payload")
	}
	var info consoleSessionInfo
	if err := json.Unmarshal(payload, &info); err != nil {
		return nil, errors.New("console session: malformed payload")
	}
	if time.Now().Unix() >= info.Expires {
		return nil, errors.New("console session: expired; sign in again")
	}
	return &info, nil
}

func signConsole(key []byte, body string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(body))
	return mac.Sum(nil)
}

// handleLogin exchanges the configured console token for a cookie.
//
//	POST /v1/auth/login   {"token": "..."}   → sets the cookie
//	DELETE /v1/auth/login                    → clears it
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodDelete:
		s.clearConsoleCookie(w, r)
		w.WriteHeader(http.StatusNoContent)
		return
	case http.MethodPost:
	default:
		s.jsonErr(w, http.StatusMethodNotAllowed, "POST or DELETE")
		return
	}

	if s.cfg.ConsoleToken == "" || len(s.cfg.ConsoleKey) == 0 {
		s.jsonErr(w, http.StatusNotImplemented,
			"console login is not configured; set [gateway.ui] token_ref to a secret reference "+
				"(for example env:LOBSLAW_CONSOLE_TOKEN) and restart")
		return
	}

	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.jsonErr(w, http.StatusBadRequest, "malformed JSON: "+err.Error())
		return
	}
	// Constant-time, because a length-or-prefix comparison on a shared
	// secret reachable over the network is a timing oracle.
	if !hmac.Equal([]byte(body.Token), []byte(s.cfg.ConsoleToken)) {
		s.log.Warn("console: failed login", "remote", r.RemoteAddr)
		s.jsonErr(w, http.StatusUnauthorized, "that token is not right")
		return
	}

	ttl := s.cfg.ConsoleSessionTTL
	if ttl <= 0 {
		ttl = DefaultConsoleSessionTTL
	}
	expires := time.Now().Add(ttl)
	token, err := mintConsoleToken(s.cfg.ConsoleKey, consoleSessionInfo{
		Subject: consoleSubject,
		Scope:   s.consoleScope(),
		Expires: expires.Unix(),
	})
	if err != nil {
		s.jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:  consoleCookieName,
		Value: token,
		Path:  "/",
		// HttpOnly so a script injected into the console cannot read
		// the session out of document.cookie and post it elsewhere.
		HttpOnly: true,
		// Strict rather than Lax: every console action is a state
		// change on somebody's assistant, and there is no cross-site
		// flow this needs to survive.
		SameSite: http.SameSiteStrictMode,
		// Secure only over TLS. Setting it unconditionally would make
		// the cookie silently undeliverable on a plain-HTTP loopback
		// console, which is the supported single-machine setup.
		Secure:  r.TLS != nil,
		Expires: expires,
	})
	respondJSON(w, http.StatusOK, map[string]any{
		"scope":      s.consoleScope(),
		"expires_at": expires.UTC().Format(rfc3339),
	})
}

// handleWhoami tells the console whether it is signed in, so it can
// show a login form instead of a wall of 401s.
func (s *Server) handleWhoami(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.jsonErr(w, http.StatusMethodNotAllowed, "GET")
		return
	}
	claims, err := s.authenticate(r)
	if err != nil {
		respondJSON(w, http.StatusOK, map[string]any{
			"authenticated": false,
			// Whether a login form is worth showing at all.
			"login_available": s.cfg.ConsoleToken != "" && len(s.cfg.ConsoleKey) > 0,
			"reason":          err.Error(),
		})
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"authenticated":   true,
		"subject":         claims.UserID,
		"scope":           claims.Scope,
		"login_available": s.cfg.ConsoleToken != "" && len(s.cfg.ConsoleKey) > 0,
	})
}

func (s *Server) clearConsoleCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     consoleCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
		MaxAge:   -1,
	})
}

// consoleSubject is who a console session is. A distinct id from
// "anon" and from any JWT subject, so an audit entry says the action
// came through the web console rather than leaving somebody to guess.
const consoleSubject = "console"

func (s *Server) consoleScope() string {
	if s.cfg.ConsoleScope != "" {
		return s.cfg.ConsoleScope
	}
	// An operator at the console is the owner of the deployment. The
	// alternative — DefaultScope, which is the UNAUTHENTICATED scope —
	// would make signing in grant nothing, and the console would look
	// broken rather than restricted.
	return "owner"
}

// consoleSessionClaims returns the claims carried by a valid console
// cookie, or nil when there is no usable one.
//
// Absence is not an error here: a request with no cookie is the normal
// case for the API, and turning it into one would make every bearer
// call log a failure.
func (s *Server) consoleSessionClaims(r *http.Request) *types.Claims {
	if len(s.cfg.ConsoleKey) == 0 {
		return nil
	}
	cookie, err := r.Cookie(consoleCookieName)
	if err != nil || cookie.Value == "" {
		return nil
	}
	info, err := verifyConsoleToken(s.cfg.ConsoleKey, cookie.Value)
	if err != nil {
		s.log.Debug("console: rejecting session cookie", "err", err)
		return nil
	}
	return &types.Claims{UserID: info.Subject, Scope: info.Scope}
}
