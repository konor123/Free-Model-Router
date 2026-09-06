package xai

import (
	"github.com/konor123/Free-Model-Router/internal/model"
)

// APIRoute builds the canonical xAI route for a model id and its opaque
// provider-native model identifier. API entitlement cannot be verified from an
// API key alone: access stays Unknown (fail closed, never auto-routed) until a
// later phase verifies it.
func APIRoute(pmid model.ProviderModelID, upstreamID string, caps model.Capabilities) model.ProviderRoute {
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
		// The xAI OpenAI-compatible layer supports streaming, tool calling,
		// structured output, vision, and reasoning at the protocol level.
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
// No key → no routes (fail closed). Key present → one API route.
func Routes(pmid model.ProviderModelID, upstreamID string, caps model.Capabilities) []model.ProviderRoute {
	if APIKey() == "" {
		return nil
	}
	return []model.ProviderRoute{APIRoute(pmid, upstreamID, caps)}
}
