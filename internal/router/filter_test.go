package router

import (
	"testing"

	"github.com/konor123/Free-Model-Router/internal/model"
)

func route(id, modelID string, access model.AccessClass, override *model.Capabilities) model.ProviderRoute {
	return model.ProviderRoute{
		ID:                 model.RouteID(id),
		ModelID:            model.ProviderModelID(modelID),
		Provider:           "opencode",
		UpstreamModelID:    id,
		Access:             access,
		Enabled:            true,
		CapabilityOverride: override,
	}
}

func buildFilter(t *testing.T) (FilterInput, map[string]model.ProviderModelID) {
	t.Helper()
	mk := func(name string) model.ProviderModelID {
		id, err := model.NewProviderModelID("opencode", name)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	ids := map[string]model.ProviderModelID{
		"text":    mk("text-only"),
		"vision":  mk("vision-model"),
		"tools":   mk("tools-model"),
		"big":     mk("big-context"),
		"paid":    mk("paid-model"),
		"unknown": mk("unknown-access"),
	}
	catalog := &model.CatalogSnapshot{
		Revision: 1,
		Models: map[model.ProviderModelID]model.ProviderModel{
			ids["text"]:    {ID: ids["text"], Base: model.Capabilities{Streaming: true}},
			ids["vision"]:  {ID: ids["vision"], Base: model.Capabilities{Streaming: true, Vision: true}},
			ids["tools"]:   {ID: ids["tools"], Base: model.Capabilities{Streaming: true, Tools: true, StructuredOutput: true}},
			ids["big"]:     {ID: ids["big"], Base: model.Capabilities{Streaming: true, ContextLength: 200000, MaxOutput: 8192}},
			ids["paid"]:    {ID: ids["paid"], Base: model.Capabilities{Streaming: true}},
			ids["unknown"]: {ID: ids["unknown"], Base: model.Capabilities{Streaming: true}},
		},
	}
	routes := map[model.ProviderModelID][]model.ProviderRoute{
		ids["text"]:    {route("pub-text", "opencode/text-only", model.AccessFree, nil)},
		ids["vision"]:  {route("pub-vision", "opencode/vision-model", model.AccessFree, nil)},
		ids["tools"]:   {route("pub-tools", "opencode/tools-model", model.AccessFree, nil)},
		ids["big"]:     {route("pub-big", "opencode/big-context", model.AccessFree, nil)},
		ids["paid"]:    {route("pub-paid", "opencode/paid-model", model.AccessPaid, nil)},
		ids["unknown"]: {route("pub-unk", "opencode/unknown-access", model.AccessUnknown, nil)},
	}
	in := FilterInput{
		Catalog: catalog,
		Routes:  routes,
		Pool: []model.ProviderModelID{
			ids["text"], ids["vision"], ids["tools"], ids["big"], ids["paid"], ids["unknown"],
		},
	}
	return in, ids
}

func TestVisionExcludesTextOnly(t *testing.T) {
	in, ids := buildFilter(t)
	in.Reqs = model.RequestRequirements{Vision: true}
	res := Filter(in)
	if len(res.Eligible) != 1 || res.Eligible[0].Model.ID != ids["vision"] {
		t.Fatalf("expected vision model only, got %+v", res)
	}
}

func TestToolsExcludeNoTools(t *testing.T) {
	in, ids := buildFilter(t)
	in.Reqs = model.RequestRequirements{Tools: true}
	res := Filter(in)
	if len(res.Eligible) != 1 || res.Eligible[0].Model.ID != ids["tools"] {
		t.Fatalf("expected tools model only, got %+v", res)
	}
}

func TestStructuredOutputIncompatibleExcluded(t *testing.T) {
	in, _ := buildFilter(t)
	in.Reqs = model.RequestRequirements{StructuredOutput: true}
	res := Filter(in)
	if len(res.Eligible) != 1 {
		t.Fatalf("expected only tools(structured) model, got %+v", res)
	}
}

func TestContextOverflowExcluded(t *testing.T) {
	in, _ := buildFilter(t)
	in.Reqs = model.RequestRequirements{MinContextLength: 100000}
	res := Filter(in)
	// Unknown limits (0) pass; only models with known-small context fail.
	// text-only has unknown context here, so it must remain eligible.
	if len(res.Eligible) == 0 {
		t.Fatal("expected some eligible with unknown limits passing")
	}
	// big-context must be eligible.
	found := false
	for _, c := range res.Eligible {
		if c.Model.ID == mustLookup(t, in, "big") {
			found = true
		}
	}
	if !found {
		t.Fatal("big-context should be eligible")
	}
}

func TestMaxOutputExcluded(t *testing.T) {
	in, _ := buildFilter(t)
	in.Reqs = model.RequestRequirements{MaxOutputTokens: 4096}
	res := Filter(in)
	found := false
	for _, c := range res.Eligible {
		if c.Model.ID == mustLookup(t, in, "big") {
			found = true
		}
	}
	if !found {
		t.Fatal("big-context (8192 out) should be eligible for 4096")
	}
}

func TestKnownSmallContextExcluded(t *testing.T) {
	in, _ := buildFilter(t)
	// Give text-only a known small context; requirement exceeds it.
	textID := mustLookup(t, in, "text")
	in.Catalog.Models[textID] = model.ProviderModel{
		ID: textID, Base: model.Capabilities{Streaming: true, ContextLength: 4096},
	}
	in.Reqs = model.RequestRequirements{MinContextLength: 100000}
	res := Filter(in)
	for _, c := range res.Eligible {
		if c.Model.ID == textID {
			t.Fatal("known small context must be excluded")
		}
	}
}

func TestKnownSmallMaxOutputExcluded(t *testing.T) {
	in, _ := buildFilter(t)
	textID := mustLookup(t, in, "text")
	in.Catalog.Models[textID] = model.ProviderModel{
		ID: textID, Base: model.Capabilities{Streaming: true, MaxOutput: 1024},
	}
	in.Reqs = model.RequestRequirements{MaxOutputTokens: 4096}
	res := Filter(in)
	for _, c := range res.Eligible {
		if c.Model.ID == textID {
			t.Fatal("known small max output must be excluded")
		}
	}
}

func TestStreamingUnsupportedExcluded(t *testing.T) {
	in, _ := buildFilter(t)
	// Route-level override disables streaming on the text model.
	textID := mustLookup(t, in, "text")
	noStream := false
	in.Routes[textID] = []model.ProviderRoute{
		{ID: "pub-text", ModelID: textID, Provider: "opencode", UpstreamModelID: "text-only", Access: model.AccessFree, Enabled: true,
			CapabilityOverride: &model.Capabilities{Streaming: noStream}},
	}
	in.Reqs = model.RequestRequirements{Streaming: true}
	res := Filter(in)
	for _, c := range res.Eligible {
		if c.Model.ID == textID {
			t.Fatal("no-stream override must exclude text model")
		}
	}
	if len(res.Eligible) == 0 {
		t.Fatal("other models should remain eligible")
	}
}

func TestPaidExcludedUnlessOptIn(t *testing.T) {
	in, _ := buildFilter(t)
	in.Pool = []model.ProviderModelID{mustLookup(t, in, "paid")}
	res := Filter(in)
	if len(res.Eligible) != 0 {
		t.Fatal("paid must be excluded by default")
	}
	in.AllowPaid = true
	res = Filter(in)
	if len(res.Eligible) != 1 {
		t.Fatal("paid allowed with explicit opt-in")
	}
}

func TestUnknownAccessNeverAutoRoutes(t *testing.T) {
	in, _ := buildFilter(t)
	in.Pool = []model.ProviderModelID{mustLookup(t, in, "unknown")}
	res := Filter(in)
	if len(res.Eligible) != 0 {
		t.Fatal("unknown access must never auto-route")
	}
}

func TestFallbackKeepsSameRequirements(t *testing.T) {
	// Simulate fallback: rerunning Filter with the identical input after a
	// candidate failure must produce the same eligible set.
	in, _ := buildFilter(t)
	in.Reqs = model.RequestRequirements{Tools: true, Streaming: true}
	first := Filter(in)
	second := Filter(in)
	if len(first.Eligible) != len(second.Eligible) {
		t.Fatal("fallback filtering must be deterministic")
	}
	for i := range first.Eligible {
		if first.Eligible[i].Model.ID != second.Eligible[i].Model.ID {
			t.Fatal("fallback eligible order/content changed")
		}
	}
}

func TestExplicitIDBypassesPoolButKeepsPolicy(t *testing.T) {
	in, ids := buildFilter(t)
	// Explicit pick of a paid model: not in pool, still access-checked.
	in.ExplicitID = ids["paid"]
	in.Reqs = model.RequestRequirements{Streaming: true}
	res := Filter(in)
	if len(res.Eligible) != 0 {
		t.Fatal("explicit id must still respect paid policy")
	}
	in.AllowPaid = true
	res = Filter(in)
	if len(res.Eligible) != 1 {
		t.Fatal("explicit id routable after opt-in")
	}
}

func TestProtocolVetoApplied(t *testing.T) {
	in, _ := buildFilter(t)
	in.Reqs = model.RequestRequirements{Streaming: true}
	in.ProtocolCompatible = func(model.ProviderRoute) bool { return false }
	res := Filter(in)
	if len(res.Eligible) != 0 {
		t.Fatal("protocol veto must exclude all")
	}
}

func TestRankWithScorePrefersPerformanceAfterEligibility(t *testing.T) {
	fast := Candidate{
		Model: model.ProviderModel{ID: "opencode/fast-model"},
		Route: route("fast-route", "opencode/fast-model", model.AccessFree, nil),
	}
	slow := Candidate{
		Model: model.ProviderModel{ID: "opencode/slow-model"},
		Route: route("slow-route", "opencode/slow-model", model.AccessFree, nil),
	}
	candidates := []Candidate{fast, slow}
	got := RankWithScore(candidates, func(routeID string) (float64, bool) {
		if routeID == "fast-route" {
			return 10, true
		}
		return 100, true
	}, func(candidate Candidate) (float64, bool) {
		if candidate.Model.ID == "opencode/slow-model" {
			return 90, true
		}
		return 10, true
	})
	if len(got) != 2 || got[0].Model.ID != "opencode/slow-model" {
		t.Fatalf("score ranking = %+v, want slow model first", got)
	}
	if candidates[0].Model.ID != "opencode/fast-model" {
		t.Fatal("score ranking must not mutate input")
	}
}

func TestRankWithScoreFallsBackToTTFTWithoutScores(t *testing.T) {
	fast := Candidate{
		Model: model.ProviderModel{ID: "opencode/fast-model"},
		Route: route("fast-route", "opencode/fast-model", model.AccessFree, nil),
	}
	slow := Candidate{
		Model: model.ProviderModel{ID: "opencode/slow-model"},
		Route: route("slow-route", "opencode/slow-model", model.AccessFree, nil),
	}
	got := RankWithScore([]Candidate{slow, fast}, func(routeID string) (float64, bool) {
		return map[string]float64{"fast-route": 10, "slow-route": 100}[routeID], true
	}, nil)
	if len(got) != 2 || got[0].Model.ID != "opencode/fast-model" {
		t.Fatalf("no-score ranking = %+v, want fast model first", got)
	}
}

func mustLookup(t *testing.T, in FilterInput, name string) model.ProviderModelID {
	t.Helper()
	// names map to model suffix in buildFilter
	pmid, err := model.NewProviderModelID("opencode", map[string]string{
		"text": "text-only", "vision": "vision-model", "tools": "tools-model",
		"big": "big-context", "paid": "paid-model", "unknown": "unknown-access",
	}[name])
	if err != nil {
		t.Fatal(err)
	}
	return pmid
}
