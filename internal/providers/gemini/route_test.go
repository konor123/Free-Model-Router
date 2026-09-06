package gemini

import (
	"testing"

	"github.com/konor123/Free-Model-Router/internal/model"
)

func TestRouteIdentity(t *testing.T) {
	pmid := mustID(t, "gemini", "gemini-3.8-flash")
	r := AIRoute(pmid, "gemini-3.8-flash", model.Capabilities{})
	if r.ID != model.RouteID("gemini-ai::gemini-3.8-flash") {
		t.Fatalf("unexpected RouteID %q", r.ID)
	}
	if r.ModelID != pmid {
		t.Fatal("route must reference the ProviderModel")
	}
	if r.Provider != ProviderID || r.UpstreamModelID != "gemini-3.8-flash" {
		t.Fatalf("route execution identity mismatch: %+v", r)
	}
	if r.CredentialID != RouteName {
		t.Fatalf("credential id = %q, want %q", r.CredentialID, RouteName)
	}
	if !r.Enabled {
		t.Fatal("route should be enabled")
	}
	if r.CapabilityOverride == nil {
		t.Fatal("route must carry protocol capability overrides")
	}
}

func TestAccessIsUnknownUntilVerified(t *testing.T) {
	r := AIRoute(mustID(t, "gemini", "gemini-3.8-flash"), "gemini-3.8-flash", model.Capabilities{})
	// Gemini API free-tier entitlement cannot be verified from a key alone:
	// fail closed as Unknown (never auto-routed until a later phase verifies).
	if r.EffectiveAccess() != model.AccessUnknown {
		t.Fatalf("route access = %q, want Unknown", r.EffectiveAccess())
	}
	if r.EffectiveAccess().AutoRoutable() {
		t.Fatal("unknown access must not be auto-routable")
	}
}

func TestNoKeyMeansNoRoutes(t *testing.T) {
	t.Setenv(EnvKey, "")
	routes := Routes(mustID(t, "gemini", "gemini-3.8-flash"), "gemini-3.8-flash", model.Capabilities{})
	if len(routes) != 0 {
		t.Fatalf("expected 0 routes without key, got %d", len(routes))
	}
}

func TestKeyPresentAddsAIRoute(t *testing.T) {
	t.Setenv(EnvKey, "test-key")
	routes := Routes(mustID(t, "gemini", "gemini-3.8-flash"), "gemini-3.8-flash", model.Capabilities{})
	if len(routes) != 1 {
		t.Fatalf("expected 1 route with key, got %d", len(routes))
	}
	if routes[0].ID != model.RouteID("gemini-ai::gemini-3.8-flash") {
		t.Fatalf("unexpected route %q", routes[0].ID)
	}
}

func TestAPIKeyFromEnv(t *testing.T) {
	t.Setenv(EnvKey, "")
	if APIKey() != "" {
		t.Fatal("empty env must yield empty key")
	}
	t.Setenv(EnvKey, "  g-key  ")
	if APIKey() != "g-key" {
		t.Fatalf("expected trimmed key, got %q", APIKey())
	}
}

func TestCanonicalizeStripsVariantSuffixes(t *testing.T) {
	cases := map[string]string{
		"gemini-3.8-flash":       "gemini-3.8-flash",
		"gemini-2.5-pro-preview": "gemini-2.5-pro",
		"gemini-embedding-001":   "gemini-embedding-001",
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
