package xai

import (
	"testing"

	"github.com/konor123/Free-Model-Router/internal/model"
)

func TestRouteIdentity(t *testing.T) {
	pmid := mustID(t, "xai", "grok-4.6")
	r := APIRoute(pmid, "grok-4.6", model.Capabilities{})
	if r.ID != model.RouteID("xai-api::grok-4.6") {
		t.Fatalf("unexpected RouteID %q", r.ID)
	}
	if r.ModelID != pmid {
		t.Fatal("route must reference the ProviderModel")
	}
	if r.Provider != ProviderID || r.UpstreamModelID != "grok-4.6" {
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
	r := APIRoute(mustID(t, "xai", "grok-4.6"), "grok-4.6", model.Capabilities{})
	// xAI API entitlement cannot be verified from a key alone: fail closed as
	// Unknown (never auto-routed until a later phase verifies).
	if r.EffectiveAccess() != model.AccessUnknown {
		t.Fatalf("route access = %q, want Unknown", r.EffectiveAccess())
	}
	if r.EffectiveAccess().AutoRoutable() {
		t.Fatal("unknown access must not be auto-routable")
	}
}

func TestNoKeyMeansNoRoutes(t *testing.T) {
	t.Setenv(EnvKey, "")
	routes := Routes(mustID(t, "xai", "grok-4.6"), "grok-4.6", model.Capabilities{})
	if len(routes) != 0 {
		t.Fatalf("expected 0 routes without key, got %d", len(routes))
	}
}

func TestKeyPresentAddsAPIRoute(t *testing.T) {
	t.Setenv(EnvKey, "test-key")
	routes := Routes(mustID(t, "xai", "grok-4.6"), "grok-4.6", model.Capabilities{})
	if len(routes) != 1 {
		t.Fatalf("expected 1 route with key, got %d", len(routes))
	}
	if routes[0].ID != model.RouteID("xai-api::grok-4.6") {
		t.Fatalf("unexpected route %q", routes[0].ID)
	}
}

func TestAPIKeyFromEnv(t *testing.T) {
	t.Setenv(EnvKey, "")
	if APIKey() != "" {
		t.Fatal("empty env must yield empty key")
	}
	t.Setenv(EnvKey, "  x-key  ")
	if APIKey() != "x-key" {
		t.Fatalf("expected trimmed key, got %q", APIKey())
	}
}

func TestCanonicalizeStripsVariantSuffixes(t *testing.T) {
	cases := map[string]string{
		"grok-4.6":      "grok-4.6",
		"grok-3-mini":   "grok-3-mini",
		"model-preview": "model",
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
