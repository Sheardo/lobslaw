package memory

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/jmylchreest/lobslaw/internal/bots"
	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
	"github.com/jmylchreest/lobslaw/pkg/types"
)

// botApplyTimeout caps raft.Apply for a bot write. Creating or
// re-instructing a bot is a human-pace action, so 5s is generous —
// the same figure soul writes use, for the same reason.
const botApplyTimeout = 5 * time.Second

// MaxBotInstructions bounds a bot's standing brief.
//
// The brief rides on every one of that bot's turns, so an unbounded
// one is an unbounded per-turn tax that nothing else in the system
// would ever report as the cause. 8 KB is several screens of prose —
// far more than a role description needs, and small enough that a
// hundred bots still snapshot quickly.
const MaxBotInstructions = 8 << 10

// botIDPattern is what an id may look like. Restrictive on purpose:
// the id becomes a principal ("bot:engineering"), a soul-overlay key
// suffix, and a policy-rule subject, so a colon or a space in one
// would silently change what a rule matches.
var botIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// ErrBotNotFound is returned for an unknown bot id. Distinct from a
// store failure: callers routinely ask about a bot that may not exist.
var ErrBotNotFound = errors.New("bots: no such bot")

// BotService is the raft-backed registry of named agents.
//
// Reads are local, straight off the FSM's bbolt, so any node can
// answer "who is the engineering bot". Writes go through raft with a
// revision check, so two operators editing one bot's instructions
// cannot silently lose an edit — the same CAS contract SoulTuneService
// uses, for the same reason.
type BotService struct {
	raft  *RaftNode
	store *Store
}

// NewBotService wires the registry against an existing Raft + Store.
// Nil raft leaves reads working and writes failing, matching the
// asymmetry every other service here has.
func NewBotService(raft *RaftNode, store *Store) *BotService {
	return &BotService{raft: raft, store: store}
}

// Get returns one bot. ErrBotNotFound when the id is unknown.
func (s *BotService) Get(_ context.Context, id string) (*lobslawv1.BotRecord, error) {
	if s.store == nil {
		return nil, errors.New("bots: store not wired")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, ErrBotNotFound
	}
	raw, err := s.store.Get(BucketBots, id)
	if err != nil {
		if errors.Is(err, types.ErrNotFound) {
			return nil, fmt.Errorf("%w: %q", ErrBotNotFound, id)
		}
		return nil, err
	}
	var rec lobslawv1.BotRecord
	if err := proto.Unmarshal(raw, &rec); err != nil {
		return nil, fmt.Errorf("bots: unmarshal %q: %w", id, err)
	}
	return &rec, nil
}

// List returns every bot, chief first and the rest by id.
//
// Chief first because every caller that renders a list wants it there
// and sorting it into the middle of the alphabet reads as a bug.
func (s *BotService) List(_ context.Context) ([]*lobslawv1.BotRecord, error) {
	if s.store == nil {
		return nil, errors.New("bots: store not wired")
	}
	var out []*lobslawv1.BotRecord
	err := s.store.ForEach(BucketBots, func(key string, value []byte) error {
		var rec lobslawv1.BotRecord
		if err := proto.Unmarshal(value, &rec); err != nil {
			return fmt.Errorf("bots: unmarshal %q: %w", key, err)
		}
		out = append(out, &rec)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].GetIsChief() != out[j].GetIsChief() {
			return out[i].GetIsChief()
		}
		return out[i].GetId() < out[j].GetId()
	})
	return out, nil
}

// Put writes a bot, checking expectedRevision against the stored
// record. Zero is the expected revision for a create.
//
// The record is validated here rather than at each call site: the GUI,
// the chief's bot_create tool and the first-boot seed all write
// through this one door, and a validation rule enforced at two of
// three doors is a rule that is not enforced.
func (s *BotService) Put(ctx context.Context, rec *lobslawv1.BotRecord, expectedRevision uint64) (*lobslawv1.BotRecord, error) {
	if rec == nil {
		return nil, errors.New("bots: record required")
	}
	if s.raft == nil {
		return nil, errors.New("bots: raft not wired")
	}
	rec = proto.Clone(rec).(*lobslawv1.BotRecord)
	if err := validateBot(rec); err != nil {
		return nil, err
	}

	if err := s.checkMessageGraph(ctx, rec); err != nil {
		return nil, err
	}

	prev, err := s.Get(ctx, rec.GetId())
	switch {
	case err == nil:
		if prev.GetRevision() != expectedRevision {
			return nil, fmt.Errorf("%w: bot %q changed; read it again and retry", ErrClaimConflict, rec.GetId())
		}
		// The chief flag and creation stamp are properties of the
		// record's history, not of whatever the caller happened to
		// send. Letting an update carry them would let an edit
		// promote a bot to chief, and there can only be one.
		rec.IsChief = prev.GetIsChief()
		rec.CreatedAt = prev.GetCreatedAt()
		rec.CreatedBy = prev.GetCreatedBy()
	case errors.Is(err, ErrBotNotFound):
		if expectedRevision != 0 {
			return nil, fmt.Errorf("%w: bot %q does not exist", ErrClaimConflict, rec.GetId())
		}
		if rec.GetCreatedAt() == nil {
			rec.CreatedAt = timestamppb.Now()
		}
	default:
		return nil, err
	}

	rec.UpdatedAt = timestamppb.Now()
	rec.Revision = expectedRevision + 1
	data, err := proto.Marshal(&lobslawv1.LogEntry{
		Op:               lobslawv1.LogOp_LOG_OP_CLAIM,
		Id:               rec.GetId(),
		ExpectedRevision: &expectedRevision,
		Payload:          &lobslawv1.LogEntry_Bot{Bot: rec},
	})
	if err != nil {
		return nil, err
	}
	res, err := s.raft.ApplyOrForward(ctx, data, botApplyTimeout)
	if err != nil {
		return nil, fmt.Errorf("bots: raft apply: %w", err)
	}
	if applyErr, ok := res.(error); ok && applyErr != nil {
		return nil, applyErr
	}
	return rec, nil
}

