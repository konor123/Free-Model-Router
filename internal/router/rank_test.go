package router

import (
	"testing"

	"github.com/konor123/Free-Model-Router/internal/model"
)

func TestRankPrefersKnownLowerTTFTAndUsesStableTieBreakers(t *testing.T) {
	input := []Candidate{
		{Model: model.ProviderModel{ID: "opencode/slow"}, Route: route("slow", "opencode/slow", model.AccessFree, nil)},
		{Model: model.ProviderModel{ID: "opencode/unknown"}, Route: route("unknown", "opencode/unknown", model.AccessFree, nil)},
		{Model: model.ProviderModel{ID: "opencode/fast"}, Route: route("fast", "opencode/fast", model.AccessFree, nil)},
	}
	lookup := func(routeID string) (float64, bool) {
		switch routeID {
		case "slow":
			return 100, true
		case "fast":
			return 25, true
		default:
			return 0, false
		}
	}

	got := Rank(input, lookup)
	if got[0].Route.ID != "fast" || got[1].Route.ID != "slow" || got[2].Route.ID != "unknown" {
		t.Fatalf("ranked candidates = %+v", got)
	}
	if input[0].Route.ID != "slow" {
		t.Fatal("Rank must not mutate its input slice")
	}
}

func TestRankUsesModelAndRouteIDForUnknownTTFTTies(t *testing.T) {
	input := []Candidate{
		{Model: model.ProviderModel{ID: "opencode/z"}, Route: route("z-route", "opencode/z", model.AccessFree, nil)},
		{Model: model.ProviderModel{ID: "opencode/a"}, Route: route("z-route-2", "opencode/a", model.AccessFree, nil)},
	}
	got := Rank(input, nil)
	if got[0].Model.ID != "opencode/a" || got[1].Model.ID != "opencode/z" {
		t.Fatalf("unknown TTFT tie order = %+v", got)
	}
}
