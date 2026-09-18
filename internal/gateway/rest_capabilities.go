package gateway

import (
	"context"
	"encoding/json"
	"net/http"
)

type capabilityFlags struct {
	Enabled    bool `json:"enabled"`
	Authorised bool `json:"authorised"`
	Configured bool `json:"configured"`
	Available  bool `json:"available"`
}

type capabilitiesResponse struct {
	Compute      capabilityFlags `json:"compute"`
	ComputeTeams capabilityFlags `json:"compute-teams"`
	UIWeb        capabilityFlags `json:"ui-web"`
}

func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, err := s.authenticateRequest(r); err != nil {
		s.jsonErr(w, http.StatusUnauthorized, err.Error())
		return
	}

	computeOn := s.runner != nil
	teamsOn := s.cfg.Bots != nil
	uiOn := s.consoleEnabled()
	out := capabilitiesResponse{
		Compute: capabilityFlags{
			Enabled:    computeOn,
			Authorised: true,
			Configured: computeOn,
			Available:  s.computeAvailable(),
		},
		ComputeTeams: capabilityFlags{
			Enabled:    teamsOn,
			Authorised: teamsOn,
			Configured: teamsOn,
			Available:  teamsOn,
		},
		UIWeb: capabilityFlags{
			Enabled:    uiOn,
			Authorised: uiOn,
			Configured: uiOn,
			Available:  uiOn,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// computeAvailability is implemented by remote runners. Local agents
// omit it and are treated as available whenever the runner is non-nil.
type computeAvailability interface {
	Available(context.Context) bool
}

func (s *Server) computeAvailable() bool {
	if s.runner == nil {
		return false
	}
	if p, ok := s.runner.(computeAvailability); ok {
		return p.Available(context.Background())
	}
	return true
}
