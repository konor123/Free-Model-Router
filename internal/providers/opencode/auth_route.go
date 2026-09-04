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

// AuthRoute builds the canonical Zen auth route for a model id.
func AuthRoute(pmid model.ProviderModelID) model.ProviderRoute {
	mdl := ""
	if _, m, err := pmid.Parse(); err == nil {
		mdl = m
	}
	rid, _ := model.NewRouteID(AuthRouteName, mdl)
	return model.ProviderRoute{
		ID:      rid,
		ModelID: pmid,
		// Authenticated Zen route is free-tier (quota applies per account).
		Access:  model.AccessFreeTier,
		Enabled: true,
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
func Routes(pmid model.ProviderModelID, caps model.Capabilities, access model.AccessClass) []model.ProviderRoute {
	routes := []model.ProviderRoute{PublicRoute(pmid, caps, access)}
	if AuthKey() != "" {
		routes = append(routes, AuthRoute(pmid))
	}
	return routes
}
