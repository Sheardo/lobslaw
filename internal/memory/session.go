package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
)

// sessionApplyTimeout bounds raft.Apply for a session append. Larger
// than channel state's because a turn's payload is bigger (every tool
// result from the turn rides along), but still short enough that a
// wedged raft doesn't hold a user's reply hostage — the gateway
// degrades to its in-memory buffer when this fails.
const sessionApplyTimeout = 5 * time.Second

// Session retention defaults. The cap is in MESSAGES, not turns: one
// user→assistant exchange that calls four tools produces ~10 messages
// (user + 5 assistant + 4 tool results), so 200 covers roughly 20
// multi-tool exchanges of replayable context.
//
// This is a STORAGE bound, not a context-window bound. What actually
// goes to the LLM is whatever the caller asks for via LoadTail, and
// eventually what compaction leaves behind; the cap only stops a
// long-lived chat grinding through disk forever.
const (
	DefaultSessionMaxMessages = 200
	// sessionSeqWidth zero-pads sequence numbers in message keys so
	// bbolt's lexical key order matches numeric sequence order. 20
	// digits covers the full uint64 range, so ordering never breaks.
	sessionSeqWidth = 20
)

// ErrNotLeader is returned by session writes attempted on a follower.
// Callers that can degrade (the gateway's in-memory buffer) check for
// it and continue; callers that can't surface it.
var ErrNotLeader = errors.New("memory: not the raft leader")

// TranscriptMessage is the package-local shape of one conversation
// message. Deliberately duplicated from compute.Message rather than
// imported: internal/compute already depends on internal/memory (the
// context engine reads the store), so the reverse import would cycle.
// A thin adapter wires the two at the gateway boundary, the same way
// EpisodicTurn does.
type TranscriptMessage struct {
	Role       string
	Content    string
	ToolCalls  []TranscriptToolCall
	ToolCallID string
}

// TranscriptToolCall mirrors compute.ToolCall.
type TranscriptToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// SessionRef identifies one conversation.
type SessionRef struct {
	Channel   string
	ChannelID string
	// UserID is recorded on the index record so a later "show me my
	// conversations" can filter without decoding transcripts. Only
	// read on session creation; an established session keeps the
	// user it was opened with.
	UserID string
}

// SessionService is the raft-backed durable transcript store.
//
// Reads are local (straight off the FSM's bbolt), so any node can
// replay a conversation. Writes go through raft and are leader-only,
// matching every other write path in this package — there is no
// forwarding layer, so a follower-hosted turn gets ErrNotLeader and
// the caller decides whether that's fatal.
type SessionService struct {
	raft        *RaftNode
	store       *Store
	maxMessages int
}

// SessionConfig tunes the service. Zero values take the defaults.
type SessionConfig struct {
	// MaxMessages caps retained messages per session. Trimming drops
	// the oldest first. <= 0 takes DefaultSessionMaxMessages.
	MaxMessages int
}

// NewSessionService wires the service against an existing Raft +
// Store. A nil raft leaves reads working and writes failing, matching
// ChannelStateService's asymmetry.
func NewSessionService(raft *RaftNode, store *Store, cfg SessionConfig) *SessionService {
	maxMessages := cfg.MaxMessages
	if maxMessages <= 0 {
		maxMessages = DefaultSessionMaxMessages
	}
	return &SessionService{raft: raft, store: store, maxMessages: maxMessages}
}

// sessionID composes the bucket key. Neither component may contain
// ':' — the separator has to stay unambiguous because message keys
// nest another one underneath it. Same rule as channelStateKey.
func sessionID(channel, channelID string) (string, error) {
	if channel == "" {
		return "", errors.New("session: channel required")
	}
	if channelID == "" {
		return "", errors.New("session: channel_id required")
	}
	for _, s := range []string{channel, channelID} {
		if strings.ContainsRune(s, ':') {
			return "", fmt.Errorf("session: %q must not contain ':'", s)
		}
	}
	return channel + ":" + channelID, nil
}

// sessionMessagePrefix is the key range holding one session's
// transcript. The trailing ':' matters: without it session "rest:1"
// would also match "rest:10"'s messages.
func sessionMessagePrefix(id string) string {
	return id + ":"
}

