package node

import (
	"github.com/jmylchreest/lobslaw/pkg/config"
)

// resolvedSearchProviders returns the search backends web_search will
// use, in failover order. Empty means the builtin is not registered
// and the model sees no web_search tool.
//
// One function rather than two because the tool wiring and the egress
// ACL builder must agree exactly: a backend the agent can call but the
// proxy has never heard of fails at the first query, with a denial
// that names neither the host nor the role.
//
// It lives here rather than as a method on ComputeConfig because
// pkg/config parses and validates, and the layer above decides what
// the parsed values mean.
func resolvedSearchProviders(c config.ComputeConfig) []config.SearchProviderConfig {
	declared := make(map[string]config.SearchProviderConfig, len(c.SearchProviders))
	for _, p := range c.SearchProviders {
		declared[p.Label] = p
	}

	names := c.WebSearch.Providers
	if len(names) == 0 && c.WebSearch.Provider != "" {
		names = []string{c.WebSearch.Provider}
	}

	if len(names) == 0 {
		switch {
		// The pre-driver shape: an api_key_ref and nothing else meant
		// Exa, and still does.
		case c.WebSearch.APIKeyRef != "":
			return []config.SearchProviderConfig{{
				Label:     "exa",
				Driver:    "exa",
				APIKeyRef: c.WebSearch.APIKeyRef,
				Endpoint:  c.WebSearch.Endpoint,
			}}
		// One declared backend needs no selecting. Several do, and
		// Config.Validate says so rather than this picking silently.
		case len(c.SearchProviders) == 1:
			return []config.SearchProviderConfig{c.SearchProviders[0]}
		default:
			return nil
		}
	}

	out := make([]config.SearchProviderConfig, 0, len(names))
	for _, name := range names {
		if p, ok := declared[name]; ok {
			out = append(out, p)
			continue
		}
		// An unmatched name is read as a bare driver name, which is
		// what `provider = "exa"` alongside a top-level api_key_ref
		// means. An unknown driver then fails at boot naming the
		// drivers that do exist — better than the old behaviour, where
		// an unrecognised provider key was dropped and Exa ran anyway.
		out = append(out, config.SearchProviderConfig{
			Label:     name,
			Driver:    name,
			APIKeyRef: c.WebSearch.APIKeyRef,
			Endpoint:  c.WebSearch.Endpoint,
		})
	}
	return out
}
