package provider

import "crypto/sha256"

// ExecutionSettings is an immutable in-process snapshot for execution factories.
// It is never a management API response; credentials are excluded from JSON and
// ordinary formatting. A new credential snapshot requires a new executor.
type ExecutionSettings struct {
	API             string `json:"api"`
	Supplier        string `json:"supplier"`
	Model           string `json:"model"`
	MaxOutputTokens int    `json:"max_output_tokens"`
	APIKey          string `json:"-"`
}

func (ExecutionSettings) String() string   { return "[execution settings; credentials redacted]" }
func (ExecutionSettings) GoString() string { return "[execution settings; credentials redacted]" }
func (r *Route) ExecutionSettings() ExecutionSettings {
	return ExecutionSettings{API: r.API(), Supplier: r.profile.ID, Model: r.model.UpstreamModel, MaxOutputTokens: int(r.model.MaxOutputTokens), APIKey: r.key}
}

// ExecutionIdentity is an opaque in-process cache/binding discriminator. Price
// identity intentionally excludes credentials; execution identity must not.
// Do not expose it in APIs, logs or persistent audit records.
func (r *Route) ExecutionIdentity() [32]byte {
	return sha256.Sum256([]byte(r.fingerprint + "\x00" + r.key))
}

// PriceSnapshot is immutable non-secret accounting input for durable execution.
func (r *Route) PriceSnapshot() ModelInfo {
	m := r.model
	return ModelInfo{AllowMessagesStream: m.AllowMessagesStream, CachePricing: m.CachePricing.clone(), API: m.API, ID: m.ID, Provider: m.Provider, Currency: Currency, Region: r.profile.Region, PriceVersion: r.fingerprint, InputPerMillion: m.InputPerMillion, OutputPerMillion: m.OutputPerMillion, MaxInputTokens: m.MaxInputTokens, MaxOutputTokens: m.MaxOutputTokens}
}

// GovernanceChannelID identifies the reviewed new-api channel for this route.
func (r *Route) GovernanceChannelID() int { return r.model.NewAPIChannelID }

func (r *Route) GovernanceChannelRevision() string { return r.model.NewAPIChannelRevision }

func (r *Route) RequiresCacheEvidence() bool { return r.model.CachePricing != nil }

// TariffVersion selects the evidence contract before dispatch.
func (r *Route) TariffVersion() int {
	if r.model.CachePricing == nil {
		return 0
	}
	if r.model.CachePricing.WriteTTL != nil {
		return 2
	}
	return 1
}

func (r *Route) AllowsMessagesStream() bool { return r.model.AllowMessagesStream }

// UsesReadOnlyCache selects the reviewed DeepSeek hit/miss evidence contract.
// It never changes the legacy new-api contract requiring explicit read/write counts.
func (r *Route) UsesReadOnlyCache() bool {
	return r != nil && r.embedded && r.model.Provider == "deepseek" && r.model.CachePricing != nil && r.model.API == ""
}

// UsesNativeMessages is server-owned physical protocol selection, not a client switch.
func (r *Route) UsesNativeMessages() bool { return r != nil && r.embedded && r.model.API == "messages" }

// UsesEmbeddedCacheEvidence distinguishes both original embedded cache protocols from legacy contracts.
func (r *Route) UsesEmbeddedCacheEvidence() bool {
	return r.UsesReadOnlyCache() || r.UsesNativeMessages()
}
