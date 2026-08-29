package promptgen

import (
	"strings"
	"testing"
)

// The same bug as self_learning, and the same fix. A node with a vault
// wired up, asked where to keep an API key, otherwise suggests an
// environment variable — or invites the user to paste the key into the
// chat, which puts it in the transcript, the session store, and the
// next turn's replay to a third-party model.
func TestRuntimeNamesConfiguredVaults(t *testing.T) {
	t.Parallel()

	body := BuildRuntime(RuntimeInfo{
		Hostname:        "nova",
		SecretProviders: []string{"bw", "vault"},
	}).Body

	if !strings.Contains(body, "bw, vault") {
		t.Errorf("configured vaults should be named:\n%s", body)
	}
	// The reference SYNTAX matters as much as the existence: knowing a
	// vault is there without knowing how to point config at it leaves
	// the model guessing at the one thing it needs to say.
	if !strings.Contains(body, "<vault>:<path>") {
		t.Errorf("reference syntax should be shown:\n%s", body)
	}
	if !strings.Contains(body, "bw:stripe/live-key") {
		t.Errorf("a worked example should use a real configured label:\n%s", body)
	}
	// The action has to come FIRST. An earlier version led with the
	// prohibitions and the model reproduced exactly those — answering
	// "no, I can't read your vault", correctly and uselessly, without
	// mentioning that it knew where the secret should go.
	action := strings.Index(body, "tell them to put it")
	prohibition := strings.Index(body, "Never ask for the secret")
	if action < 0 || prohibition < 0 {
		t.Fatalf("both halves should be present:\n%s", body)
	}
	if action > prohibition {
		t.Error("the prohibition leads; a prompt that is mostly what NOT to do gets obeyed as what to say")
	}
	if !strings.Contains(body, "offer it without being asked") {
		t.Errorf("must instruct it to volunteer the vault, not wait to be asked:\n%s", body)
	}
	// And the honesty about what it cannot do, so it does not offer.
	if !strings.Contains(body, "cannot read vault values") {
		t.Errorf("should say it cannot read values:\n%s", body)
	}
}

// A node with no vault says nothing about one, rather than describing a
// capability it does not have — which is the failure this whole line of
// work has been correcting all week.
func TestRuntimeSaysNothingWithoutVaults(t *testing.T) {
	t.Parallel()

	body := BuildRuntime(RuntimeInfo{Hostname: "nova"}).Body
	if strings.Contains(body, "secret_vaults") {
		t.Errorf("no vault configured, so none should be described:\n%s", body)
	}
}
