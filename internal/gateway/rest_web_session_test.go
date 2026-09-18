package gateway

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	jwtlib "github.com/golang-jwt/jwt/v5"
)

func TestSessionCookieSetAfterJWTLogin(t *testing.T) {
	t.Parallel()
	srv := startWebREST(t, &captureRunner{}, nil)
	token := mintJWTWith(t, "alice@idp", nil)

	resp := doJSON(t, http.MethodPost, webBaseURL(srv)+"/v1/session", "", http.Header{
		"Authorization": []string{"Bearer " + token},
	})
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("login status %d body=%s", resp.StatusCode, body)
	}
	cookie := loginCookie(resp)
	if cookie == nil {
		t.Fatal("login did not Set-Cookie " + LoginCookieName)
	}
	if !cookie.HttpOnly {
		t.Error("login cookie must be HttpOnly")
	}
	if cookie.SameSite != http.SameSiteStrictMode {
		t.Errorf("SameSite = %v, want Strict", cookie.SameSite)
	}
	if cookie.Value == "" || strings.Contains(cookie.Value, "alice") {
		t.Errorf("cookie value %q must be an opaque id, not the user id", cookie.Value)
	}
}

func TestRevokeMakesSubsequentAPI401(t *testing.T) {
	t.Parallel()
	srv := startWebREST(t, &captureRunner{}, nil)
	token := mintJWTWith(t, "alice@idp", nil)
	base := webBaseURL(srv)

	login := doJSON(t, http.MethodPost, base+"/v1/session", "", http.Header{
		"Authorization": []string{"Bearer " + token},
	})
	if login.StatusCode != http.StatusOK {
		t.Fatalf("login: %d", login.StatusCode)
	}
	cookie := loginCookie(login)
	if cookie == nil {
		t.Fatal("missing login cookie")
	}

	plan := doJSON(t, http.MethodGet, base+"/v1/plan", "", cookieHeader(cookie))
	if plan.StatusCode != http.StatusOK {
		t.Fatalf("plan with cookie: %d", plan.StatusCode)
	}

	rev := doJSON(t, http.MethodDelete, base+"/v1/session", "", cookieHeader(cookie, originFor(base)))
	if rev.StatusCode != http.StatusOK {
		t.Fatalf("revoke: %d", rev.StatusCode)
	}

	again := doJSON(t, http.MethodGet, base+"/v1/plan", "", cookieHeader(cookie))
	if again.StatusCode != http.StatusUnauthorized {
		t.Errorf("plan after revoke = %d, want 401", again.StatusCode)
	}
}

func TestRevokeCancelsActiveStream(t *testing.T) {
	t.Parallel()
	runner := &captureRunner{hold: make(chan struct{})}
	srv := startWebREST(t, runner, nil)
	token := mintJWTWith(t, "alice@idp", nil)
	base := webBaseURL(srv)

	login := doJSON(t, http.MethodPost, base+"/v1/session", "", http.Header{
		"Authorization": []string{"Bearer " + token},
	})
	cookie := loginCookie(login)
	if cookie == nil {
		t.Fatal("missing login cookie")
	}

	started := make(chan *http.Response, 1)
	go func() {
		req, err := http.NewRequest(http.MethodPost, base+"/v1/messages", strings.NewReader(`{"message":"stream me"}`))
		if err != nil {
			t.Error(err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("Origin", originValue(base))
		req.AddCookie(cookie)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			started <- nil
			return
		}
		started <- resp
	}()

	deadline := time.Now().Add(2 * time.Second)
	for runner.lastRequest().Message == "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if runner.lastRequest().Message == "" {
		t.Fatal("streamed turn never reached the runner")
	}

	rev := doJSON(t, http.MethodDelete, base+"/v1/session", "", cookieHeader(cookie, originFor(base)))
	if rev.StatusCode != http.StatusOK {
		t.Fatalf("revoke: %d", rev.StatusCode)
	}

	select {
	case resp := <-started:
		if resp == nil {
			return
		}
		defer func() { _ = resp.Body.Close() }()
		_, _ = io.Copy(io.Discard, resp.Body)
	case <-time.After(2 * time.Second):
		t.Fatal("revoke did not drop the active stream")
	}
}

func TestCookieCSRFRejectsCrossOriginPOST(t *testing.T) {
	t.Parallel()
	srv := startWebREST(t, &captureRunner{}, nil)
	token := mintJWTWith(t, "alice@idp", nil)
	base := webBaseURL(srv)
	login := doJSON(t, http.MethodPost, base+"/v1/session", "", http.Header{
		"Authorization": []string{"Bearer " + token},
	})
	cookie := loginCookie(login)
	if cookie == nil {
		t.Fatal("missing login cookie")
	}

	h := cookieHeader(cookie)
	h.Set("Origin", "https://evil.example")
	resp := doJSON(t, http.MethodPost, base+"/v1/messages", `{"message":"hi"}`, h)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin cookie POST = %d, want 403", resp.StatusCode)
	}
}

func TestWebLoginDoesNotGrantOperator(t *testing.T) {
	t.Parallel()
	runner := &captureRunner{}
	srv := startWebREST(t, runner, nil)
	token := mintJWTWith(t, "alice@idp", jwtlibRoles("operator"))
	base := webBaseURL(srv)

	login := doJSON(t, http.MethodPost, base+"/v1/session", "", http.Header{
		"Authorization": []string{"Bearer " + token},
	})
	cookie := loginCookie(login)
	if cookie == nil {
		t.Fatal("missing login cookie")
	}

	resp := doJSON(t, http.MethodPost, base+"/v1/messages", `{"message":"hi"}`,
		cookieHeader(cookie, originFor(base)))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("messages: %d", resp.StatusCode)
	}
	got := runner.lastRequest()
	if got.Claims != nil && got.Claims.HasRole("operator") {
		t.Fatal("web login copied JWT role:operator onto the session")
	}
}

func TestUnenrolledJWTCannotMintACookie(t *testing.T) {
	t.Parallel()
	srv := startWebREST(t, &captureRunner{}, nil)
	token := mintJWTWith(t, "stranger@idp", nil)
	resp := doJSON(t, http.MethodPost, webBaseURL(srv)+"/v1/session", "", http.Header{
		"Authorization": []string{"Bearer " + token},
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("unenrolled login = %d, want 403 (no self-signup)", resp.StatusCode)
	}
}

func loginCookie(resp *http.Response) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == LoginCookieName {
			return c
		}
	}
	return nil
}

func cookieHeader(cookie *http.Cookie, extra ...http.Header) http.Header {
	h := http.Header{}
	h.Add("Cookie", cookie.Name+"="+cookie.Value)
	for _, e := range extra {
		for k, vs := range e {
			for _, v := range vs {
				h.Add(k, v)
			}
		}
	}
	return h
}

func originFor(base string) http.Header {
	return http.Header{"Origin": []string{originValue(base)}}
}

func originValue(base string) string {
	return strings.TrimRight(base, "/")
}

func jwtlibRoles(roles ...string) jwtlib.MapClaims {
	return jwtlib.MapClaims{"roles": roles}
}
