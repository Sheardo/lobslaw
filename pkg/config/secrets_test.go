package config

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jmylchreest/lobslaw/pkg/types"
)

func TestValidateSecretProviders(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		cfg     SecretsConfig
		wantErr string
	}{
		{"unlabelled", SecretsConfig{
			Providers: []SecretProviderConfig{{Driver: "exec"}},
		}, "needs a label"},
		// env: and file: resolve before any provider can exist, so
		// shadowing one would make the bootstrap path depend on the
		// thing it bootstraps.
		{"shadows env", SecretsConfig{
			Providers: []SecretProviderConfig{{Label: "env", Driver: "exec"}},
		}, "reserved"},
		{"shadows file, any case", SecretsConfig{
			Providers: []SecretProviderConfig{{Label: "FILE", Driver: "exec"}},
		}, "reserved"},
		{"duplicate label", SecretsConfig{
			Providers: []SecretProviderConfig{
				{Label: "bw", Driver: "bitwarden"},
				{Label: "bw", Driver: "exec", Command: []string{"true"}},
			},
		}, "duplicate"},
		{"no driver", SecretsConfig{
			Providers: []SecretProviderConfig{{Label: "bw"}},
		}, "needs a driver"},
		{"empty argv element", SecretsConfig{
			Providers: []SecretProviderConfig{{Label: "p", Driver: "exec", Command: []string{"pass", ""}}},
		}, "empty element"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateSecretProviders(tc.cfg)
			if err == nil {
				t.Fatal("want an error")
			}
			if !errors.Is(err, types.ErrInvalidConfig) {
				t.Errorf("should wrap ErrInvalidConfig; got %v", err)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q should mention %q", err, tc.wantErr)
			}
		})
	}

	valid := SecretsConfig{
		Providers: []SecretProviderConfig{
			{Label: "bw", Driver: "bitwarden"},
			{Label: "pass", Driver: "exec", Command: []string{"pass", "show", "{{path}}"}},
		},
	}
	if err := validateSecretProviders(valid); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}
	if err := validateSecretProviders(SecretsConfig{}); err != nil {
		t.Errorf("no providers is the normal case: %v", err)
	}
}

// The fields have to survive koanf, not merely compile.
//
// Every documented-but-absent bug found this week looked exactly like a
// struct that was right and a value that never arrived: `provider =
// "tavily"` parsed into nothing, a `kms:` scheme nobody implemented, a
// channel binding the seeder discarded. An inline table, a string
// array and a duration are three different unmarshalling paths and none
// of them was proven until this test.
func TestSecretProvidersRoundTripThroughTOML(t *testing.T) {
	t.Parallel()

	path := writeTempConfig(t, miniConfig+`
[secrets]
cache_ttl = "90s"

[[secrets.providers]]
label   = "bw"
driver  = "bitwarden"
timeout = "12s"
env        = { BW_CONFIG_DIR = "/var/lib/bw", CA_BUNDLE = "/etc/ssl/ca.pem" }
secret_env = { BW_SESSION = "env:BW_SESSION" }
options    = { field = "password" }

[[secrets.providers]]
label   = "pass"
driver  = "exec"
command = ["pass", "show", "{{path}}"]
`)

	cfg, err := Load(LoadOptions{Path: path, SkipEnv: true})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Secrets.CacheTTL; got != 90*time.Second {
		t.Errorf("cache_ttl = %v; want 90s", got)
	}
	if len(cfg.Secrets.Providers) != 2 {
		t.Fatalf("providers = %d; want 2", len(cfg.Secrets.Providers))
	}

	bw := cfg.Secrets.Providers[0]
	if bw.Label != "bw" || bw.Driver != "bitwarden" {
		t.Errorf("first provider = %+v", bw)
	}
	if bw.Timeout != 12*time.Second {
		t.Errorf("timeout = %v; want 12s", bw.Timeout)
	}
	// The inline tables are the ones most likely to arrive empty, and an
	// empty secret_env means a vault credential silently absent.
	if bw.Env["BW_CONFIG_DIR"] != "/var/lib/bw" || bw.Env["CA_BUNDLE"] != "/etc/ssl/ca.pem" {
		t.Errorf("env = %+v; the plaintext inline table did not survive", bw.Env)
	}
	if bw.SecretEnv["BW_SESSION"] != "env:BW_SESSION" {
		t.Errorf("secret_env = %+v; the reference table did not survive", bw.SecretEnv)
	}
	if bw.Options["field"] != "password" {
		t.Errorf("options = %+v", bw.Options)
	}

	pass := cfg.Secrets.Providers[1]
	if len(pass.Command) != 3 || pass.Command[0] != "pass" || pass.Command[2] != "{{path}}" {
		t.Errorf("command = %+v; the argv did not survive", pass.Command)
	}
}

// Validate runs inside Load, so a reserved label must fail there and
// not only when someone calls the validator directly.
func TestLoadRejectsReservedSecretLabel(t *testing.T) {
	t.Parallel()

	path := writeTempConfig(t, miniConfig+`
[[secrets.providers]]
label   = "env"
driver  = "exec"
command = ["true"]
`)
	if _, err := Load(LoadOptions{Path: path, SkipEnv: true}); err == nil {
		t.Fatal("Load should have refused a provider shadowing env:")
	}
}

// env used to BE the reference field. A config written against the
// shipped version would now hand its CLI the literal "env:BW_SESSION"
// as a session token, and authentication would fail for a reason
// nothing in the error names.
//
// A silent semantics change is the worst kind — so a value that still
// looks like a reference is a boot error naming the field that
// replaced it.
func TestEnvRejectsWhatLooksLikeAReference(t *testing.T) {
	t.Parallel()

	for _, v := range []string{"env:BW_SESSION", "file:/run/secrets/tok", "  env:X  "} {
		err := validateSecretProviders(SecretsConfig{
			Providers: []SecretProviderConfig{{
				Label: "bw", Driver: "bitwarden",
				Env: map[string]string{"BW_SESSION": v},
			}},
		})
		if err == nil {
			t.Errorf("env value %q looks like a reference and should be refused", v)
			continue
		}
		if !strings.Contains(err.Error(), "secret_env") {
			t.Errorf("error should name the field that replaced it; got %v", err)
		}
	}

	// Plaintext settings — the whole reason the split exists — stay fine.
	if err := validateSecretProviders(SecretsConfig{
		Providers: []SecretProviderConfig{{
			Label: "bw", Driver: "bitwarden",
			Env:       map[string]string{"BITWARDENCLI_APPDATA_DIR": "/var/lib/bw", "PATH_LIKE": "/a:/b"},
			SecretEnv: map[string]string{"BW_SESSION": "env:BW_SESSION"},
		}},
	}); err != nil {
		t.Errorf("plaintext env and referenced secret_env are the intended shape: %v", err)
	}
}
