package skills

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func generateKeypair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

// writeSignedSkill creates a manifest + handler + detached ed25519
// signature in dir. Returns the manifest path for convenience.
//
// The manifest pins the handler digest, because a signed manifest
// without one is now refused — the signature would cover no
// executable content.
func writeSignedSkill(t *testing.T, dir string, priv ed25519.PrivateKey) string {
	t.Helper()
	writeHandler(t, dir, "h.sh", "echo")
	return writeSignedManifest(t, dir, priv, "signed-skill")
}

// writeSignedManifest signs a manifest for a handler that already
// exists in dir, digesting it as a publisher would.
func writeSignedManifest(t *testing.T, dir string, priv ed25519.PrivateKey, name string) string {
	t.Helper()
	sum, err := fileDigest(filepath.Join(dir, "h.sh"))
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Appendf(nil, `name: %s
version: 1.0.0
runtime: bash
handler: h.sh
handler_sha256: %s
`, name, sum)
	manifestPath := filepath.Join(dir, "manifest.yaml")
	if err := os.WriteFile(manifestPath, body, 0o644); err != nil {
		t.Fatal(err)
	}
	sig := ed25519.Sign(priv, body)
	if err := os.WriteFile(manifestPath+".sig", sig, 0o644); err != nil {
		t.Fatal(err)
	}
	return manifestPath
}

// --- SigningPolicy parsing + validation ---------------------------------

func TestParseSigningPolicy(t *testing.T) {
	t.Parallel()
	cases := map[string]SigningPolicy{
		"off":     SigningOff,
		"OFF":     SigningOff,
		"prefer":  SigningPrefer,
		"require": SigningRequire,
		"":        SigningPrefer, // default
		"garbage": SigningPrefer, // unrecognised → safe default
	}
	for in, want := range cases {
		if got := ParseSigningPolicy(in); got != want {
			t.Errorf("ParseSigningPolicy(%q) = %q want %q", in, got, want)
		}
	}
}

func TestSigningPolicyIsValid(t *testing.T) {
	t.Parallel()
	for _, p := range []SigningPolicy{SigningOff, SigningPrefer, SigningRequire} {
		if !p.IsValid() {
			t.Errorf("%q should be valid", p)
		}
	}
	for _, p := range []SigningPolicy{"", "yes", "true", "REQUIRE_YES"} {
		if p.IsValid() {
			t.Errorf("%q should not be valid", p)
		}
	}
}

// --- Verifier -----------------------------------------------------------

func TestVerifierAddKeyRejectsWrongSize(t *testing.T) {
	t.Parallel()
	v := NewVerifier()
	if err := v.AddKey("bogus", []byte("too-short")); err == nil {
		t.Error("short key should be rejected")
	}
}

func TestVerifierAddKeyRejectsEmptyName(t *testing.T) {
	t.Parallel()
	v := NewVerifier()
	pub, _ := generateKeypair(t)
	if err := v.AddKey("", pub); err == nil {
		t.Error("empty name should be rejected")
	}
}

func TestVerifierVerifyHappyPath(t *testing.T) {
	t.Parallel()
	pub, priv := generateKeypair(t)
	v := NewVerifier()
	_ = v.AddKey("alice", pub)

	data := []byte("hello skill")
	sig := ed25519.Sign(priv, data)
	signer, ok := v.Verify(data, sig)
	if !ok {
		t.Fatal("valid signature should verify")
	}
	if signer != "alice" {
		t.Errorf("signer name: %q", signer)
	}
}

func TestVerifierVerifyMultipleKeys(t *testing.T) {
	t.Parallel()
	_, alicePriv := generateKeypair(t)
	bobPub, bobPriv := generateKeypair(t)
	carolPub, _ := generateKeypair(t)

	v := NewVerifier()
	_ = v.AddKey("bob", bobPub)
	_ = v.AddKey("carol", carolPub)

	data := []byte("from bob")
	aliceSig := ed25519.Sign(alicePriv, data) // alice not registered
	if _, ok := v.Verify(data, aliceSig); ok {
		t.Error("alice's signature should not verify against bob+carol")
	}

	bobSig := ed25519.Sign(bobPriv, data)
	signer, ok := v.Verify(data, bobSig)
	if !ok || signer != "bob" {
		t.Errorf("bob's signature should verify as bob; got %q ok=%v", signer, ok)
	}
}

func TestVerifierVerifyRejectsWrongSizeSignature(t *testing.T) {
	t.Parallel()
	pub, _ := generateKeypair(t)
	v := NewVerifier()
	_ = v.AddKey("k", pub)
	_, ok := v.Verify([]byte("x"), []byte("not-64-bytes"))
	if ok {
		t.Error("wrong-sized sig should not verify")
	}
}

