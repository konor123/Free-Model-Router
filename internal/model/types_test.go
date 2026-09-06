package model

import "testing"

func TestAccessAutoRoutable(t *testing.T) {
	cases := map[AccessClass]bool{
		AccessFree:     true,
		AccessFreeTier: true,
		AccessUnknown:  false,
		AccessPaid:     false,
	}
	for a, want := range cases {
		if got := a.AutoRoutable(); got != want {
			t.Fatalf("%s.AutoRoutable() = %v, want %v", a, got, want)
		}
	}
}

func TestCapabilityIntersection(t *testing.T) {
	base := Capabilities{Streaming: true, Tools: true, Vision: false, ContextLength: 128000}
	override := Capabilities{Streaming: true, Tools: false, Vision: true, ContextLength: 32000}
	got := base.Intersect(override)
	if !got.Streaming || got.Tools || got.Vision {
		t.Fatalf("intersection wrong: %+v", got)
	}
	if got.ContextLength != 32000 {
		t.Fatalf("context = %d, want 32000", got.ContextLength)
	}
}

func TestCapabilityStateDistinguishesUnknownAndUnsupported(t *testing.T) {
	unknown := Capabilities{}
	unknown.MarkUnknown(CapTools)
	if got := unknown.State(CapTools); got != CapabilityUnknown {
		t.Fatalf("missing metadata state = %v, want Unknown", got)
	}
	if unknown.Supports(RequestRequirements{Tools: true}) {
		t.Fatal("unknown tools capability must fail closed for routing")
	}

	unsupported := Capabilities{}
	if got := unsupported.State(CapTools); got != CapabilityUnsupported {
		t.Fatalf("explicit false state = %v, want Unsupported", got)
	}

	supported := Capabilities{Tools: true}
	if got := supported.State(CapTools); got != CapabilitySupported {
		t.Fatalf("explicit true state = %v, want Supported", got)
	}
}

func TestCapabilityIntersectionPreservesUnknownUnlessVetoed(t *testing.T) {
	base := Capabilities{}
	base.MarkUnknown(CapTools)
	protocol := Capabilities{Tools: true}
	if got := base.Intersect(protocol).State(CapTools); got != CapabilityUnknown {
		t.Fatalf("unknown model plus supported route = %v, want Unknown", got)
	}
	unsupportedRoute := Capabilities{}
	if got := base.Intersect(unsupportedRoute).State(CapTools); got != CapabilityUnsupported {
		t.Fatalf("unsupported route must veto unknown model, got %v", got)
	}
}

func TestSupportsRequirement(t *testing.T) {
	caps := Capabilities{Streaming: true, Tools: true, ContextLength: 8192, MaxOutput: 4096}

	req := RequestRequirements{Tools: true, MaxOutputTokens: 2048}
	if !caps.Supports(req) {
		t.Fatal("should support tools + 2048 output")
	}
	if caps.Supports(RequestRequirements{Vision: true}) {
		t.Fatal("should not support vision")
	}
	if caps.Supports(RequestRequirements{MinContextLength: 100000}) {
		t.Fatal("should not support 100k context")
	}
	if caps.Supports(RequestRequirements{MaxOutputTokens: 8192}) {
		t.Fatal("should not support 8192 output tokens")
	}
}

func TestRouteEffectiveCapabilities(t *testing.T) {
	base := Capabilities{Streaming: true, Tools: true}
	route := ProviderRoute{ID: "opencode-public::m", ModelID: "opencode/m", Provider: "opencode", UpstreamModelID: "m", Access: AccessFree, Enabled: true,
		CapabilityOverride: &Capabilities{Streaming: true, Tools: false}}
	got := route.EffectiveCapabilities(base)
	if got.Streaming != true || got.Tools != false {
		t.Fatalf("effective capabilities wrong: %+v", got)
	}
}

func TestRouteEffectiveAccessDefaultsToUnknown(t *testing.T) {
	route := ProviderRoute{}
	if route.EffectiveAccess() != AccessUnknown {
		t.Fatalf("empty route access = %q, want Unknown", route.EffectiveAccess())
	}
}

func TestCatalogSnapshotValidate(t *testing.T) {
	s := &CatalogSnapshot{Revision: 3}
	if err := s.Validate(); err == nil {
		t.Fatal("nil models map should fail validation")
	}
	s.Models = map[ProviderModelID]ProviderModel{}
	if err := s.Validate(); err != nil {
		t.Fatalf("empty map should pass: %v", err)
	}
}
