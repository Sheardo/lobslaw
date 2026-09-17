package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jmylchreest/lobslaw/pkg/types"
)

// A session has to know who is in it.
//
// The console was one shared secret, so every session was the same
// anonymous subject: nothing recorded who rewrote a bot's brief, and
// the roles an operator had already declared in [[user]] had nowhere
// to apply. Signing in as a person now produces that person's
// principal and their roles.
func TestAPerUserLoginCarriesThatPerson(t *testing.T) {
	t.Parallel()

	key, err := DeriveConsoleKey([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("DeriveConsoleKey: %v", err)
	}
	s := NewServer(RESTConfig{
		ConsoleKey:   key,
		ConsoleToken: "shared-secret",
		ConsoleUsers: []ConsoleUser{
			{ID: "james", DisplayName: "James", Token: "james-token", Roles: []string{"operator"}},
			{ID: "sam", DisplayName: "Sam", Token: "sam-token"},
		},
		DefaultScope: "owner",
	}, nil)

	login := func(token string) (*http.Response, map[string]any) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/auth/login",
			strings.NewReader(`{"token":"`+token+`"}`))
		s.handleLogin(rec, req)
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return rec.Result(), body
	}

	t.Run("a person's token names them", func(t *testing.T) {
		resp, body := login("james-token")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d: %v", resp.StatusCode, body)
		}
		if body["user"] != "user:james" {
			t.Errorf("user = %v, want the principal form", body["user"])
		}
		// The principal form matters: a console session and a Telegram
		// message from the same person must resolve to one identity,
		// or their memory and their work do not follow them across.
		claims := claimsFromLogin(t, s, resp)
		if claims.UserID != "user:james" {
			t.Errorf("session subject = %q", claims.UserID)
		}
		if len(claims.Roles) != 1 || claims.Roles[0] != "operator" {
			t.Errorf("roles = %v, want the ones declared in [[user]]", claims.Roles)
		}
	})

	t.Run("the shared token still works and stays anonymous", func(t *testing.T) {
		resp, body := login("shared-secret")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("the shared token stopped working: %v", body)
		}
		if body["user"] != "console" {
			t.Errorf("user = %v, want the anonymous console subject", body["user"])
		}
	})

	t.Run("a token belonging to nobody is refused", func(t *testing.T) {
		resp, _ := login("not-a-token")
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", resp.StatusCode)
		}
	})

	t.Run("one person's token does not become another's session", func(t *testing.T) {
		resp, body := login("sam-token")
		if body["user"] != "user:sam" {
			t.Errorf("user = %v, want sam", body["user"])
		}
		claims := claimsFromLogin(t, s, resp)
		if len(claims.Roles) != 0 {
			t.Errorf("sam picked up roles they were not given: %v", claims.Roles)
		}
	})
}

// claimsFromLogin replays the session cookie the way a later request
// would, so the assertion is on what the server will actually see.
func claimsFromLogin(t *testing.T, s *Server, resp *http.Response) *types.Claims {
	t.Helper()
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == consoleCookieName {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("login set no session cookie")
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/bots", nil)
	req.AddCookie(cookie)
	claims := s.consoleSessionClaims(req)
	if claims == nil {
		t.Fatal("the session cookie did not verify")
	}
	return claims
}
