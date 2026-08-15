package node

import (
	"strings"
	"sync"

	"github.com/jmylchreest/lobslaw/internal/compute"
	"github.com/jmylchreest/lobslaw/internal/compute/drivers/anthropic"
	"github.com/jmylchreest/lobslaw/internal/compute/drivers/dashscope"
	"github.com/jmylchreest/lobslaw/internal/compute/drivers/elevenlabs"
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

		// Generation modalities resolve their driver by name too, so a
		// second vendor is a registration rather than a rewrite of the
		// wiring. Job has no default registration under DriverOpenAI:
		// the async protocols share no shape, so there is nothing
		// sensible to default to.
		s.RegisterSpeak(compute.DriverOpenAI, compute.OpenAISpeakFactory)
		s.RegisterSpeak(elevenlabs.DriverName, elevenlabsSpeakFactory)
		s.RegisterImage(compute.DriverOpenAI, compute.OpenAIImageFactory)
		s.RegisterJob(compute.DriverMock, compute.MockJobFactory)
		s.RegisterJob(dashscope.DriverName, dashscopeJobFactory)
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
	return credentialForDriver(p.Driver, apiKey)
}

// credentialForDriver picks the auth shape a driver expects. Split out
// from credentialFor because the generation modalities resolve their
// driver from an already-resolved endpoint rather than from the whole
// ProviderConfig, and both paths must agree on what "anthropic" means.
func credentialForDriver(driver, apiKey string) compute.Credential {
	if apiKey == "" {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(driver)) {
	case compute.DriverAnthropic:
		return compute.NewHeaderCredential("x-api-key", apiKey)
	case elevenlabs.DriverName:
		return compute.NewHeaderCredential("xi-api-key", apiKey)
	default:
		return compute.NewBearerCredential(apiKey)
	}
}

// dashscopeJobFactory adapts the Wan video driver to the registry.
func dashscopeJobFactory(cfg compute.JobDriverConfig) (compute.JobDriver, error) {
	return dashscope.New(dashscope.Config{
		SubmitEndpoint: cfg.Endpoint,
		Model:          cfg.Model,
		Credential:     cfg.Credential,
		HTTPClient:     cfg.HTTPClient,
	})
}

// elevenlabsSpeakFactory adapts the ElevenLabs driver.
func elevenlabsSpeakFactory(cfg compute.SpeakDriverConfig) (compute.SpeakDriver, error) {
	return elevenlabs.New(elevenlabs.Config{
		BaseURL:    cfg.Endpoint,
		Model:      cfg.Model,
		Voice:      cfg.Voice,
		Format:     cfg.Format,
		Credential: cfg.Credential,
		HTTPClient: cfg.HTTPClient,
	})
}
