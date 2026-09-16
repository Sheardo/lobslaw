package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/jmylchreest/lobslaw/internal/memory"
	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
)

type fakeBots struct {
	byID map[string]*lobslawv1.BotRecord
}

func newFakeBots(records ...*lobslawv1.BotRecord) *fakeBots {
	f := &fakeBots{byID: map[string]*lobslawv1.BotRecord{}}
	for _, r := range records {
		f.byID[r.GetId()] = r
	}
	return f
}

func (f *fakeBots) List(context.Context) ([]*lobslawv1.BotRecord, error) {
	out := make([]*lobslawv1.BotRecord, 0, len(f.byID))
	for _, r := range f.byID {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GetId() < out[j].GetId() })
	return out, nil
}

func (f *fakeBots) Get(_ context.Context, id string) (*lobslawv1.BotRecord, error) {
	r, ok := f.byID[id]
	if !ok {
		return nil, fmt.Errorf("%w: %q", memory.ErrBotNotFound, id)
	}
	return proto.Clone(r).(*lobslawv1.BotRecord), nil
}

func (f *fakeBots) Put(_ context.Context, rec *lobslawv1.BotRecord, expected uint64) (*lobslawv1.BotRecord, error) {
	if cur, ok := f.byID[rec.GetId()]; ok && cur.GetRevision() != expected {
		return nil, memory.ErrClaimConflict
	}
	rec.Revision = expected + 1
	f.byID[rec.GetId()] = rec
	return rec, nil
}

func (f *fakeBots) Delete(_ context.Context, id string) error {
	if _, ok := f.byID[id]; !ok {
		return fmt.Errorf("%w: %q", memory.ErrBotNotFound, id)
	}
	delete(f.byID, id)
	return nil
}

type fakeInbox struct {
	items []*lobslawv1.BotInboxItem
	full  bool
}

