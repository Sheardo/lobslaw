package types

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The tier became a number because three was never the real shape. A
// model on a VPS the operator rents is not `local` — the hardware is
// somebody else's — and it is plainly better than a public API with no
// contract. Under a closed enum that provider had to be filed under a
// tier that misdescribed it, and the floor then admitted or excluded
// it for the wrong reason.

func TestHigherIsMoreTrusted(t *testing.T) {
	t.Parallel()
	if !(TrustLocal > TrustPrivate && TrustPrivate > TrustPublic) {
		t.Fatalf("ordering is wrong: local=%d private=%d public=%d",
			TrustLocal, TrustPrivate, TrustPublic)
	}
	// The direction the whole guard depends on. Inverting it would
	// make a floor admit exactly what it exists to exclude.
	if !TrustLocal.AtLeast(TrustPrivate) {
		t.Error("local does not satisfy a private floor")
	}
	if TrustPublic.AtLeast(TrustPrivate) {
		t.Error("public satisfies a private floor")
	}
}

// Zero is reserved. An omitted field decodes to Go's zero, so if the
// zero value were a real tier every provider that forgot to declare
// one would silently acquire it — the bug SkillTier hit when TierAgent
// was the zero value.
func TestZeroIsNotATier(t *testing.T) {
	t.Parallel()
	if TrustUnset.IsValid() {
		t.Error("the zero value reports itself as a usable tier")
	}
	if TrustPublic == TrustUnset {
		t.Error("public is the zero value; an undeclared provider would become public")
	}
	// It fails any real floor, which is the safe direction.
	if TrustUnset.AtLeast(TrustPublic) {
		t.Error("an undeclared tier satisfied the weakest floor")
	}
}

func TestOutOfRangeIsInvalid(t *testing.T) {
	t.Parallel()
	for _, tier := range []TrustTier{-1, 0, MaxTrustTier + 1, 9999} {
		if tier.IsValid() {
			t.Errorf("TrustTier(%d) reports valid", int(tier))
		}
	}
	for _, tier := range []TrustTier{TrustPublic, 25, TrustPrivate, 73, TrustLocal} {
		if !tier.IsValid() {
			t.Errorf("TrustTier(%d) reports invalid", int(tier))
		}
	}
}

// --- parsing -------------------------------------------------------

func TestNamesAndNumbersBothParse(t *testing.T) {
	t.Parallel()
	cases := map[string]TrustTier{
		"public": TrustPublic, "private": TrustPrivate, "local": TrustLocal,
		"PRIVATE": TrustPrivate, "  local  ": TrustLocal,
		"1": TrustPublic, "50": TrustPrivate, "100": TrustLocal,
		// The case the scale exists for: a rented VPS is better than a
		// public API and is not local.
		"60": TrustTier(60),
		"":   TrustUnset,
	}
	for in, want := range cases {
		got, err := ParseTrustTier(in)
		if err != nil {
			t.Errorf("ParseTrustTier(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseTrustTier(%q) = %d, want %d", in, int(got), int(want))
		}
	}
}

// An unrecognised NAME is an error rather than a new tier. Operator-
// defined tier names would turn a typo into a silent extra tier, which
// is the one failure mode this scale must not have.
func TestAnUnknownNameIsAnError(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"privat", "secure", "trusted", "localhost"} {
		if _, err := ParseTrustTier(in); err == nil {
			t.Errorf("%q parsed as a tier", in)
		}
	}
}

func TestOutOfRangeNumbersAreRefusedAtParse(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"0", "-1", "101", "500"} {
		_, err := ParseTrustTier(in)
		if err == nil {
			t.Errorf("%q parsed", in)
			continue
		}
		if !strings.Contains(err.Error(), "higher is more trusted") {
			t.Errorf("%q: err = %q; it does not say which direction the scale runs", in, err)
		}
	}
}

// --- rendering -----------------------------------------------------

