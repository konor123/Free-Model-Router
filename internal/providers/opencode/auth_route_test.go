package opencode

import (
	"testing"

	"github.com/konor123/Free-Model-Router/internal/model"
)

func TestAuthKeyEnvOptional(t *testing.T) {
	t.Setenv(AuthRouteEnv, "")
	if AuthKey() != "" {
		t.Fatal("empty env must yield empty key")
	}
	t.Setenv(AuthRouteEnv, "test-key-123")
	if AuthKey() != "test-key-123" {
		t.Fatalf("expected key, got %q", AuthKey())
	}
}

func TestNoKeyMeansPublicOnly(t *testing.T) {
	t.Setenv(AuthRouteEnv, "")
	pmid := mustID(t, "opencode", "mimo-v2.5")
	routes := Routes(pmid, model.Capabilities{}, model.AccessFree)
	if len(routes) != 1 {
		t.Fatalf("expected 1 route without key, got %d", len(routes))
	}
	if routes[0].ID != model.RouteID("opencode-public::mimo-v2.5") {
		t.Fatalf("unexpected route %q", routes[0].ID)
	}
}

func TestKeyPresentAddsAuthRoute(t *testing.T) {
	t.Setenv(AuthRouteEnv, "test-key-123")
	pmid := mustID(t, "opencode", "mimo-v2.5")
	routes := Routes(pmid, model.Capabilities{}, model.AccessFree)
	if len(routes) != 2 {
		t.Fatalf("expected 2 routes with key, got %d", len(routes))
	}
	auth := routes[1]
	if auth.ID != model.RouteID("opencode-zen::mimo-v2.5") {
		t.Fatalf("unexpected auth route %q", auth.ID)
	}
	if auth.Access != model.AccessFreeTier {
		t.Fatalf("auth route access should be Free-tier, got %q", auth.Access)
	}
	if auth.ModelID != pmid {
		t.Fatal("auth route must reference same ProviderModel")
	}
}

func TestRouteAccessIndependence(t *testing.T) {
	t.Setenv(AuthRouteEnv, "k")
	pmid := mustID(t, "opencode", "mimo-v2.5")
	routes := Routes(pmid, model.Capabilities{}, model.AccessFree)
	pub, auth := routes[0], routes[1]
	if pub.Access != model.AccessFree || auth.Access != model.AccessFreeTier {
		t.Fatalf("access must be route-specific: pub=%q auth=%q", pub.Access, auth.Access)
	}
	if pub.ID == auth.ID {
		t.Fatal("route ids must differ")
	}
	if pub.CapabilityOverride == nil || auth.CapabilityOverride == nil {
		t.Fatal("both routes need protocol capability overrides")
	}
}

func TestAuthRouteStateIsolationOnFailure(t *testing.T) {
	// Zen auth endpoint 401 must not affect the public route object at all.
	t.Setenv(AuthRouteEnv, "bad-key")
	auth := AuthRoute(mustID(t, "opencode", "mimo-v2.5"))
	// Failure simulation happens at call level; here we verify the route struct
	// remains enabled and untouched (state is per-route by construction).
	if !auth.Enabled {
		t.Fatal("auth route must stay enabled; failure state lives elsewhere")
	}
	if auth.Access != model.AccessFreeTier {
		t.Fatal("auth access must be unchanged by failures")
	}
}
