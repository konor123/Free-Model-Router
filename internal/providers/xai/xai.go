// Package xai implements the xAI Grok provider (PLAN_V7.1 Phase 10).
//
// xAI exposes an OpenAI-compatible endpoint at https://api.x.ai/v1 and
// requires a bearer API key (XAI_API_KEY). Route naming: xai-api::<model>.
package xai

import (
	"os"
	"strings"
)

// ProviderID is the provider segment used in ProviderModelIDs.
const ProviderID = "xai"

// RouteName is the fixed route name for the xAI Grok catalog.
const RouteName = "xai-api"

// EnvKey is the required API key environment variable.
const EnvKey = "XAI_API_KEY"

// DefaultBaseURL is the xAI OpenAI-compatible base. Overridable for tests.
const DefaultBaseURL = "https://api.x.ai/v1"

// APIKey reads the required API key. Empty string means the provider has no
// usable routes (fail closed: no key, no routes).
func APIKey() string {
	return strings.TrimSpace(os.Getenv(EnvKey))
}
