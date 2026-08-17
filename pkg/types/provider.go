package types

// ProviderConfig describes one LLM provider endpoint. Providers are
// identified by Label in chains and resolver config.
type ProviderConfig struct {
	Label        string    `json:"label"`    // "fast", "smart", "local-llama"
	Endpoint     string    `json:"endpoint"` // OpenAI-compatible base URL
	Model        string    `json:"model"`
	APIKeyRef    string    `json:"api_key_ref,omitempty"` // env:VAR, kms:arn, file:/path
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
	// A map rather than a field per concept, because the set is open —
	// R22 names video_seconds, images and credits and then an
	// ellipsis, and a field per vendor idea means a code change and a
	// release every time somebody meters something new.
	//
	// Tokens deliberately stay above as their own fields. They are the
	// common case, they need the cached/uncached distinction this map
	// cannot express, and putting them here would make every chat turn
	// pay a map lookup to price the thing it always prices.
	UnitUSD map[string]float64 `json:"unit_usd,omitempty" koanf:"unit_usd,omitempty"`
}

// Billing units. Free-form strings, so a vendor with a unit nobody
// anticipated is a config entry rather than a patch — these are the
// ones lobslaw itself reports.
const (
	// UnitVideoSeconds is billed per second of OUTPUT video, which is
	// not the same as the time the job took to run.
	UnitVideoSeconds = "video_seconds"
	// UnitImages is billed per image returned, which for a provider
	// asked for four variations is four.
	UnitImages = "images"
	// UnitAudioCharacters is billed per character of input text, the
	// usual shape for text-to-speech.
	UnitAudioCharacters = "audio_characters"
	// UnitCredits is an opaque vendor unit. Priced if the operator
	// says what a credit costs; counted regardless, so a plan-billed
	// provider still shows consumption even at a marginal cost of nil.
	UnitCredits = "credits"
)

// ChainConfig is an ordered set of provider steps (primary +
// optional reviewers). Picked by the resolver when triggers match.
type ChainConfig struct {
	Label        string       `json:"label"`
	Steps        []ChainStep  `json:"steps"`
	Trigger      ChainTrigger `json:"trigger"`
	MinTrustTier TrustTier    `json:"min_trust_tier,omitempty"`
}

// ChainStep is one step in a chain. Role is advisory metadata
// ("primary", "reviewer", "synthesizer"). PromptTemplate, when
// present, wraps the previous step's output — e.g. "Review this
// response for accuracy: {{response}}".
type ChainStep struct {
	Provider       string `json:"provider"` // label ref to a ProviderConfig
	Role           string `json:"role"`
	PromptTemplate string `json:"prompt_template,omitempty"`
}

// ChainTrigger is a predicate over the turn's analysis that picks
// this chain. The resolver evaluates triggers in order; first match
// wins. Always=true makes the chain the default.
type ChainTrigger struct {
	MinComplexity int      `json:"min_complexity,omitempty"` // 1-10
	Domains       []string `json:"domains,omitempty"`        // "code", "creative", ...
	Always        bool     `json:"always,omitempty"`
}

// IsSet reports whether any rate is configured.
//
// A method rather than a == comparison against the zero value:
// UnitUSD is a map, and a struct containing one cannot be compared.
func (p ProviderPricing) IsSet() bool {
	return p.InputUSDPer1K != 0 || p.OutputUSDPer1K != 0 ||
		p.CachedUSDPer1K != 0 || len(p.UnitUSD) > 0
}