func TestLoadTrustedPublishersFile(t *testing.T) {
	t.Parallel()
	pub, _ := generateKeypair(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "publishers")
	content := strings.Join([]string{
		"# one key per line",
		"",
		"alice " + base64.StdEncoding.EncodeToString(pub),
	}, "\n")
	_ = os.WriteFile(path, []byte(content), 0o644)

	v := NewVerifier()
	if err := v.LoadTrustedPublishersFile(path); err != nil {
		t.Fatal(err)
	}
	if v.Count() != 1 {
		t.Errorf("expected 1 key loaded; got %d", v.Count())
	}
}

func TestLoadTrustedPublishersFileRejectsMalformed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "pub")
	_ = os.WriteFile(path, []byte("alice no-space-key-on-one-token"), 0o644)
	v := NewVerifier()
	if err := v.LoadTrustedPublishersFile(path); err == nil {
		t.Error("malformed line should error")
	}
}

// --- ParseWithPolicy behaviour ------------------------------------------

func TestParseWithPolicyOffIgnoresSignature(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, priv := generateKeypair(t)
	_ = writeSignedSkill(t, dir, priv)

	// Verifier is nil under SigningOff — allowed.
	s, err := ParseWithPolicy(dir, SigningOff, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.IsSigned {
		t.Error("SigningOff must report IsSigned=false regardless of signature presence")
	}
}

func TestParseWithPolicyPreferAcceptsUnsigned(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeHandler(t, dir, "h.sh", "echo")
	writeManifest(t, dir, `
name: plain
version: 1.0.0
runtime: bash
handler: h.sh
`)
	s, err := ParseWithPolicy(dir, SigningPrefer, NewVerifier())
	if err != nil {
		t.Fatal(err)
	}
	if s.IsSigned {
		t.Error("unsigned manifest should have IsSigned=false")
	}
}

func TestParseWithPolicyPreferAcceptsValidSignature(t *testing.T) {
	t.Parallel()
	pub, priv := generateKeypair(t)
	dir := t.TempDir()
	_ = writeSignedSkill(t, dir, priv)

	v := NewVerifier()
	_ = v.AddKey("publisher-1", pub)

	s, err := ParseWithPolicy(dir, SigningPrefer, v)
	if err != nil {
		t.Fatal(err)
	}
	if !s.IsSigned {
		t.Error("valid signature should set IsSigned=true")
	}
	if s.SignedBy != "publisher-1" {
		t.Errorf("SignedBy: %q", s.SignedBy)
	}
}

func TestParseWithPolicyPreferRejectsInvalidSignature(t *testing.T) {
	t.Parallel()
	// Publisher key WRONG — sig is from priv but verifier doesn't
	// know that key.
	_, priv := generateKeypair(t)
	otherPub, _ := generateKeypair(t)

	dir := t.TempDir()
	_ = writeSignedSkill(t, dir, priv)

	v := NewVerifier()
	_ = v.AddKey("wrong-one", otherPub)

	_, err := ParseWithPolicy(dir, SigningPrefer, v)
	if err == nil {
		t.Fatal("sig present but not-verifying must reject even under Prefer")
	}
	if !strings.Contains(err.Error(), "did not verify") {
		t.Errorf("error should mention verification; got %v", err)
	}
}

func TestParseWithPolicyRequireRejectsUnsigned(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeHandler(t, dir, "h.sh", "echo")
	writeManifest(t, dir, `
name: plain
version: 1.0.0
runtime: bash
handler: h.sh
`)
	_, err := ParseWithPolicy(dir, SigningRequire, NewVerifier())
	if err == nil {
		t.Fatal("SigningRequire must reject unsigned manifest")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("error should mention 'required'; got %v", err)
	}
}

func TestParseWithPolicyRequireAcceptsValidSignature(t *testing.T) {
	t.Parallel()
	pub, priv := generateKeypair(t)
	dir := t.TempDir()
	_ = writeSignedSkill(t, dir, priv)

	v := NewVerifier()
	_ = v.AddKey("p", pub)

	s, err := ParseWithPolicy(dir, SigningRequire, v)
	if err != nil {
		t.Fatal(err)
	}
	if !s.IsSigned {
		t.Error("Require-path valid-sig should set IsSigned=true")
	}
}

func TestParseWithPolicyRequireWithNilVerifierErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeHandler(t, dir, "h.sh", "echo")
	writeManifest(t, dir, `
name: plain
version: 1.0.0
runtime: bash
handler: h.sh
`)
	_, err := ParseWithPolicy(dir, SigningRequire, nil)
	if err == nil {
		t.Error("SigningRequire with nil verifier should error at construction-time")
	}
}

// --- Registry preference -------------------------------------------------

func TestRegistryPreferSignedBreaksTies(t *testing.T) {
	t.Parallel()
	r := NewRegistryWithPolicy(nil, SigningPrefer)

	unsigned := &Skill{
		Manifest:    Manifest{Name: "s", Version: "1.0.0"},
		ManifestDir: "/mnt/a",
	}
	signed := &Skill{
		Manifest:    Manifest{Name: "s", Version: "1.0.0"},
		ManifestDir: "/mnt/z", // lexicographically LATER
		IsSigned:    true,
		SignedBy:    "publisher-1",
	}

	r.Put(unsigned)
	r.Put(signed)

	got, _ := r.Get("s")
	if !got.IsSigned {
		t.Errorf("PreferSigned should pick the signed candidate; got %+v", got)
	}
}

