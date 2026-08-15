package main

import (
	"fmt"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/jmylchreest/lobslaw/internal/memory"
	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
)

// seedSession writes an index record plus a transcript. Message keys
// are "<session>:<20-digit zero-padded seq>" — the padding is what
// makes bbolt's byte order match sequence order, so the fixture has to
// reproduce it or a prefix scan comes back shuffled.
func seedSession(t *testing.T, ts *testStore, id, user string, contents []string) {
	t.Helper()
	ts.with(t, func(store *memory.Store) {
		channel, channelID, _ := strings.Cut(id, ":")
		rec := &lobslawv1.SessionRecord{
			Id: id, Channel: channel, ChannelId: channelID, UserId: user,
			Title: "seeded thread", FirstSeq: 1, NextSeq: uint64(len(contents)) + 1,
			CreatedAt: timestamppb.Now(), UpdatedAt: timestamppb.Now(),
		}
		raw, err := proto.Marshal(rec)
		if err != nil {
			t.Fatalf("marshal session: %v", err)
		}
		if err := store.Put(memory.BucketSessions, id, raw); err != nil {
			t.Fatalf("put session: %v", err)
		}
		for i, content := range contents {
			seq := uint64(i) + 1
			msg := &lobslawv1.SessionMessage{
				SessionId: id, Seq: seq, Role: "user", Content: content,
				Timestamp: timestamppb.Now(),
			}
			mraw, err := proto.Marshal(msg)
			if err != nil {
				t.Fatalf("marshal message: %v", err)
			}
			key := fmt.Sprintf("%s:%020d", id, seq)
			if err := store.Put(memory.BucketSessionMessages, key, mraw); err != nil {
				t.Fatalf("put message: %v", err)
			}
		}
	})
}

func TestSessionListAndShow(t *testing.T) {
	ts := newTestStore(t)
	seedSession(t, ts, "telegram:-100", "tg-@alice", []string{"first", "second"})

	if err := sessionList(ts.flags()); err != nil {
		t.Fatalf("session list: %v", err)
	}
	if err := sessionShow(ts.flags("telegram:-100")); err != nil {
		t.Fatalf("session show: %v", err)
	}

	ts.with(t, func(store *memory.Store) {
		msgs, err := loadMessages(store, "telegram:-100")
		if err != nil {
			t.Fatalf("loadMessages: %v", err)
		}
		if len(msgs) != 2 {
			t.Fatalf("loaded %d messages, want 2", len(msgs))
		}
		if msgs[0].Seq != 1 || msgs[1].Seq != 2 {
			t.Errorf("transcript out of sequence: %d, %d", msgs[0].Seq, msgs[1].Seq)
		}
	})
}

func TestSessionShowUnknownID(t *testing.T) {
	ts := newTestStore(t)
	err := sessionShow(ts.flags("telegram:-999"))
	if err == nil || !strings.Contains(err.Error(), "telegram:-999") {
		t.Errorf("session show of an unknown id = %v, want an error naming the id", err)
	}
}

func TestSessionSearchRequiresText(t *testing.T) {
	ts := newTestStore(t)
	if err := sessionSearch(ts.flags()); err == nil {
		t.Error("session search accepted an empty query; that is enumeration, which list already covers")
	}
}

func TestSessionSearchFindsTranscript(t *testing.T) {
	ts := newTestStore(t)
	seedSession(t, ts, "telegram:-100", "tg-@alice", []string{"the gateway is down", "never mind"})
	seedSession(t, ts, "rest:abc", "rest-bob", []string{"unrelated chatter"})

	if err := sessionSearch(ts.flags("gateway")); err != nil {
		t.Fatalf("session search: %v", err)
	}

	// Assert on the service the command drives rather than on stdout —
	// the point is that the CLI and the agent's session_search tool
	// resolve the same hits.
	ts.with(t, func(store *memory.Store) {
		svc := memory.NewSessionService(nil, store, memory.SessionConfig{})
		hits, err := svc.SearchTranscripts(t.Context(), memory.SessionSearchQuery{Text: "gateway"})
		if err != nil {
			t.Fatalf("SearchTranscripts: %v", err)
		}
		if len(hits) != 1 {
			t.Fatalf("got %d hits, want 1", len(hits))
		}
		if hits[0].Session.Id != "telegram:-100" {
			t.Errorf("hit = %s, want telegram:-100", hits[0].Session.Id)
		}
	})
}
