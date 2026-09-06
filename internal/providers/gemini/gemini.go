// Package gemini implements the Google Gemini provider (PLAN_V7.1 Phase 10).
//
// Gemini exposes an OpenAI-compatible endpoint at
// https://generativelanguage.googleapis.com/v1beta/openai/ and requires a
// bearer API key (GEMINI_API_KEY). Route naming: gemini-ai::<model>.
package gemini

import (
	"os"
	"strings"
)

// ProviderID is the provider segment used in ProviderModelIDs.
const ProviderID = "gemini"

// RouteName is the fixed route name for the Gemini AI Studio catalog.
const RouteName = "gemini-ai"

// EnvKey is the required API key environment variable.
const EnvKey = "GEMINI_API_KEY"

// DefaultBaseURL is the Gemini OpenAI-compatible base. Overridable for tests.
const DefaultBaseURL = "https://generativelanguage.googleapis.com/v1beta/openai"

// APIKey reads the required API key. Empty string means the provider has no
// usable routes (fail closed: no key, no routes).
func APIKey() string {
	return strings.TrimSpace(os.Getenv(EnvKey))
}
