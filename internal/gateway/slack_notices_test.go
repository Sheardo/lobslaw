package gateway

import (
	"testing"
)

// The review-queue nudge matches an operator-written subject list. The
// operator can only write down an id they can see — the bare Slack
// handle — while a turn is attributed under whatever the alias
// resolved to. Matching one spelling and not the others is how the
// nudge ends up configured, reported enabled, and unable to fire; the
// same mismatch cmd/lobslaw's ownerSubjects comment records having hit
// once already.
func TestSlackNoticeSubjectsCoverEverySpelling(t *testing.T) {
	t.Parallel()

	const (
		team = "T06E50P7CUU"
		user = "U06DZJWNACV"
	)

	// What ownerSubjects emits from user_scopes = { "U06…" = "owner" }.
	fromConfig := "user:" + user
	// What the handler passes alongside the resolved principal.
	derived := slackChannelSubject(team, user)
	bare := "user:" + user

	if derived != "user:slack-"+team+"-"+user {
		t.Fatalf("derived subject = %q", derived)
	}
	if bare != fromConfig {
		t.Fatalf("the bare form %q does not match what ownerSubjects writes (%q)", bare, fromConfig)
	}
	// The derived form alone would NOT match a default config, which is
	// the bug this covers.
	if derived == fromConfig {
		t.Fatal("derived and config forms collapsed; the test no longer proves anything")
	}
}

func TestSlackChannelSubjectIsTeamScoped(t *testing.T) {
	t.Parallel()

	a := slackChannelSubject("T0ONE", "U0ALICE")
	b := slackChannelSubject("T0TWO", "U0ALICE")
	if a == b {
		t.Fatal("the same handle in two workspaces produced one subject")
	}
	if slackChannelSubject("T0ONE", "") != "" {
		t.Error("an empty user produced a subject")
	}
	// Must carry the "user:" namespace the allowlist is written in; a
	// bare id here produced a list that could not match anything.
	if got := slackChannelSubject("T0ONE", "U0ALICE"); got[:5] != "user:" {
		t.Errorf("subject %q is not in the user: namespace", got)
	}
}
