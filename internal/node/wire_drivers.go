package node

import (
	"strings"
	"sync"

	"github.com/jmylchreest/lobslaw/internal/compute"
	"github.com/jmylchreest/lobslaw/internal/compute/drivers/anthropic"
	"github.com/jmylchreest/lobslaw/pkg/config"
)

// The driver table for this build.
//
// Assembled here rather than in compute because driver packages import
// compute for the request types, so compute cannot import them back.
// That inversion is deliberate: compute owns the contract, the wiring
// layer owns which implementations exist.
//
// Adding a driver is one line below plus one package. If it ever needs
// more than that, the waist has sprung a leak.
var (
	driverSetOnce sync.Once
	driverSet     *compute.DriverSet
)

func (n *Node) drivers() *compute.DriverSet {
	driverSetOnce.Do(func() {
		s := compute.NewDriverSet()
		s.RegisterChat(compute.DriverOpenAI, compute.OpenAIChatFactory)
		s.RegisterChat(compute.DriverAnthropic, anthropicChatFactory)
		s.RegisterChat(compute.DriverMock, compute.MockChatFactory)
		driverSet = s
	})
	return driverSet
}

// anthropicChatFactory adapts the package's own constructor to the
// generic factory signature.
func anthropicChatFactory(cfg compute.ChatDriverConfig) (compute.ChatDriver, error) {
	return anthropic.New(anthropic.Config{
		Endpoint:   cfg.Endpoint,
		Model:      cfg.Model,
		Credential: cfg.Credential,
		HTTPClient: cfg.HTTPClient,
		Logger:     cfg.Logger,
	})
}

// credentialFor builds the credential a driver will apply.
//
// The header differs per protocol — Anthropic wants a bare x-api-key,
// everything else a bearer token — and that belongs here rather than
// in the driver, so a driver never has to ask what kind of credential
// it was handed. Vertex and Bedrock will add kinds here, not branches
// inside drivers.
func credentialFor(p config.ProviderConfig, apiKey string) compute.Credential {
	if apiKey == "" {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(p.Driver)) {
	case compute.DriverAnthropic:
		return compute.NewHeaderCredential("x-api-key", apiKey)
	default:
		return compute.NewBearerCredential(apiKey)
	}
}
