package config

import (
	"errors"
	"strings"
	"testing"

	"github.com/jmylchreest/lobslaw/pkg/types"
)

func TestValidateSearchProviders(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		compute ComputeConfig
		wantErr string
	}{
		{"unlabelled entry", ComputeConfig{
			SearchProviders: []SearchProviderConfig{{Driver: "searxng"}},
		}, "needs a label"},
		{"duplicate labels", ComputeConfig{
			SearchProviders: []SearchProviderConfig{{Label: "a"}, {Label: "a"}},
			WebSearch:       WebSearchConfig{Provider: "a"},
		}, "duplicate"},
		{"out of range trust tier", ComputeConfig{
			SearchProviders: []SearchProviderConfig{{Label: "a", TrustTier: types.TrustTier(500)}},
		}, "trust_tier"},
		{"several declared, none chosen", ComputeConfig{
			SearchProviders: []SearchProviderConfig{{Label: "a"}, {Label: "b"}},
		}, "selects none"},
		{"both provider and providers", ComputeConfig{
			SearchProviders: []SearchProviderConfig{{Label: "a"}},
			WebSearch:       WebSearchConfig{Provider: "a", Providers: []string{"a"}},
		}, "both provider and providers"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateSearchProviders(tc.compute)
			if err == nil {
				t.Fatal("want an error")
			}
			if !errors.Is(err, types.ErrInvalidConfig) {
				t.Errorf("error should wrap ErrInvalidConfig; got %v", err)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q should mention %q", err, tc.wantErr)
			}
		})
	}

	valid := ComputeConfig{
		SearchProviders: []SearchProviderConfig{
			{Label: "searxng", Driver: "searxng", Endpoint: "http://searxng:8080", TrustTier: types.TrustLocal},
			{Label: "exa", Driver: "exa", APIKeyRef: "env:EXA_API_KEY"},
		},
		WebSearch: WebSearchConfig{Providers: []string{"searxng", "exa"}},
	}
	if err := validateSearchProviders(valid); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}
}
