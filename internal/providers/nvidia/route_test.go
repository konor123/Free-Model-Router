package nvidia

import (
	"testing"

	"github.com/konor123/Free-Model-Router/internal/model"
)

func TestRouteIdentity(t *testing.T) {
	pmid := mustID(t, "nvidia", "b64-bWV0YS9sbGFtYQ")
	r := HostedRoute(pmid, "meta/llama-3.1-8b-instruct", model.Capabilities{})
	if r.ID != model.RouteID("nvidia-hosted::meta/llama-3.1-8b-instruct") {
		t.Fatalf("unexpected RouteID %q", r.ID)
	}
	if r.ModelID != pmid {
		t.Fatal("route must reference the ProviderModel")
	}
	if r.Provider != ProviderID || r.UpstreamModelID != "meta/llama-3.1-8b-instruct" {
		t.Fatalf("route execution identity mismatch: %+v", r)
	}
	if r.CredentialID != RouteName {
		t.Fatalf("credential id = %q, want %q", r.CredentialID, RouteName)
	}
	if !r.Enabled {
		t.Fatal("route should be enabled")
	}
	if r.CapabilityOverride == nil {
		t.Fatal("hosted route must carry protocol capability overrides")
	}
}

func TestAccessIsUnknownUntilVerified(t *testing.T) {
	r := HostedRoute(mustID(t, "nvidia", "b64-bWV0YS9sbGFtYQ"), "meta/llama", model.Capabilities{})
	// Hosted catalog access depends on the user's entitlement, which FMR
	// cannot verify from an API key alone: fail closed as Unknown.
	if r.EffectiveAccess() != model.AccessUnknown {
		t.Fatalf("route access = %q, want Unknown", r.EffectiveAccess())
	}
	if r.EffectiveAccess().AutoRoutable() {
		t.Fatal("unknown access must not be auto-routable")
	}
}

func TestNoKeyMeansNoRoutes(t *testing.T) {
	t.Setenv(EnvKey, "")
	routes := Routes(mustID(t, "nvidia", "b64-bWV0YS9sbGFtYQ"), "meta/llama", model.Capabilities{})
	if len(routes) != 0 {
		t.Fatalf("expected 0 routes without key, got %d", len(routes))
	}
}

func TestKeyPresentAddsHostedRoute(t *testing.T) {
	t.Setenv(EnvKey, "nvapi-test-key")
	routes := Routes(mustID(t, "nvidia", "b64-bWV0YS9sbGFtYQ"), "meta/llama", model.Capabilities{})
	if len(routes) != 1 {
		t.Fatalf("expected 1 route with key, got %d", len(routes))
	}
	if routes[0].ID != model.RouteID("nvidia-hosted::meta/llama") {
		t.Fatalf("unexpected route %q", routes[0].ID)
	}
}

func TestAPIKeyFromEnv(t *testing.T) {
	t.Setenv(EnvKey, "")
	if APIKey() != "" {
		t.Fatal("empty env must yield empty key")
	}
	t.Setenv(EnvKey, "  nvapi-key  ")
	if APIKey() != "nvapi-key" {
		t.Fatalf("expected trimmed key, got %q", APIKey())
	}
}

func TestCanonicalizeStripsOrgAndVariantSuffixes(t *testing.T) {
	cases := map[string]string{
		"meta/llama-3.1-8b-instruct":        "llama-3.1-8b-instruct",
		"deepseek-ai/deepseek-r1":           "deepseek-r1",
		"qwen/qwen2.5-coder-32b-instruct":   "qwen2.5-coder-32b-instruct",
		"org/model-preview":                 "model",
		"org/model-it":                      "model",
		"org/model":                         "model",
	}
	for in, want := range cases {
		if got := canonicalize(in); got != want {
			t.Errorf("canonicalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func mustID(t *testing.T, prov, mdl string) model.ProviderModelID {
	t.Helper()
	id, err := model.NewProviderModelID(prov, mdl)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