// Delete removes a bot. The chief is refused: it owns the human-facing
// channels, so deleting it would leave an inbound Telegram message
// with nobody to answer it, and the failure would look like an outage
// rather than a consequence.
//
// Records the bot owned — memories, sessions, scheduled routines —
// are NOT cascaded. They are owned by a principal that no longer
// resolves, which is recoverable by recreating the bot under the same
// id; a cascade is not.
func (s *BotService) Delete(ctx context.Context, id string) error {
	if s.raft == nil {
		return errors.New("bots: raft not wired")
	}
	rec, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if rec.GetIsChief() {
		return errors.New("bots: the chief of staff cannot be deleted; it is what answers your messages")
	}
	data, err := proto.Marshal(&lobslawv1.LogEntry{
		Op: lobslawv1.LogOp_LOG_OP_DELETE,
		Id: rec.GetId(),
		// The payload names the bucket the delete routes to; the FSM
		// reads the id, not the body.
		Payload: &lobslawv1.LogEntry_Bot{Bot: &lobslawv1.BotRecord{Id: rec.GetId()}},
	})
	if err != nil {
		return err
	}
	res, err := s.raft.ApplyOrForward(ctx, data, botApplyTimeout)
	if err != nil {
		return fmt.Errorf("bots: raft apply: %w", err)
	}
	if applyErr, ok := res.(error); ok && applyErr != nil {
		return applyErr
	}
	return nil
}

// EnsureChief writes the chief record if the registry is empty.
//
// This is the whole upgrade story. An existing cluster has a soul
// overlay under SoulTuneRecordID and no bots bucket; after this it has
// a chief whose overlay key is that same constant, so the personality
// the deployment already had is the personality the chief has. Nothing
// about an existing turn changes.
//
// Idempotent, and a no-op once any chief exists — it must not
// overwrite instructions an operator has since edited.
func (s *BotService) EnsureChief(ctx context.Context, displayName string) (*lobslawv1.BotRecord, error) {
	existing, err := s.Get(ctx, ChiefBotID)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, ErrBotNotFound) {
		return nil, err
	}
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		displayName = "Chief of Staff"
	}
	return s.Put(ctx, &lobslawv1.BotRecord{
		Id:          ChiefBotID,
		DisplayName: displayName,
		Description: "The agent you talk to. Coordinates the other bots and answers on your channels.",
		IsChief:     true,
		Enabled:     true,
		CreatedBy:   "system",
	}, 0)
}

// checkMessageGraph refuses a may_message edge that would close a
// loop, naming the path.
//
// On WRITE rather than at call time, which is the whole reason
// may_message is declared rather than open. A cycle caught here is one
// message to one person about a decision they just made; the same
// cycle caught at call time is a depth counter, a mid-conversation
// refusal, and a bill for every turn that ran before it tripped.
func (s *BotService) checkMessageGraph(ctx context.Context, rec *lobslawv1.BotRecord) error {
	if len(rec.GetMayMessage()) == 0 {
		return nil
	}
	existing, err := s.List(ctx)
	if err != nil {
		return err
	}
	graph := make(bots.Graph, len(existing)+1)
	for _, b := range existing {
		graph[b.GetId()] = b.GetMayMessage()
	}
	return graph.WithEdges(rec.GetId(), rec.GetMayMessage()).Validate()
}

func validateBot(rec *lobslawv1.BotRecord) error {
	id := strings.TrimSpace(rec.GetId())
	if !botIDPattern.MatchString(id) {
		return fmt.Errorf("bots: id %q must be lowercase letters, digits and hyphens, starting with a letter or digit, at most 63 characters", rec.GetId())
	}
	rec.Id = id
	if n := len(rec.GetInstructions()); n > MaxBotInstructions {
		return fmt.Errorf("bots: instructions are %d bytes, over the %d-byte cap; they ride on every turn this bot takes", n, MaxBotInstructions)
	}
	for _, target := range rec.GetMayMessage() {
		if strings.TrimSpace(target) == id {
			return fmt.Errorf("bots: %q may not be listed as its own message target", id)
		}
		if !botIDPattern.MatchString(strings.TrimSpace(target)) {
			return fmt.Errorf("bots: may_message entry %q is not a valid bot id", target)
		}
	}
	if b := rec.GetBudget(); b != nil {
		if b.GetMaxToolCalls() < 0 || b.GetMaxSpendUsd() < 0 || b.GetMaxEgressBytes() < 0 {
			return fmt.Errorf("bots: %q has a negative budget cap; zero means inherit the node default", id)
		}
	}
	return nil
}
