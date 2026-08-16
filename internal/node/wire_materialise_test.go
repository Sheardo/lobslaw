package node

import (
	"testing"

	"github.com/jmylchreest/lobslaw/internal/skills"
	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
)

// The translation from store record to materialiser input. Small, and
// exactly the place a schema change would break quietly: a field
// dropped here means a skill that materialises with no description, or
// with no bundled files, and nothing fails.

func TestArtefactsCarryEverythingTheSkillNeeds(t *testing.T) {
	t.Parallel()
	got := artefactsFor([]*lobslawv1.SelfTaughtRecord{{
		Id:          "skill:tidy",
		Name:        "tidy",
		Description: "how this user likes things tidied",
		Body:        "the procedure",
		Version:     7,
		Files:       map[string]string{"references/api.md": "content"},
	}})
	if len(got) != 1 {
		t.Fatalf("got %d artefacts", len(got))
	}
	want := skills.Artefact{
		Name:        "tidy",
		Description: "how this user likes things tidied",
		Body:        "the procedure",
		Version:     7,
		Files:       map[string]string{"references/api.md": "content"},
	}
	if got[0].Name != want.Name || got[0].Description != want.Description ||
		got[0].Body != want.Body || got[0].Version != want.Version {
		t.Errorf("artefact = %+v, want %+v", got[0], want)
	}
	if got[0].Files["references/api.md"] != "content" {
		t.Errorf("files = %v", got[0].Files)
	}
}

// A refinement awaiting approval is a proposal. Materialising it would
// put it in the prompt, which is precisely what proposing instead of
// applying exists to prevent.
func TestAPendingRefinementIsNotMaterialised(t *testing.T) {
	t.Parallel()
	got := artefactsFor([]*lobslawv1.SelfTaughtRecord{{
		Name:    "tidy",
		Body:    "the approved procedure",
		Version: 1,
		Pending: &lobslawv1.PendingRevision{
			Body:        "a suggestion nobody has accepted",
			Description: "the suggested description",
		},
	}})
	if got[0].Body != "the approved procedure" {
		t.Errorf("body = %q; a pending refinement reached the cache", got[0].Body)
	}
}
