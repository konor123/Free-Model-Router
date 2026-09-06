package gemini

import (
	"github.com/konor123/Free-Model-Router/internal/model"
)

// AIRoute builds the canonical AI Studio route for a model id and its opaque
// provider-native model identifier. Free-tier entitlement cannot be verified
// from an API key alone: access stays Unknown (fail closed, never auto-routed)
// until a later phase verifies it.
func AIRoute(pmid model.ProviderModelID, upstreamID string, caps model.Capabilities) model.ProviderRoute {
	rid, _ := model.NewRouteID(RouteName, upstreamID)
	return model.ProviderRoute{
		ID:              rid,
		ModelID:         pmid,
		Provider:        ProviderID,
		UpstreamModelID: upstreamID,
		CredentialID:    RouteName,
		Access:          model.AccessUnknown,
		Enabled:         true,
		// Protocol-level capabilities intersected with model base capabilities.
		// The Gemini OpenAI-compatible layer supports streaming, function
		// calling, structured output, and vision at the protocol level.
		CapabilityOverride: &model.Capabilities{
			Streaming:        true,
			Tools:            true,
			StructuredOutput: true,
			Vision:           true,
			Reasoning:        true,
		},
	}
}

// Routes returns all enabled routes for a model given the current auth state.
// No key → no routes (fail closed). Key present → one AI Studio route.
func Routes(pmid model.ProviderModelID, upstreamID string, caps model.Capabilities) []model.ProviderRoute {
	if APIKey() == "" {
		return nil
	}
	return []model.ProviderRoute{AIRoute(pmid, upstreamID, caps)}
}
