package gateway

import "testing"

// Two bots must not arrive looking like the same person.
func TestBotIdentityIsDistinctAndStable(t *testing.T) {
	t.Parallel()

	devops := botIdentity("devops")
	eng := botIdentity("engineering")

	if devops.Username != "Devops" {
		t.Errorf("Username = %q, want a name rather than a slug", devops.Username)
	}
	if devops.IconEmoji == "" {
		t.Error("no icon, so every bot renders with the app's avatar")
	}
	if devops.IconEmoji == eng.IconEmoji && devops.Username == eng.Username {
		t.Error("two bots are visually identical in a channel")
	}
	// Stable: a bot whose avatar changes between messages reads as two
	// different senders.
	if botIdentity("devops") != devops {
		t.Error("identity is not stable across calls")
	}
	// A message from nobody in particular posts as the app itself.
	if (botIdentity("") != slackIdentity{}) {
		t.Error("an unattributed message claimed an identity")
	}
}