// sessionMessageKey is prefix + zero-padded seq.
func sessionMessageKey(id string, seq uint64) string {
	return fmt.Sprintf("%s%0*d", sessionMessagePrefix(id), sessionSeqWidth, seq)
}

// Load returns the retained transcript in order, oldest first. A
// session that has never been written returns (nil, nil) — an absent
// conversation is not an error, it's a new one.
func (s *SessionService) Load(ctx context.Context, ref SessionRef) ([]TranscriptMessage, error) {
	return s.LoadTail(ctx, ref, 0)
}

// LoadTail returns at most the last n messages of the transcript,
// oldest first. n <= 0 means everything retained.
//
// Tail rather than head: when a caller can only afford part of a
// conversation, the recent part is the part that matters. Callers
// wanting a specific window can read the whole thing and slice.
func (s *SessionService) LoadTail(_ context.Context, ref SessionRef, n int) ([]TranscriptMessage, error) {
	if s.store == nil {
		return nil, errors.New("session: store not wired")
	}
	id, err := sessionID(ref.Channel, ref.ChannelID)
	if err != nil {
		return nil, err
	}
	var out []TranscriptMessage
	err = s.store.ForEachPrefix(BucketSessionMessages, sessionMessagePrefix(id),
		func(key string, raw []byte) error {
			var msg lobslawv1.SessionMessage
			if err := proto.Unmarshal(raw, &msg); err != nil {
				return fmt.Errorf("session: unmarshal %s: %w", key, err)
			}
			out = append(out, fromProtoMessage(&msg))
			return nil
		})
	if err != nil {
		return nil, err
	}
	if n > 0 && len(out) > n {
		out = out[len(out)-n:]
	}
	return out, nil
}

// Append records the messages a turn produced and returns the
// resulting index record.
//
// Trimming happens here, on the leader, and the evictions ride along
// inside the raft entry — see SessionAppendRecord's comment for why
// the FSM must not recompute them.
//
// System messages are dropped: promptgen rebuilds the system prompt
// every turn from live state, so a persisted copy would be stale the
// moment SOUL or the tool list changed.
func (s *SessionService) Append(_ context.Context, ref SessionRef, turnID string, msgs []TranscriptMessage) (*lobslawv1.SessionRecord, error) {
	if s.store == nil {
		return nil, errors.New("session: store not wired")
	}
	id, err := sessionID(ref.Channel, ref.ChannelID)
	if err != nil {
		return nil, err
	}
	if s.raft == nil {
		return nil, fmt.Errorf("%w: raft not wired", ErrNotLeader)
	}
	if !s.raft.IsLeader() {
		return nil, fmt.Errorf("%w; current leader is %s", ErrNotLeader, s.raft.LeaderAddress())
	}

	rec, err := s.loadRecord(id)
	if err != nil {
		return nil, err
	}
	now := timestamppb.Now()
	if rec == nil {
		rec = &lobslawv1.SessionRecord{
			Id:        id,
			Channel:   ref.Channel,
			ChannelId: ref.ChannelID,
			UserId:    ref.UserID,
			NextSeq:   1,
			FirstSeq:  1,
			CreatedAt: now,
		}
	}
	rec.UpdatedAt = now

	out := make([]*lobslawv1.SessionMessage, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == "system" {
			continue
		}
		out = append(out, toProtoMessage(id, rec.NextSeq, turnID, now, m))
		rec.NextSeq++
	}
	if len(out) == 0 {
		return rec, nil
	}

	// Retained range is [FirstSeq, NextSeq). Advance FirstSeq until
	// it fits the cap, collecting the keys the FSM should drop.
	var evict []string
	for rec.NextSeq-rec.FirstSeq > uint64(s.maxMessages) {
		evict = append(evict, sessionMessageKey(id, rec.FirstSeq))
		rec.FirstSeq++
	}

	entry := &lobslawv1.LogEntry{
		Op: lobslawv1.LogOp_LOG_OP_PUT,
		Id: id,
		Payload: &lobslawv1.LogEntry_SessionAppend{
			SessionAppend: &lobslawv1.SessionAppendRecord{
				Session:   rec,
				Messages:  out,
				EvictKeys: evict,
			},
		},
	}
	data, err := proto.Marshal(entry)
	if err != nil {
		return nil, fmt.Errorf("session: marshal: %w", err)
	}
	if _, err := s.raft.Apply(data, sessionApplyTimeout); err != nil {
		return nil, fmt.Errorf("session: raft apply: %w", err)
	}
	return rec, nil
}

