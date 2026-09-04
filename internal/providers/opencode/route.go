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

// PublicRoute builds the canonical public route for a model id.
func PublicRoute(pmid model.ProviderModelID, caps model.Capabilities, access model.AccessClass) model.ProviderRoute {
	mdl := ""
	if _, m, err := pmid.Parse(); err == nil {
		mdl = m
	}
	rid, _ := model.NewRouteID(PublicRouteName, mdl)
	return model.ProviderRoute{
		ID:      rid,
		ModelID: pmid,
		Access:  access,
		Enabled: true,
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
