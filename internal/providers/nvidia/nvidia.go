// Package nvidia implements the NVIDIA hosted NIM provider (PLAN_V7.1 Phase 10).
//
// NVIDIA hosts OpenAI-compatible NIM endpoints on DGX Cloud. Models are named
// "org/model" (e.g. "meta/llama-3.1-8b-instruct") and require a bearer API key.
// Route naming: nvidia-hosted::<model>.
package nvidia

import (
	"os"
	"strings"
)

// ProviderID is the provider segment used in ProviderModelIDs.
const ProviderID = "nvidia"

// RouteName is the fixed route name for the NVIDIA hosted catalog.
const RouteName = "nvidia-hosted"

// EnvKey is the required API key environment variable.
const EnvKey = "NVIDIA_API_KEY"

// DefaultBaseURL is the NVIDIA hosted API base. Overridable for tests.
const DefaultBaseURL = "https://integrate.api.nvidia.com/v1"

// APIKey reads the required API key. Empty string means the provider has no
// usable routes (fail closed: no key, no routes).
func APIKey() string {
	return strings.TrimSpace(os.Getenv(EnvKey))
}
