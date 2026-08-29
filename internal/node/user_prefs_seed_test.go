package node

import (
	"testing"

	"github.com/jmylchreest/lobslaw/pkg/config"
	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
)

// A channel bound in config AFTER first boot used to be ignored
// forever: the seeder skipped any user whose record already existed,
// justified by "runtime edits via builtins win" — and no such builtin
// exists, so operator config was the only source and it was being
// dropped on every boot but the first.
//
// The symptom looked like something else entirely. A reminder scheduled
// from Slack fired, found no slack address in prefs, fell back to the
// originating conversation id, and posted into channel_not_found —
// while the operator watched a correct-looking [[user.channels]] block
// do nothing across restarts.
func TestMergeMissingChannelsAddsNewlyConfiguredTypes(t *testing.T) {
	t.Parallel()

	rec := &lobslawv1.UserPreferences{
		UserId: "james",
		Channels: []*lobslawv1.UserChannelAddress{
			{Type: "telegram", Address: "5053517285"},
		},
	}
	added := mergeMissingChannels(rec, []config.UserChannelAddrConfig{
		{Type: "telegram", Address: "9999999999"},
		{Type: "slack", Address: "U06DZJWNACV"},
	})

	if len(added) != 1 || added[0] != "slack" {
		t.Fatalf("added = %v; want just slack", added)
	}
	byType := map[string]string{}
	for _, c := range rec.Channels {
		byType[c.GetType()] = c.GetAddress()
	}
	// The existing address is left alone: it may have been corrected at
	// runtime, and config is not entitled to overwrite it.
	if byType["telegram"] != "5053517285" {
		t.Errorf("telegram address was overwritten: %q", byType["telegram"])
	}
	if byType["slack"] != "U06DZJWNACV" {
		t.Errorf("slack address = %q", byType["slack"])
	}
}

func TestMergeMissingChannelsIsANoOpWhenNothingIsNew(t *testing.T) {
	t.Parallel()

	rec := &lobslawv1.UserPreferences{
		Channels: []*lobslawv1.UserChannelAddress{{Type: "slack", Address: "U1"}},
	}
	added := mergeMissingChannels(rec, []config.UserChannelAddrConfig{
		{Type: "slack", Address: "U2"},
		// A type with no address is not a binding, and appending it
		// would shadow a real one that arrives later.
		{Type: "telegram", Address: ""},
	})
	if len(added) != 0 {
		t.Errorf("added = %v; want none", added)
	}
	if len(rec.Channels) != 1 {
		t.Errorf("channels = %d; want 1", len(rec.Channels))
	}
}

func TestMergeMissingChannelsHandlesNilRecord(t *testing.T) {
	t.Parallel()
	if added := mergeMissingChannels(nil, []config.UserChannelAddrConfig{{Type: "slack", Address: "U1"}}); added != nil {
		t.Errorf("added = %v; want nil", added)
	}
}

// A config entry reading type = "Slack" must bind something notify can
// actually find: findChannelAddress compares against the lowercase
// channel constants exactly, so the stored type has to be folded.
func TestMergeMissingChannelsNormalisesTypeCase(t *testing.T) {
	t.Parallel()

	rec := &lobslawv1.UserPreferences{}
	added := mergeMissingChannels(rec, []config.UserChannelAddrConfig{
		{Type: "  Slack  ", Address: "U1"},
	})
	if len(added) != 1 || added[0] != "slack" {
		t.Fatalf("added = %v; want [slack]", added)
	}
	if got := rec.Channels[0].GetType(); got != "slack" {
		t.Errorf("stored type = %q; notify would never match it", got)
	}
}

// And the same folding stops a differently-cased duplicate shadowing a
// binding that already works.
func TestMergeMissingChannelsDoesNotDuplicateOnCase(t *testing.T) {
	t.Parallel()

	rec := &lobslawv1.UserPreferences{
		Channels: []*lobslawv1.UserChannelAddress{{Type: "slack", Address: "U1"}},
	}
	if added := mergeMissingChannels(rec, []config.UserChannelAddrConfig{
		{Type: "SLACK", Address: "U2"},
	}); len(added) != 0 {
		t.Errorf("added = %v; want none", added)
	}
	if len(rec.Channels) != 1 {
		t.Errorf("channels = %d; want 1", len(rec.Channels))
	}
}
