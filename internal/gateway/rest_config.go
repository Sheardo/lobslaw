package gateway

import (
	"net/http"
	"strings"
)

// ConfigView is what the console is allowed to see of the node's
// configuration.
//
// An ALLOWLIST, assembled field by field at the wiring site, rather
// than a marshalled Config with redactions applied. The difference
// matters: with a denylist you leak whatever nobody thought to redact,
// and the thing nobody thought to redact is by definition the thing
// nobody was thinking about. Here you can only ever leak what somebody
// named.
//
// Same reasoning as memory.archiveKinds, which is opt-in for the same
// reason and in the same direction.
type ConfigView struct {
	NodeID    string   `json:"node_id"`
	Version   string   `json:"version,omitempty"`
	Functions []string `json:"functions"`

	Gateway  ConfigGatewayView  `json:"gateway"`
	Compute  ConfigComputeView  `json:"compute"`
	Memory   ConfigMemoryView   `json:"memory"`
	Bots     ConfigBotsView     `json:"bots"`
	Channels []ConfigChannelRow `json:"channels"`
}

type ConfigGatewayView struct {
	Enabled     bool   `json:"enabled"`
	BindAddress string `json:"bind_address"`
	HTTPPort    int    `json:"http_port"`
	RequireAuth bool   `json:"require_auth"`
	UIEnabled   bool   `json:"ui_enabled"`
	// LoginConfigured says whether a console token exists, NOT what it
	// is. An operator locked out needs to know which of the two
	// problems they have.
	LoginConfigured bool   `json:"login_configured"`
	DefaultTimezone string `json:"default_timezone,omitempty"`
	QueueMode       string `json:"queue_mode,omitempty"`
}

type ConfigComputeView struct {
	// Providers are LABELS and roles, never endpoints or credentials.
	// The same line list_providers draws, for the same reason: a
	// console that showed endpoints would be a console that showed
	// which host holds the API key.
	Providers        []ConfigProviderRow `json:"providers"`
	MaxToolCalls     int                 `json:"max_tool_calls_per_turn"`
	SelfLearningMode string              `json:"self_learning_mode,omitempty"`
}

type ConfigProviderRow struct {
	Label     string   `json:"label"`
	TrustTier string   `json:"trust_tier,omitempty"`
	Roles     []string `json:"roles,omitempty"`
}

type ConfigMemoryView struct {
	Enabled       bool   `json:"enabled"`
	DreamSchedule string `json:"dream_schedule,omitempty"`
	// EmbeddingModel identifies the vector space the corpus is in,
	// which is the one thing an operator debugging bad recall needs.
	EmbeddingModel string `json:"embedding_model,omitempty"`
}

type ConfigBotsView struct {
	MaxPending   int  `json:"max_pending"`
	DrainEnabled bool `json:"drain_enabled"`
}

type ConfigChannelRow struct {
	Type    string `json:"type"`
	Enabled bool   `json:"enabled"`
}

// handleConfig serves GET /v1/config.
//
// Read-only. Editing configuration from a browser would mean a running
// node writing its own config.toml, and every reload caveat that
// section of ARCHITECTURE.md documents becomes a race somebody
// triggers by clicking Save.
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Config == nil {
		s.jsonErr(w, http.StatusServiceUnavailable, "this node does not publish its configuration")
		return
	}
	if _, err := s.authenticate(r); err != nil {
		s.jsonErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	if r.Method != http.MethodGet {
		s.jsonErr(w, http.StatusMethodNotAllowed, "GET")
		return
	}
	view := *s.cfg.Config
	view.Gateway.LoginConfigured = s.cfg.ConsoleToken != "" && len(s.cfg.ConsoleKey) > 0
	respondJSON(w, http.StatusOK, view)
}

// looksLikeSecret is the belt to the allowlist's braces.
//
// Nothing in ConfigView is meant to hold a credential, so this should
// never fire — it exists because "should never" is how the last one
// got in. Used by the test that walks the assembled view.
func looksLikeSecret(field, value string) bool {
	lower := strings.ToLower(field)
	for _, marker := range []string{"secret", "token", "password", "key", "credential"} {
		if strings.Contains(lower, marker) {
			// A reference is fine — it names where the secret lives
			// rather than what it is, and that is what an operator
			// wrote in the file.
			return !strings.HasPrefix(value, "env:") &&
				!strings.HasPrefix(value, "file:") &&
				value != ""
		}
	}
	return false
}
