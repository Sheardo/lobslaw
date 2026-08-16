package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmylchreest/lobslaw/pkg/types"
)

// The tier is a number now, and koanf decodes it through
// mapstructure. That path is the one most able to fail silently: a
// hook that does not fire leaves the field at its zero value, which
// here means "unset" and would quietly drop a provider out of every
// floor comparison. So it is exercised through a real config file
// rather than by calling the parser.

func writeTOML(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTOMLAcceptsNamedTiers(t *testing.T) {
	t.Parallel()
	path := writeTOML(t, `
[[compute.providers]]
label = "ollama"
trust_tier = "local"

[[compute.providers]]
label = "anthropic"
trust_tier = "private"

[[compute.providers]]
label = "openrouter"
trust_tier = "public"
`)
	cfg, err := Load(LoadOptions{Path: path, SkipEnv: true})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]types.TrustTier{
		"ollama": types.TrustLocal, "anthropic": types.TrustPrivate, "openrouter": types.TrustPublic,
	}
	for _, p := range cfg.Compute.Providers {
		if got := p.TrustTier; got != want[p.Label] {
			t.Errorf("%s: tier = %d, want %d", p.Label, int(got), int(want[p.Label]))
		}
	}
}

// The case the numeric scale exists for: a model on a rented VPS is
// not local — the hardware is somebody else's — and is plainly better
// than a public API with no contract.
func TestTOMLAcceptsANumberBetweenTheNamedTiers(t *testing.T) {
	t.Parallel()
	path := writeTOML(t, `
[[compute.providers]]
label = "my-vps"
trust_tier = 60
`)
	cfg, err := Load(LoadOptions{Path: path, SkipEnv: true})
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.Compute.Providers[0].TrustTier
	if got != types.TrustTier(60) {
		t.Fatalf("tier = %d, want 60", int(got))
	}
	if !got.AtLeast(types.TrustPrivate) || got.AtLeast(types.TrustLocal) {
		t.Errorf("60 does not sit between private and local")
	}
}

// A quoted number is the same value. TOML makes the two easy to
// confuse and an operator should not have to know which one koanf
// prefers.
func TestTOMLAcceptsAQuotedNumber(t *testing.T) {
	t.Parallel()
	path := writeTOML(t, `
[[compute.providers]]
label = "p"
trust_tier = "60"
`)
	cfg, err := Load(LoadOptions{Path: path, SkipEnv: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Compute.Providers[0].TrustTier; got != types.TrustTier(60) {
		t.Errorf("tier = %d, want 60", int(got))
	}
}

// An omitted tier stays unset rather than becoming the weakest real
// tier. Those are different facts: nobody said, versus somebody said
// public.
func TestAnOmittedTierIsUnsetNotPublic(t *testing.T) {
	t.Parallel()
	path := writeTOML(t, `
[[compute.providers]]
label = "p"
`)
	cfg, err := Load(LoadOptions{Path: path, SkipEnv: true})
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.Compute.Providers[0].TrustTier
	if got != types.TrustUnset {
		t.Errorf("tier = %d, want unset", int(got))
	}
	if got.IsValid() {
		t.Error("an omitted tier reports itself usable")
	}
	// And it fails any real floor, which is the safe direction.
	if got.AtLeast(types.TrustPublic) {
		t.Error("an omitted tier satisfied the weakest floor")
	}
}

// A typo has to fail loudly. Silently decoding to unset would drop the
// provider from every floor comparison without saying why.
func TestATypoedTierFailsToLoad(t *testing.T) {
	t.Parallel()
	path := writeTOML(t, `
[[compute.providers]]
label = "p"
trust_tier = "privat"
`)
	_, err := Load(LoadOptions{Path: path, SkipEnv: true})
	if err == nil {
		t.Fatal("a typo'd tier loaded")
	}
	if !strings.Contains(err.Error(), "privat") {
		t.Errorf("err = %q; it does not name the offending value", err)
	}
}

func TestAnOutOfRangeTierFailsToLoad(t *testing.T) {
	t.Parallel()
	for _, v := range []string{"trust_tier = 500", "trust_tier = -1", "trust_tier = 101"} {
		path := writeTOML(t, "[[compute.providers]]\nlabel = \"p\"\n"+v+"\n")
		_, err := Load(LoadOptions{Path: path, SkipEnv: true})
		if err == nil {
			t.Errorf("%q loaded", v)
			continue
		}
		if !strings.Contains(err.Error(), "higher is more trusted") {
			t.Errorf("%q: err = %q; it does not say which direction the scale runs", v, err)
		}
	}
}

// A bare TOML integer never reaches the type's UnmarshalText —
// mapstructure converts int to int directly — so the range check has
// to happen in Validate. This is the test that would catch somebody
// deleting it.
func TestABareIntegerIsRangeCheckedToo(t *testing.T) {
	t.Parallel()
	path := writeTOML(t, "[[compute.providers]]\nlabel = \"p\"\ntrust_tier = 500\n")
	if _, err := Load(LoadOptions{Path: path, SkipEnv: true}); err == nil {
		t.Error("a bare out-of-range integer loaded")
	}
}

// An explicit zero is indistinguishable from an omitted field: both
// decode to Go's zero, and nothing downstream can tell them apart.
//
// Left as unset rather than chased with a raw-key lookup, because the
// two mean the same thing operationally — no usable tier, fails any
// floor. That is the safe direction, and the alternative is a special
// case in the loader that exists to produce a better error message for
// a value nobody writes on purpose.
func TestAnExplicitZeroIsTreatedAsUnset(t *testing.T) {
	t.Parallel()
	path := writeTOML(t, "[[compute.providers]]\nlabel = \"p\"\ntrust_tier = 0\n")
	cfg, err := Load(LoadOptions{Path: path, SkipEnv: true})
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.Compute.Providers[0].TrustTier
	if got != types.TrustUnset {
		t.Fatalf("tier = %d", int(got))
	}
	if got.AtLeast(types.TrustPublic) {
		t.Error("an explicit zero satisfied the weakest floor")
	}
}
