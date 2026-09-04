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
	route := ProviderRoute{ID: "opencode-public::m", ModelID: "opencode/m", Access: AccessFree, Enabled: true,
		CapabilityOverride: &Capabilities{Streaming: true, Tools: false}}
	got := route.EffectiveCapabilities(base)
	if got.Streaming != true || got.Tools != false {
		t.Fatalf("effective capabilities wrong: %+v", got)
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
