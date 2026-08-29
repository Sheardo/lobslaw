package config

import (
	"errors"
	"strings"
	"testing"

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
