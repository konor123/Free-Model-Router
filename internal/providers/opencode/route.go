// Package opencode implements the OpenCode Public provider (PLAN_V7 §6).
//
// OpenCode Public is a free, key-less OpenAI-compatible endpoint.
// Route naming: opencode-public::<model>.
package opencode

import (
	"github.com/konor123/Free-Model-Router/internal/model"
)

// PublicRouteName is the fixed route name for OpenCode Public.
const PublicRouteName = "opencode-public"

// ProviderID is the provider segment used in ProviderModelIDs.
const ProviderID = "opencode"

// baseURL is the OpenCode public API base. Overridable for tests.
var defaultBaseURL = "https://opencode.ai/zen/v1"

// PublicRoute builds the canonical public route for a model id and its opaque
// provider-native model identifier.
func PublicRoute(pmid model.ProviderModelID, upstreamID string, caps model.Capabilities) model.ProviderRoute {
	rid, _ := model.NewRouteID(PublicRouteName, upstreamID)
	return model.ProviderRoute{
		ID:              rid,
		ModelID:         pmid,
		Provider:        ProviderID,
		UpstreamModelID: upstreamID,
		CredentialID:    PublicRouteName,
		Access:          model.AccessFree,
		Enabled:         true,
		// Protocol-level capabilities intersected with model base capabilities.
		CapabilityOverride: &model.Capabilities{
			Streaming:        true,
			Tools:            true,
			StructuredOutput: true,
			// Vision/Reasoning depend on the model, not the route: leave false (intersection no-ops only if base false).
			Vision:    true,
			Reasoning: true,
		},
	}
}