// Named where a name exists, because that is what the operator wrote
// and what they will search their config for. A bare 50 in a log line
// sends somebody to a scale definition; "private" does not.
func TestRenderingPrefersTheName(t *testing.T) {
	t.Parallel()
	cases := map[TrustTier]string{
		TrustPublic: "public", TrustPrivate: "private", TrustLocal: "local",
		TrustTier(60): "60", TrustUnset: "unset",
	}
	for in, want := range cases {
		if got := in.String(); got != want {
			t.Errorf("TrustTier(%d).String() = %q, want %q", int(in), got, want)
		}
	}
}

// --- decoding, per format ------------------------------------------

// yaml.v3 does not consult encoding.TextUnmarshaler. A type that
// decodes from TOML and silently zeroes from YAML would leave a soul
// floor unset while the config looked right.
func TestYAMLTakesBothForms(t *testing.T) {
	t.Parallel()
	var byName struct {
		Tier TrustTier `yaml:"tier"`
	}
	if err := yaml.Unmarshal([]byte("tier: private\n"), &byName); err != nil {
		t.Fatal(err)
	}
	if byName.Tier != TrustPrivate {
		t.Errorf("named YAML = %d", int(byName.Tier))
	}

	var byNumber struct {
		Tier TrustTier `yaml:"tier"`
	}
	if err := yaml.Unmarshal([]byte("tier: 60\n"), &byNumber); err != nil {
		t.Fatal(err)
	}
	if byNumber.Tier != TrustTier(60) {
		t.Errorf("numeric YAML = %d", int(byNumber.Tier))
	}
}

func TestYAMLRefusesGarbage(t *testing.T) {
	t.Parallel()
	var out struct {
		Tier TrustTier `yaml:"tier"`
	}
	if err := yaml.Unmarshal([]byte("tier: privat\n"), &out); err == nil {
		t.Error("a typo'd tier decoded from YAML")
	}
	if err := yaml.Unmarshal([]byte("tier: 500\n"), &out); err == nil {
		t.Error("an out-of-range tier decoded from YAML")
	}
}

// encoding/json consults TextUnmarshaler for map KEYS only, never for
// values, so JSON needs its own pair.
func TestJSONTakesBothFormsAndRoundTrips(t *testing.T) {
	t.Parallel()
	var byName struct {
		Tier TrustTier `json:"tier"`
	}
	if err := json.Unmarshal([]byte(`{"tier":"private"}`), &byName); err != nil {
		t.Fatal(err)
	}
	if byName.Tier != TrustPrivate {
		t.Errorf("named JSON = %d", int(byName.Tier))
	}

	var byNumber struct {
		Tier TrustTier `json:"tier"`
	}
	if err := json.Unmarshal([]byte(`{"tier":60}`), &byNumber); err != nil {
		t.Fatal(err)
	}
	if byNumber.Tier != TrustTier(60) {
		t.Errorf("numeric JSON = %d", int(byNumber.Tier))
	}

	raw, err := json.Marshal(byNumber)
	if err != nil {
		t.Fatal(err)
	}
	var back struct {
		Tier TrustTier `json:"tier"`
	}
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("marshalled form does not decode: %s: %v", raw, err)
	}
	if back.Tier != TrustTier(60) {
		t.Errorf("round trip lost the value: %s", raw)
	}
}

func TestTextRoundTripsBothForms(t *testing.T) {
	t.Parallel()
	for _, tier := range []TrustTier{TrustPublic, TrustPrivate, TrustLocal, TrustTier(73)} {
		raw, err := tier.MarshalText()
		if err != nil {
			t.Fatal(err)
		}
		var back TrustTier
		if err := back.UnmarshalText(raw); err != nil {
			t.Fatalf("%q: %v", raw, err)
		}
		if back != tier {
			t.Errorf("round trip: %d -> %q -> %d", int(tier), raw, int(back))
		}
	}
}

// The reserved points must never move. A deployment that pinned a
// provider at "private" and a floor at 50 has to keep meaning the same
// thing after any future change, so these can be added to but never
// renumbered.
func TestTheReservedPointsAreWhereTheyWere(t *testing.T) {
	t.Parallel()
	if TrustPublic != 1 || TrustPrivate != 50 || TrustLocal != 100 {
		t.Errorf("a reserved tier moved: public=%d private=%d local=%d",
			int(TrustPublic), int(TrustPrivate), int(TrustLocal))
	}
}