// Forget drops a conversation and its whole transcript. Used by
// /reset and by the user asking the agent to forget a thread.
//
// Deliberately a hard delete, not a retention downgrade: a user
// saying "forget this conversation" means the bytes go away, and
// leaving them recoverable in a lower tier would betray that.
func (s *SessionService) Forget(_ context.Context, ref SessionRef) error {
	id, err := sessionID(ref.Channel, ref.ChannelID)
	if err != nil {
		return err
	}
	if s.raft == nil {
		return fmt.Errorf("%w: raft not wired", ErrNotLeader)
	}
	if !s.raft.IsLeader() {
		return fmt.Errorf("%w; current leader is %s", ErrNotLeader, s.raft.LeaderAddress())
	}
	entry := &lobslawv1.LogEntry{
		Op:      lobslawv1.LogOp_LOG_OP_DELETE,
		Id:      id,
		Payload: &lobslawv1.LogEntry_Session{Session: &lobslawv1.SessionRecord{Id: id}},
	}
	data, err := proto.Marshal(entry)
	if err != nil {
		return fmt.Errorf("session: marshal: %w", err)
	}
	if _, err := s.raft.Apply(data, sessionApplyTimeout); err != nil {
		return fmt.Errorf("session: raft apply: %w", err)
	}
	return nil
}

// List returns every session index record, unordered. Callers that
// need "the user's recent conversations" filter and sort — the set is
// small (one per live chat) so this stays cheap.
func (s *SessionService) List(_ context.Context) ([]*lobslawv1.SessionRecord, error) {
	if s.store == nil {
		return nil, errors.New("session: store not wired")
	}
	var out []*lobslawv1.SessionRecord
	err := s.store.ForEach(BucketSessions, func(key string, raw []byte) error {
		var rec lobslawv1.SessionRecord
		if err := proto.Unmarshal(raw, &rec); err != nil {
			return fmt.Errorf("session: unmarshal %s: %w", key, err)
		}
		out = append(out, &rec)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// loadRecord fetches the index record, returning (nil, nil) when the
// session doesn't exist yet.
func (s *SessionService) loadRecord(id string) (*lobslawv1.SessionRecord, error) {
	raw, err := s.store.Get(BucketSessions, id)
	if err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	var rec lobslawv1.SessionRecord
	if err := proto.Unmarshal(raw, &rec); err != nil {
		return nil, fmt.Errorf("session: unmarshal %s: %w", id, err)
	}
	return &rec, nil
}

func toProtoMessage(id string, seq uint64, turnID string, now *timestamppb.Timestamp, m TranscriptMessage) *lobslawv1.SessionMessage {
	msg := &lobslawv1.SessionMessage{
		SessionId:  id,
		Seq:        seq,
		Role:       m.Role,
		Content:    m.Content,
		ToolCallId: m.ToolCallID,
		TurnId:     turnID,
		Timestamp:  now,
	}
	for _, tc := range m.ToolCalls {
		msg.ToolCalls = append(msg.ToolCalls, &lobslawv1.SessionToolCall{
			Id:        tc.ID,
			Name:      tc.Name,
			Arguments: tc.Arguments,
		})
	}
	return msg
}

func fromProtoMessage(msg *lobslawv1.SessionMessage) TranscriptMessage {
	out := TranscriptMessage{
		Role:       msg.Role,
		Content:    msg.Content,
		ToolCallID: msg.ToolCallId,
	}
	for _, tc := range msg.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, TranscriptToolCall{
			ID:        tc.Id,
			Name:      tc.Name,
			Arguments: tc.Arguments,
		})
	}
	return out
}
