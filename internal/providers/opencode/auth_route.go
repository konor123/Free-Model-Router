package opencode

import (
	"os"
	"strings"

	"github.com/konor123/Free-Model-Router/internal/model"
)

// AuthRouteName is the route name for OpenCode Zen (authenticated).
const AuthRouteName = "opencode-zen"

// AuthRouteEnv is the optional API key environment variable.
const AuthRouteEnv = "OPENCODE_API_KEY"

// AuthBaseURL is the Zen authenticated endpoint base.
const AuthBaseURL = "https://opencode.ai/zen/v1"

// AuthRoute builds the canonical Zen auth route for a model id. Configuring a
// key alone does not establish a free-tier or paid-use entitlement.
func AuthRoute(pmid model.ProviderModelID, upstreamID string) model.ProviderRoute {
	rid, _ := model.NewRouteID(AuthRouteName, upstreamID)
	return model.ProviderRoute{
		ID:              rid,
		ModelID:         pmid,
		Provider:        ProviderID,
		UpstreamModelID: upstreamID,
		CredentialID:    AuthRouteName,
		Access:          model.AccessUnknown,
		Enabled:         true,
		CapabilityOverride: &model.Capabilities{
			Streaming:        true,
			Tools:            true,
			StructuredOutput: true,
			Vision:           true,
			Reasoning:        true,
		},
	}
}

// AuthKey reads the optional API key. Empty string means anonymous (public only).
func AuthKey() string {
	return strings.TrimSpace(os.Getenv(AuthRouteEnv))
}

// RoutesFor returns all enabled routes for a model given the current auth state.
// No key → Public only. Key present → Public + Zen Auth (PLAN_V7 §9).
func Routes(pmid model.ProviderModelID, upstreamID string, caps model.Capabilities) []model.ProviderRoute {
	routes := []model.ProviderRoute{PublicRoute(pmid, upstreamID, caps)}
	if AuthKey() != "" {
		routes = append(routes, AuthRoute(pmid, upstreamID))
	}
	return routes
}