func (f *fakeInbox) List(_ context.Context, recipient string, filter memory.InboxFilter) ([]*lobslawv1.BotInboxItem, error) {
	var out []*lobslawv1.BotInboxItem
	for _, item := range f.items {
		if item.GetRecipient() != recipient {
			continue
		}
		if len(filter.Statuses) > 0 && filter.Statuses[0] != item.GetStatus() {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

func (f *fakeInbox) Post(_ context.Context, item *lobslawv1.BotInboxItem) (*lobslawv1.BotInboxItem, error) {
	if f.full {
		return nil, fmt.Errorf("%w: full", memory.ErrInboxFull)
	}
	item.Id = fmt.Sprintf("item-%d", len(f.items)+1)
	item.Status = lobslawv1.InboxStatus_INBOX_STATUS_PENDING
	f.items = append(f.items, item)
	return item, nil
}

func (f *fakeInbox) Get(_ context.Context, recipient, id string) (*lobslawv1.BotInboxItem, error) {
	for _, item := range f.items {
		if item.GetRecipient() == recipient && item.GetId() == id {
			return item, nil
		}
	}
	return nil, fmt.Errorf("%w: %q", memory.ErrInboxNotFound, id)
}

func (f *fakeInbox) Cancel(ctx context.Context, recipient, id string) (*lobslawv1.BotInboxItem, error) {
	item, err := f.Get(ctx, recipient, id)
	if err != nil {
		return nil, err
	}
	item.Status = lobslawv1.InboxStatus_INBOX_STATUS_CANCELLED
	return item, nil
}

func (f *fakeInbox) Retry(ctx context.Context, recipient, id string) (*lobslawv1.BotInboxItem, error) {
	item, err := f.Get(ctx, recipient, id)
	if err != nil {
		return nil, err
	}
	item.Status = lobslawv1.InboxStatus_INBOX_STATUS_PENDING
	return item, nil
}

func (f *fakeInbox) Recipients(context.Context) ([]string, error) {
	seen := map[string]struct{}{}
	for _, item := range f.items {
		seen[item.GetRecipient()] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for r := range seen {
		out = append(out, r)
	}
	sort.Strings(out)
	return out, nil
}

func botServer(bots BotAPI, inbox InboxAPI) *Server {
	return NewServer(RESTConfig{Bots: bots, Inbox: inbox, DefaultScope: "owner"}, nil)
}

func do(t *testing.T, s *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	rec := httptest.NewRecorder()
	switch {
	case strings.HasPrefix(path, "/v1/inbox"):
		s.handleInboxItem(rec, req)
	case strings.HasPrefix(path, "/v1/activity"):
		s.handleActivity(rec, req)
	default:
		s.handleBots(rec, req)
	}
	return rec
}

func TestListBots(t *testing.T) {
	t.Parallel()
	s := botServer(newFakeBots(
		&lobslawv1.BotRecord{Id: "chief", IsChief: true, Enabled: true},
		&lobslawv1.BotRecord{Id: "engineering", Enabled: true},
	), nil)

	rec := do(t, s, http.MethodGet, "/v1/bots", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var got struct{ Bots []botJSON }
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Bots) != 2 {
		t.Fatalf("got %d bots", len(got.Bots))
	}
	// Empty slices, not null: a client should not have to special-case
	// "this bot has no edges".
	if got.Bots[0].Tools == nil || got.Bots[0].MayMessage == nil {
		t.Error("empty lists serialised as null")
	}
}

func TestCreateAndGetBot(t *testing.T) {
	t.Parallel()
	s := botServer(newFakeBots(), nil)

	rec := do(t, s, http.MethodPost, "/v1/bots",
		`{"id":"devops","display_name":"DevOps","instructions":"watch the cluster"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}

	rec = do(t, s, http.MethodGet, "/v1/bots/devops", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var got botJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Instructions != "watch the cluster" {
		t.Errorf("instructions = %q", got.Instructions)
	}
}

// PATCH leaves an omitted field alone. A GUI that cleared a bot's
// brief because somebody edited its display name would be a GUI you
// could not trust with the form.
func TestPatchLeavesOmittedFieldsAlone(t *testing.T) {
	t.Parallel()
	s := botServer(newFakeBots(&lobslawv1.BotRecord{
		Id: "engineering", DisplayName: "Engineering",
		Instructions: "the original brief", Enabled: true,
	}), nil)

	rec := do(t, s, http.MethodPatch, "/v1/bots/engineering", `{"display_name":"Eng"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var got botJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Instructions != "the original brief" {
		t.Errorf("instructions = %q; an omitted field was cleared", got.Instructions)
	}
	if got.DisplayName != "Eng" {
		t.Errorf("display_name = %q", got.DisplayName)
	}
}

// An explicit empty value IS a change — that is why the fields are
// pointers rather than plain strings.
func TestPatchCanClearAFieldExplicitly(t *testing.T) {
	t.Parallel()
	s := botServer(newFakeBots(&lobslawv1.BotRecord{
		Id: "engineering", Description: "the old one", Enabled: true,
	}), nil)

	rec := do(t, s, http.MethodPatch, "/v1/bots/engineering", `{"description":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var got botJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Description != "" {
		t.Errorf("description = %q; an explicit empty value was ignored", got.Description)
	}
}

// A GUI has to tell "you typed a name that does not exist" apart from
// "somebody edited this while your form was open".
func TestErrorsMapToDistinctStatuses(t *testing.T) {
	t.Parallel()
	s := botServer(newFakeBots(), nil)
	if rec := do(t, s, http.MethodGet, "/v1/bots/nobody", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown bot = %d, want 404", rec.Code)
	}
	if got := botStatusFor(memory.ErrClaimConflict); got != http.StatusConflict {
		t.Errorf("conflict = %d, want 409", got)
	}
	if got := inboxStatusFor(fmt.Errorf("%w: full", memory.ErrInboxFull)); got != http.StatusTooManyRequests {
		t.Errorf("full queue = %d, want 429 — the request was well-formed", got)
	}
}

func TestBotInboxListAndAssign(t *testing.T) {
	t.Parallel()
	inbox := &fakeInbox{}
	s := botServer(newFakeBots(&lobslawv1.BotRecord{Id: "engineering", Enabled: true}), inbox)

	rec := do(t, s, http.MethodPost, "/v1/bots/engineering/inbox",
		`{"subject":"deploy","body":"deploy the staging branch","kind":"task"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	// The browser is not the authority on who is asking.
	if got := inbox.items[0].GetSender(); got != "operator" {
		t.Errorf("sender = %q, want it stamped server-side", got)
	}

	rec = do(t, s, http.MethodGet, "/v1/bots/engineering/inbox", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var got struct{ Items []inboxItemJSON }
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Items) != 1 {
		t.Fatalf("listed %d items", len(got.Items))
	}
	// A listing omits bodies; the detail route carries them. Sending
	// every body in a queue view is how a page with a hundred items
	// becomes a megabyte.
	if got.Items[0].Body != "" {
		t.Error("the listing included a body")
	}
}

func TestInboxRetryAndCancel(t *testing.T) {
	t.Parallel()
	inbox := &fakeInbox{items: []*lobslawv1.BotInboxItem{{
		Id: "item-1", Recipient: "engineering",
		Status: lobslawv1.InboxStatus_INBOX_STATUS_FAILED,
	}}}
	s := botServer(newFakeBots(), inbox)

	rec := do(t, s, http.MethodPatch, "/v1/inbox/engineering/item-1", `{"action":"retry"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("retry status = %d: %s", rec.Code, rec.Body)
	}
	var got inboxItemJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Status != "pending" {
		t.Errorf("status after retry = %q", got.Status)
	}

	if rec := do(t, s, http.MethodPatch, "/v1/inbox/engineering/item-1", `{"action":"nonsense"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown action = %d, want 400", rec.Code)
	}
}

// The activity feed is the console's front page: every queue, newest
// first, so "what is the team doing" is one request.
func TestActivityIsNewestFirstAcrossEveryQueue(t *testing.T) {
	t.Parallel()
	inbox := &fakeInbox{items: []*lobslawv1.BotInboxItem{
		{Id: "01A", Recipient: "engineering"},
		{Id: "01C", Recipient: "marketing"},
		{Id: "01B", Recipient: "engineering"},
	}}
	s := botServer(newFakeBots(), inbox)

	rec := do(t, s, http.MethodGet, "/v1/activity", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var got struct{ Items []inboxItemJSON }
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	ids := make([]string, len(got.Items))
	for i, item := range got.Items {
		ids[i] = item.ID
	}
	if strings.Join(ids, ",") != "01C,01B,01A" {
		t.Errorf("order = %v, want newest first across both queues", ids)
	}
}

// A node that does not host raft answers 503 rather than 404: a
// console served by that node can then say what is wrong, instead of
// looking like the feature does not exist.
func TestNodeWithoutRegistryAnswers503(t *testing.T) {
	t.Parallel()
	s := NewServer(RESTConfig{DefaultScope: "owner"}, nil)
	rec := httptest.NewRecorder()
	s.handleBots(rec, httptest.NewRequest(http.MethodGet, "/v1/bots", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestBotRoutesRequireAuthWhenConfigured(t *testing.T) {
	t.Parallel()
	s := NewServer(RESTConfig{
		Bots: newFakeBots(), RequireAuth: true, DefaultScope: "owner",
	}, nil)
	rec := httptest.NewRecorder()
	s.handleBots(rec, httptest.NewRequest(http.MethodGet, "/v1/bots", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 with require_auth and no token", rec.Code)
	}
}

func TestDeleteBot(t *testing.T) {
	t.Parallel()
	bots := newFakeBots(&lobslawv1.BotRecord{Id: "temporary"})
	s := botServer(bots, nil)

	if rec := do(t, s, http.MethodDelete, "/v1/bots/temporary", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}
	if _, err := bots.Get(context.Background(), "temporary"); !errors.Is(err, memory.ErrBotNotFound) {
		t.Errorf("the bot survived the delete: %v", err)
	}
}
