package types

// ProviderConfig describes one LLM provider endpoint. Providers are
// identified by Label in chains and resolver config.
type ProviderConfig struct {
	Label        string    `json:"label"`    // "fast", "smart", "local-llama"
	Endpoint     string    `json:"endpoint"` // OpenAI-compatible base URL
	Model        string    `json:"model"`
	APIKeyRef    string    `json:"api_key_ref,omitempty"` // env:VAR, file:/path, or a [[secrets.providers]] label
	Capabilities []string  `json:"capabilities,omitempty"`
	TrustTier    TrustTier `json:"trust_tier"`
	// Pricing overrides the built-in pricing table for this
	// provider. Zero values mean "use the built-in default".
	Pricing *ProviderPricing `json:"pricing,omitempty"`
}

// ProviderPricing is what a provider charges. Zero fields fall back to
// the hardcoded defaults.
type ProviderPricing struct {
	InputUSDPer1K  float64 `json:"input_usd_per_1k,omitempty"  koanf:"input_usd_per_1k,omitempty"`
	OutputUSDPer1K float64 `json:"output_usd_per_1k,omitempty" koanf:"output_usd_per_1k,omitempty"`
	CachedUSDPer1K float64 `json:"cached_usd_per_1k,omitempty" koanf:"cached_usd_per_1k,omitempty"`

	// UnitUSD prices what is NOT billed in tokens: video by the
	// second, images by the image, some vendors by an opaque credit.
	//
	// A map rather than a field per concept, because the set is open
	// and a field per vendor idea means a code change and a release
	// every time somebody meters something new. Keyed by plain string
	// rather than by compute.Unit because a rate card is CONFIG, and
	// this package cannot import the one that owns the vocabulary.
	//
	// Tokens deliberately stay above as their own fields. They are the
	// common case, they need the cached/uncached distinction this map
	// cannot express, and putting them here would make every chat turn
	// pay a map lookup to price the thing it always prices.
	UnitUSD map[string]float64 `json:"unit_usd,omitempty" koanf:"unit_usd,omitempty"`
}

// IsSet reports whether any rate is configured.
//
// A method rather than a == comparison against the zero value:
// UnitUSD is a map, and a struct containing one cannot be compared.
func (p ProviderPricing) IsSet() bool {
	return p.InputUSDPer1K != 0 || p.OutputUSDPer1K != 0 ||
		p.CachedUSDPer1K != 0 || len(p.UnitUSD) > 0
}