// Under SigningOff nothing is ever verified, so every candidate is
// operator tier and the tie falls through to the directory exactly as
// it always did.
//
// This test used to set IsSigned on one candidate while the policy was
// Off — a state ParseWithPolicy cannot produce, since Off returns
// before verification. Asserting against an impossible input made it
// look as though tier-first had changed SigningOff behaviour, when the
// only thing that changed was the test's fiction.
func TestSigningOffStillFallsBackToLexicographic(t *testing.T) {
	t.Parallel()
	r := NewRegistryWithPolicy(nil, SigningOff)

	a := &Skill{Manifest: Manifest{Name: "s", Version: "1.0.0"}, ManifestDir: "/mnt/a"}
	z := &Skill{Manifest: Manifest{Name: "s", Version: "1.0.0"}, ManifestDir: "/mnt/z"}

	r.Put(a)
	r.Put(z)

	got, _ := r.Get("s")
	if got.ManifestDir != "/mnt/a" {
		t.Errorf("SigningOff should stick to lexicographic tiebreak; got %q", got.ManifestDir)
	}
}

// BEHAVIOUR CHANGE, and the reason for the whole tier-first rule.
//
// This test used to assert the opposite — "Signing is only a
// TIE-breaker, not an override", with a higher-version unsigned
// candidate winning. That was defensible while nothing but an operator
// could write a skill. It became a privilege-escalation path the
// moment the agent could author one: name your skill after a signed
// one, set version 99.0.0, and take the name.
//
// A version bump can no longer promote a skill past its provenance.
func TestAHigherVersionCannotBeatABetterTier(t *testing.T) {
	t.Parallel()
	r := NewRegistryWithPolicy(nil, SigningPrefer)
	signedOld := &Skill{
		Manifest:    Manifest{Name: "s", Version: "1.0.0"},
		ManifestDir: "/mnt/a", IsSigned: true,
	}
	unsignedNew := &Skill{
		Manifest:    Manifest{Name: "s", Version: "99.0.0"},
		ManifestDir: "/mnt/b",
	}
	r.Put(signedOld)
	r.Put(unsignedNew)

	got, _ := r.Get("s")
	if !got.IsSigned {
		t.Errorf("an unsigned v99 took the name from a signed v1: %+v", got)
	}

	// And the same for the tier that actually matters now: an
	// agent-authored skill must not take a name from an operator's,
	// however high it numbers itself.
	r2 := NewRegistryWithPolicy(nil, SigningOff)
	operator := &Skill{
		Manifest:    Manifest{Name: "deploy", Version: "1.0.0"},
		ManifestDir: "/mnt/skills", Tier: TierOperator,
	}
	agent := &Skill{
		Manifest:    Manifest{Name: "deploy", Version: "99.0.0"},
		ManifestDir: "/cache/self-taught", Tier: TierAgent,
	}
	r2.Put(operator)
	r2.Put(agent)

	won, _ := r2.Get("deploy")
	if won.Tier != TierOperator {
		t.Errorf("an agent-authored v99 took an operator's name: %+v", won)
	}
}

// Within a tier, the version still decides — tier-first must not have
// frozen the library at whatever was installed first.
func TestVersionStillWinsWithinATier(t *testing.T) {
	t.Parallel()
	r := NewRegistryWithPolicy(nil, SigningPrefer)
	r.Put(&Skill{Manifest: Manifest{Name: "s", Version: "1.0.0"}, ManifestDir: "/mnt/a", IsSigned: true})
	r.Put(&Skill{Manifest: Manifest{Name: "s", Version: "2.0.0"}, ManifestDir: "/mnt/b", IsSigned: true})

	got, _ := r.Get("s")
	if got.Manifest.Version != "2.0.0" {
		t.Errorf("a newer signed version did not win within its tier: %+v", got)
	}
}

// A skill built as a struct literal must derive its tier rather than
// defaulting to the lowest one. The zero value is "underived" for
// exactly this reason — making TierAgent the zero value looked tidier
// and silently demoted every hand-constructed skill.
func TestUnsetTierDerivesRatherThanDemoting(t *testing.T) {
	t.Parallel()
	if got := tierOf(&Skill{IsSigned: true}); got != TierSigned {
		t.Errorf("an unset tier on a signed skill derived %v, want signed", got)
	}
	if got := tierOf(&Skill{}); got != TierOperator {
		t.Errorf("an unset tier derived %v, want operator", got)
	}
	if got := tierOf(&Skill{Tier: TierAgent}); got != TierAgent {
		t.Errorf("an explicit agent tier was overridden: %v", got)
	}
}

// --- Compile-time check -------------------------------------------------

var _ = errors.New // reserve in case future edits trim other imports
