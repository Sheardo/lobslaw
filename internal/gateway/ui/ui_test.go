package ui

import (
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// These skip rather than fail on a binary built without `make web`.
// A Go-only contributor should be able to run the suite without
// installing npm, and the alternative — a permanently red test on a
// clean checkout — is one people learn to ignore.
func handlerOrSkip(t *testing.T) http.Handler {
	t.Helper()
	h, err := Handler()
	if errors.Is(err, ErrNotBuilt) {
		t.Skip("web assets not built; run `make web`")
	}
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	return h
}

func TestServesTheAppShell(t *testing.T) {
	t.Parallel()
	h := handlerOrSkip(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<div id=\"root\">") {
		t.Error("the response is not the app shell")
	}
	// The shell changes every build, so caching it is how somebody ends
	// up running last week's console against this week's API.
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", got)
	}
}

// The routes the console owns exist only in the browser's router, so a
// reload on any page but the root has to fall through to the shell.
func TestDeepLinkFallsThroughToTheShell(t *testing.T) {
	t.Parallel()
	h := handlerOrSkip(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/bots/engineering", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; a reload on a bot page 404s", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<div id=\"root\">") {
		t.Error("the deep link did not serve the shell")
	}
}

// An unmatched /v1 path must 404 rather than fall through. The API
// shares this mux, and answering a typo'd endpoint with 200-and-HTML
// gives a client a parse error instead of a status it can act on.
func TestUnknownAPIPathsAreNotSwallowed(t *testing.T) {
	t.Parallel()
	h := handlerOrSkip(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/nonexistent", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 — an API typo must not return the app shell", rec.Code)
	}
}

// A real asset is served as itself rather than falling through to the
// shell — otherwise the page loads and every script tag returns HTML.
//
// The bundle's name carries a content hash that changes every build,
// so the test reads it out of the embedded FS instead of hard-coding
// one that would go stale on the next `make web`.
func TestRealAssetsAreServed(t *testing.T) {
	t.Parallel()
	h := handlerOrSkip(t)

	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		t.Fatalf("sub: %v", err)
	}
	entries, err := fs.ReadDir(sub, "assets")
	if err != nil {
		t.Skipf("no assets directory in this build: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("the build produced no assets")
	}
	name := "/assets/" + entries[0].Name()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, name, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d serving %s", rec.Code, name)
	}
	if strings.Contains(rec.Body.String(), "<div id=\"root\">") {
		t.Errorf("%s fell through to the app shell; every script tag would return HTML", name)
	}
}
