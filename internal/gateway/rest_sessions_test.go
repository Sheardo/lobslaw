package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/jmylchreest/lobslaw/internal/compute"
	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
)

type fakeTranscripts struct {
	records  []*lobslawv1.SessionRecord
	messages map[string][]*lobslawv1.SessionMessage
}

func (f *fakeTranscripts) ListFiltered(_ context.Context, channel, _ string) ([]*lobslawv1.SessionRecord, error) {
	var out []*lobslawv1.SessionRecord
	for _, r := range f.records {
		if channel != "" && r.GetChannel() != channel {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func (f *fakeTranscripts) LoadMessages(_ context.Context, id string) ([]*lobslawv1.SessionMessage, error) {
	msgs, ok := f.messages[id]
	if !ok {
		return nil, errors.New("no such session")
	}
	return msgs, nil
}

// An inbox item carries a session_id. Without this route the console's
// answer to "what did the bot actually do" stops at the result string.
func TestSessionTranscriptIsReadable(t *testing.T) {
	t.Parallel()
	s := NewServer(RESTConfig{Transcripts: &fakeTranscripts{
		messages: map[string][]*lobslawv1.SessionMessage{
			"sess-1": {
				{Seq: 1, Role: "user", Content: "deploy staging"},
				{Seq: 2, Role: "assistant", Content: "deployed v0.4.2"},
			},
		},
	}, DefaultScope: "owner"}, nil)

	rec := httptest.NewRecorder()
	s.handleSession(rec, httptest.NewRequest(http.MethodGet, "/v1/sessions/sess-1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var got struct{ Messages []messageJSON }
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Messages) != 2 || got.Messages[1].Content != "deployed v0.4.2" {
		t.Errorf("transcript = %+v", got.Messages)
	}
}

func TestUnknownSessionIs404(t *testing.T) {
	t.Parallel()
	s := NewServer(RESTConfig{Transcripts: &fakeTranscripts{}, DefaultScope: "owner"}, nil)
	rec := httptest.NewRecorder()
	s.handleSession(rec, httptest.NewRequest(http.MethodGet, "/v1/sessions/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// "eng" must not claim "engineering"'s conversations. The leak only
// shows up once somebody names two bots similarly, which is exactly
// when nobody is looking for it.
func TestOneBotDoesNotClaimAnothersSessions(t *testing.T) {
	t.Parallel()
	s := NewServer(RESTConfig{Transcripts: &fakeTranscripts{
		records: []*lobslawv1.SessionRecord{
			{Id: "a", Channel: botChannel, ChannelId: "eng"},
			{Id: "b", Channel: botChannel, ChannelId: "engineering"},
			{Id: "c", Channel: botChannel, ChannelId: "engineering:thread-2"},
		},
	}, DefaultScope: "owner"}, nil)

	rec := httptest.NewRecorder()
	s.handleBotSessions(rec, httptest.NewRequest(http.MethodGet, "/v1/bots/eng/sessions", nil), "eng")
	var got struct{ Sessions []sessionJSON }
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Sessions) != 1 || got.Sessions[0].ID != "a" {
		t.Errorf("eng saw %+v; it must not claim engineering's", got.Sessions)
	}
}

func TestBelongsToBotMatchesOnTheSeparator(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"engineering":          true,
		"engineering:thread-1": true,
		"engineering-old":      false,
		"eng":                  false,
		"":                     false,
	}
	for id, want := range cases {
		if got := belongsToBot(id, "engineering"); got != want {
			t.Errorf("belongsToBot(%q) = %v, want %v", id, got, want)
		}
	}
}

// --- config view ---

// The allowlist is the guarantee. This walks the assembled view and
// fails on anything that looks like a credential rather than a
// reference to one — the belt to the allowlist's braces, because
// "should never happen" is how the last one got in.
func TestConfigViewCarriesNoSecrets(t *testing.T) {
	t.Parallel()
	view := &ConfigView{
		NodeID: "node-1",
		Gateway: ConfigGatewayView{
			Enabled: true, BindAddress: "127.0.0.1", HTTPPort: 8080, RequireAuth: true,
		},
		Compute: ConfigComputeView{
			Providers: []ConfigProviderRow{{Label: "anthropic", TrustTier: "trusted", Roles: []string{"main"}}},
		},
	}
	walkStrings(t, reflect.ValueOf(*view), "")
}

func walkStrings(t *testing.T, v reflect.Value, path string) {
	t.Helper()
	switch v.Kind() {
	case reflect.Struct:
		for i := range v.NumField() {
			name := v.Type().Field(i).Name
			walkStrings(t, v.Field(i), strings.TrimPrefix(path+"."+name, "."))
		}
	case reflect.Slice:
		for i := range v.Len() {
			walkStrings(t, v.Index(i), path)
		}
	case reflect.Pointer:
		if !v.IsNil() {
			walkStrings(t, v.Elem(), path)
		}
	case reflect.String:
		if looksLikeSecret(path, v.String()) {
			t.Errorf("%s carries what looks like a credential: %q", path, v.String())
		}
	}
}

func TestLooksLikeSecretAcceptsAReferenceAndRejectsAValue(t *testing.T) {
	t.Parallel()
	// A reference names WHERE a secret lives; that is what the operator
	// wrote in the file and is not itself sensitive.
	if looksLikeSecret("TokenRef", "env:LOBSLAW_CONSOLE_TOKEN") {
		t.Error("a secret reference was treated as a secret")
	}
	if looksLikeSecret("TokenRef", "file:/etc/lobslaw/token") {
		t.Error("a file reference was treated as a secret")
	}
	if !looksLikeSecret("ApiKey", "sk-live-abcdef") {
		t.Error("a literal credential was not caught")
	}
	if looksLikeSecret("BindAddress", "0.0.0.0") {
		t.Error("an ordinary field was flagged")
	}
}

// login_configured says WHETHER a token exists, never what it is — an
// operator locked out needs to know which of the two problems they
// have.
func TestConfigReportsWhetherLoginIsConfiguredWithoutTheToken(t *testing.T) {
	t.Parallel()
	s := NewServer(RESTConfig{
		Config:       &ConfigView{NodeID: "node-1"},
		ConsoleToken: "super-secret-token",
		ConsoleKey:   []byte("0123456789abcdef0123456789abcdef"),
		DefaultScope: "owner",
	}, nil)

	rec := httptest.NewRecorder()
	s.handleConfig(rec, httptest.NewRequest(http.MethodGet, "/v1/config", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if strings.Contains(body, "super-secret-token") {
		t.Fatal("the config view returned the console token itself")
	}
	if !strings.Contains(body, `"login_configured":true`) {
		t.Errorf("login_configured missing: %s", body)
	}
}

func TestConfigRequiresAuthWhenConfigured(t *testing.T) {
	t.Parallel()
	s := NewServer(RESTConfig{
		Config: &ConfigView{NodeID: "node-1"}, RequireAuth: true, DefaultScope: "public",
	}, nil)
	rec := httptest.NewRecorder()
	s.handleConfig(rec, httptest.NewRequest(http.MethodGet, "/v1/config", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

// --- per-bot chat ---

type fakeTurns struct {
	got   compute.TurnRequest
	reply string
	err   error
}

func (f *fakeTurns) Run(_ context.Context, req compute.TurnRequest) (*compute.ProcessMessageResponse, error) {
	f.got = req
	if f.err != nil {
		return nil, f.err
	}
	return &compute.ProcessMessageResponse{Reply: f.reply}, nil
}

// The console must be able to talk to a SPECIFIC bot. /v1/messages is
// the coordinator's, shared with Telegram, so without this every specialist
// is something you can configure but not converse with.
func TestBotChatRunsAsThatBotAndStreams(t *testing.T) {
	t.Parallel()
	turns := &fakeTurns{reply: "staging is on v0.4.2"}
	s := NewServer(RESTConfig{Turns: turns, DefaultScope: "owner"}, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/bots/engineering/messages",
		strings.NewReader(`{"message":"what is live?"}`))
	rec := httptest.NewRecorder()
	s.handleBotChat(rec, req, "engineering")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q, want an SSE stream", got)
	}
	if turns.got.BotID != "engineering" {
		t.Errorf("the turn ran as %q, not the bot that was addressed", turns.got.BotID)
	}
	body := rec.Body.String()
	for _, want := range []string{"event: start", "event: reply", "staging is on v0.4.2"} {
		if !strings.Contains(body, want) {
			t.Errorf("stream is missing %q:\n%s", want, body)
		}
	}
}

// A failed turn goes down the STREAM: the 200 and the headers are
// already on the wire by the time a turn can fail, and a client
// parsing SSE has nowhere to put a status that arrives afterwards.
func TestBotChatReportsFailureInTheStream(t *testing.T) {
	t.Parallel()
	turns := &fakeTurns{err: errors.New("provider unreachable")}
	s := NewServer(RESTConfig{Turns: turns, DefaultScope: "owner"}, nil)

	rec := httptest.NewRecorder()
	s.handleBotChat(rec, httptest.NewRequest(http.MethodPost, "/v1/bots/eng/messages",
		strings.NewReader(`{"message":"hi"}`)), "eng")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; the stream had already started", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "event: error") ||
		!strings.Contains(rec.Body.String(), "provider unreachable") {
		t.Errorf("the failure is not in the stream:\n%s", rec.Body)
	}
}

func TestBotChatRefusesAnEmptyMessage(t *testing.T) {
	t.Parallel()
	s := NewServer(RESTConfig{Turns: &fakeTurns{}, DefaultScope: "owner"}, nil)
	rec := httptest.NewRecorder()
	s.handleBotChat(rec, httptest.NewRequest(http.MethodPost, "/v1/bots/eng/messages",
		strings.NewReader(`{"message":"   "}`)), "eng")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// A console turn is attributed to the OPERATOR, the same way a
// scheduled routine is attributed to whoever scheduled it — the bot is
// doing the work, not owning it.
func TestBotChatAttributesToTheOperator(t *testing.T) {
	t.Parallel()
	turns := &fakeTurns{reply: "ok"}
	s := NewServer(RESTConfig{Turns: turns, DefaultScope: "owner"}, nil)

	rec := httptest.NewRecorder()
	s.handleBotChat(rec, httptest.NewRequest(http.MethodPost, "/v1/bots/eng/messages",
		strings.NewReader(`{"message":"hi"}`)), "eng")
	_ = rec

	if turns.got.Claims == nil {
		t.Fatal("the turn ran with no claims")
	}
	if turns.got.Claims.UserID == "eng" {
		t.Error("the turn was attributed to the bot rather than the person who asked")
	}
	if turns.got.Channel != botChannel {
		t.Errorf("Channel = %q, want the bot channel so the transcript is filed as the bot's",
			turns.got.Channel)
	}
}
