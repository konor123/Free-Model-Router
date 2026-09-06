package nvidia

import (
	"github.com/konor123/Free-Model-Router/internal/model"
)

// HostedRoute builds the canonical hosted route for a model id and its opaque
// provider-native model identifier. Hosted access depends on the user's
// entitlement, which cannot be verified from an API key alone: access stays
// Unknown (fail closed, never auto-routed) until a later phase verifies it.
func HostedRoute(pmid model.ProviderModelID, upstreamID string, caps model.Capabilities) model.ProviderRoute {
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
		// The hosted NIM API supports streaming and tool calling at the
		// protocol level; model-specific features come from the base caps.
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
// No key → no routes (fail closed). Key present → one hosted route.
func Routes(pmid model.ProviderModelID, upstreamID string, caps model.Capabilities) []model.ProviderRoute {
	if APIKey() == "" {
		return nil
	}
	return []model.ProviderRoute{HostedRoute(pmid, upstreamID, caps)}
}
