package memory

import (
	"testing"

	"github.com/jmylchreest/lobslaw/internal/identity"
	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
)

// The disclosure this whole dimension exists to prevent: alice tells the
// agent something in a DM, bob speaks in a shared channel, and the
// agent recalls alice's memory to an audience that never owned it.
func TestConversationAudienceDoesNotLeakAnothersDM(t *testing.T) {
	t.Parallel()

	alice := identity.User("alice")
	bob := identity.User("bob")

	aliceDM := &lobslawv1.EpisodicRecord{
		Owner:      alice.String(),
		Visibility: lobslawv1.Visibility_VISIBILITY_PRIVATE,
		SessionRef: "slack:D0ALICE",
	}

	// Bob speaking in #general.
	bobInChannel := ForConversation(bob, "slack:C0GENERAL")
	if bobInChannel.AllowsEpisodic(aliceDM) {
		t.Fatal("alice's DM memory surfaced in a shared channel because bob spoke")
	}

	// Alice speaking in the same channel still reaches her own memory:
	// the rule scopes by conversation, it does not disown people.
	aliceInChannel := ForConversation(alice, "slack:C0GENERAL")
	if !aliceInChannel.AllowsEpisodic(aliceDM) {
		t.Fatal("alice cannot see her own memory when she is the speaker")
	}
}

// The other half: what the conversation itself produced is readable by
// whoever is in it, or the agent is useless in a team channel.
func TestConversationAudienceAllowsRecordsFromThatConversation(t *testing.T) {
	t.Parallel()

	alice := identity.User("alice")
	bob := identity.User("bob")

	fromChannel := &lobslawv1.EpisodicRecord{
		Owner:      alice.String(),
		Visibility: lobslawv1.Visibility_VISIBILITY_PRIVATE,
		SessionRef: "slack:C0GENERAL",
	}

	if !ForConversation(bob, "slack:C0GENERAL").AllowsEpisodic(fromChannel) {
		t.Fatal("a record this conversation produced was hidden from someone in it")
	}
	// A different channel must not reach it, or the scope is no scope.
	if ForConversation(bob, "slack:C0RANDOM").AllowsEpisodic(fromChannel) {
		t.Fatal("a record leaked across two different conversations")
	}
}

// A DM keeps the old behaviour exactly: For() names no conversation, so
// nothing widens.
func TestPlainAudienceUnaffectedByConversationOrigin(t *testing.T) {
	t.Parallel()

	alice := identity.User("alice")
	bob := identity.User("bob")

	rec := &lobslawv1.EpisodicRecord{
		Owner:      alice.String(),
		Visibility: lobslawv1.Visibility_VISIBILITY_PRIVATE,
		SessionRef: "slack:C0GENERAL",
	}
	if For(bob).AllowsEpisodic(rec) {
		t.Fatal("a plain audience read another principal's record")
	}
	if !For(alice).AllowsEpisodic(rec) {
		t.Fatal("a plain audience lost its owner's own record")
	}
}

// A record with no conversation origin — a scheduled task, a commitment
// fire — must not be swept in by a conversation scope. Empty matching
// empty is the classic accident and the reason ForConversation refuses
// to widen on an empty conversation.
func TestConversationScopeIgnoresOriginlessRecords(t *testing.T) {
	t.Parallel()

	alice := identity.User("alice")
	bob := identity.User("bob")

	scheduled := &lobslawv1.EpisodicRecord{
		Owner:      alice.String(),
		Visibility: lobslawv1.Visibility_VISIBILITY_PRIVATE,
		SessionRef: "",
	}
	if ForConversation(bob, "").AllowsEpisodic(scheduled) {
		t.Fatal("an empty conversation matched an originless record")
	}
	if ForConversation(bob, "slack:C0GENERAL").AllowsEpisodic(scheduled) {
		t.Fatal("a conversation scope reached a record with no origin")
	}
}

// Vector records carry the same origin as the episodic record they
// embed. Search reads vectors, so a vector without it is the leak
// wearing a different hat — the same argument the ingest path already
// makes for Owner.
func TestConversationAudienceAppliesToVectors(t *testing.T) {
	t.Parallel()

	alice := identity.User("alice")
	bob := identity.User("bob")

	aliceDMVec := &lobslawv1.VectorRecord{
		Owner:      alice.String(),
		Visibility: lobslawv1.Visibility_VISIBILITY_PRIVATE,
		SessionRef: "slack:D0ALICE",
	}
	if ForConversation(bob, "slack:C0GENERAL").AllowsVector(aliceDMVec) {
		t.Fatal("alice's DM vector was readable from a shared channel")
	}

	channelVec := &lobslawv1.VectorRecord{
		Owner:      alice.String(),
		Visibility: lobslawv1.Visibility_VISIBILITY_PRIVATE,
		SessionRef: "slack:C0GENERAL",
	}
	if !ForConversation(bob, "slack:C0GENERAL").AllowsVector(channelVec) {
		t.Fatal("a vector from this conversation was hidden from someone in it")
	}
}

// Shared records stay shared, and the zero Audience still matches
// nothing — the two invariants the rest of the file is built on.
func TestConversationScopeKeepsExistingInvariants(t *testing.T) {
	t.Parallel()

	bob := identity.User("bob")

	shared := &lobslawv1.EpisodicRecord{
		Owner:      "user:alice",
		Visibility: lobslawv1.Visibility_VISIBILITY_SHARED,
		SessionRef: "slack:D0ALICE",
	}
	if !ForConversation(bob, "slack:C0GENERAL").AllowsEpisodic(shared) {
		t.Error("a SHARED record stopped being shared under a conversation scope")
	}

	var zero Audience
	if zero.AllowsEpisodic(shared) {
		t.Error("the zero Audience matched a record")
	}
	if !zero.IsZero() {
		t.Error("the zero Audience did not report itself as zero")
	}
}
