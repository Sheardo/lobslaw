package node

import (
	"testing"

	"github.com/jmylchreest/lobslaw/pkg/config"
)

// The pre-driver config shape has to keep meaning what it meant, or
// every existing deployment loses web search on upgrade.
func TestResolvedSearchProvidersKeepsLegacyShapeWorking(t *testing.T) {
	t.Parallel()
	got := resolvedSearchProviders(config.ComputeConfig{WebSearch: config.WebSearchConfig{
		APIKeyRef: "env:EXA_API_KEY",
		Endpoint:  "https://api.exa.ai/search",
	}})
	if len(got) != 1 {
		t.Fatalf("providers = %+v", got)
	}
	if got[0].Driver != "exa" || got[0].APIKeyRef != "env:EXA_API_KEY" {
		t.Errorf("legacy config resolved to %+v; want Exa with the declared key", got[0])
	}
}

func TestResolvedSearchProvidersOrdersTheChain(t *testing.T) {
	t.Parallel()
	got := resolvedSearchProviders(config.ComputeConfig{
		SearchProviders: []config.SearchProviderConfig{
			{Label: "exa", Driver: "exa", APIKeyRef: "env:EXA_API_KEY"},
			{Label: "searxng", Driver: "searxng", Endpoint: "http://searxng:8080/search"},
		},
		WebSearch: config.WebSearchConfig{Providers: []string{"searxng", "exa"}},
	})
	if len(got) != 2 || got[0].Label != "searxng" || got[1].Label != "exa" {
		t.Fatalf("chain = %+v; selection order should win over declaration order", got)
	}
}

func TestResolvedSearchProvidersSingleDeclaredNeedsNoSelection(t *testing.T) {
	t.Parallel()
	got := resolvedSearchProviders(config.ComputeConfig{
		SearchProviders: []config.SearchProviderConfig{
			{Label: "searxng", Driver: "searxng", Endpoint: "http://searxng:8080"},
		},
	})
	if len(got) != 1 || got[0].Label != "searxng" {
		t.Fatalf("providers = %+v", got)
	}
}

// `provider = "exa"` alongside a top-level api_key_ref is the shape
// the config reference documented before it worked. It resolves to a
// bare driver name, so an unknown one fails at boot naming the drivers
// that exist — rather than being dropped, which is what used to happen.
func TestResolvedSearchProvidersReadsUnmatchedNameAsDriver(t *testing.T) {
	t.Parallel()
	got := resolvedSearchProviders(config.ComputeConfig{WebSearch: config.WebSearchConfig{
		Provider:  "exa",
		APIKeyRef: "env:EXA_API_KEY",
	}})
	if len(got) != 1 || got[0].Driver != "exa" || got[0].APIKeyRef != "env:EXA_API_KEY" {
		t.Fatalf("providers = %+v", got)
	}
}

func TestResolvedSearchProvidersEmptyWhenNothingDeclared(t *testing.T) {
	t.Parallel()
	if got := resolvedSearchProviders(config.ComputeConfig{}); len(got) != 0 {
		t.Errorf("providers = %+v; a node with no search config registers no tool", got)
	}
}

// A private address is the normal case for the backend this feature
// exists to support, and smokescreen refuses those regardless of the
// hostname ACL. Getting the predicate wrong in either direction is a
// bad afternoon: a false negative is a silent proxy denial, a false
// positive is a warning that trains operators to ignore warnings.
func TestIsPrivateHost(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"searxng":            true, // compose service name, on the bridge network
		"localhost":          true, // and the reason the bare-label rule alone is not enough
		"127.0.0.1":          true,
		"10.1.2.3":           true,
		"192.168.0.10":       true,
		"172.16.5.4":         true,
		"169.254.1.1":        true,
		"::1":                true,
		"api.exa.ai":         false,
		"8.8.8.8":            false,
		"search.example.com": false,
	}
	for host, want := range cases {
		if got := isPrivateHost(host); got != want {
			t.Errorf("isPrivateHost(%q) = %v; want %v", host, got, want)
		}
	}
}
