package probe

import (
	"testing"
	"time"

	"github.com/konor123/Free-Model-Router/internal/health"
	"github.com/konor123/Free-Model-Router/internal/model"
)

func TestEligibleTargetsFiltering(t *testing.T) {
	h := health.New()
	id, _ := model.NewProviderModelID("opencode", "m")
	catalog := &model.CatalogSnapshot{Revision: 1, Models: map[model.ProviderModelID]model.ProviderModel{
		id: {ID: id, UpstreamID: "m"},
	}}
	routes := map[model.ProviderModelID][]model.ProviderRoute{
		id: {
			{ID: "pub::m", ModelID: id, Provider: "opencode", UpstreamModelID: "m", Access: model.AccessFree, Enabled: true},
			{ID: "zen::m", ModelID: id, Provider: "opencode", UpstreamModelID: "m", Access: model.AccessFreeTier, Enabled: true},
			{ID: "paid::m", ModelID: id, Provider: "opencode", UpstreamModelID: "m", Access: model.AccessPaid, Enabled: true},
			{ID: "unk::m", ModelID: id, Provider: "opencode", UpstreamModelID: "m", Access: model.AccessUnknown, Enabled: true},
			{ID: "off::m", ModelID: id, Provider: "opencode", UpstreamModelID: "m", Access: model.AccessFree, Enabled: false},
			{ID: "cool::m", ModelID: id, Provider: "opencode", UpstreamModelID: "m", Access: model.AccessFree, Enabled: true},
		},
	}
	// One route is cooling down.
	h.RouteFailure("cool::m", time.Minute)

	targets := EligibleTargets(catalog, []model.ProviderModelID{id}, routes, h)
	got := map[string]bool{}
	for _, t := range targets {
		got[string(t.Route.ID)] = true
	}
	if len(targets) != 2 || !got["pub::m"] || !got["zen::m"] {
		t.Fatalf("expected pub+zen only, got %v", got)
	}
}

func TestCooldownSkipInTargetSelection(t *testing.T) {
	h := health.New()
	id, _ := model.NewProviderModelID("opencode", "m")
	catalog := &model.CatalogSnapshot{Revision: 1, Models: map[model.ProviderModelID]model.ProviderModel{id: {ID: id, UpstreamID: "m"}}}
	routes := map[model.ProviderModelID][]model.ProviderRoute{
		id: {{ID: "pub::m", ModelID: id, Provider: "opencode", UpstreamModelID: "m", Access: model.AccessFree, Enabled: true}},
	}
	h.RouteFailure("pub::m", time.Second)
	targets := EligibleTargets(catalog, []model.ProviderModelID{id}, routes, h)
	if len(targets) != 0 {
		t.Fatal("cooling route must be skipped")
	}
}

func TestPaidNeverProbed(t *testing.T) {
	h := health.New()
	id, _ := model.NewProviderModelID("opencode", "m")
	catalog := &model.CatalogSnapshot{Revision: 1, Models: map[model.ProviderModelID]model.ProviderModel{id: {ID: id, UpstreamID: "m"}}}
	routes := map[model.ProviderModelID][]model.ProviderRoute{
		id: {{ID: "paid::m", ModelID: id, Provider: "opencode", UpstreamModelID: "m", Access: model.AccessPaid, Enabled: true}},
	}
	if targets := EligibleTargets(catalog, []model.ProviderModelID{id}, routes, h); len(targets) != 0 {
		t.Fatal("paid routes must never be auto-probed")
	}
}

func TestUnknownGenericRouteUsesExplicitProbePolicy(t *testing.T) {
	h := health.New()
	id, _ := model.NewProviderModelID("generic", "m")
	catalog := &model.CatalogSnapshot{Revision: 1, Models: map[model.ProviderModelID]model.ProviderModel{id: {ID: id, UpstreamID: "m"}}}
	allowed := true
	routes := map[model.ProviderModelID][]model.ProviderRoute{id: {{ID: "generic::m", ModelID: id, Provider: "generic", UpstreamModelID: "m", Access: model.AccessUnknown, Enabled: true, ProbeAllowed: &allowed}}}
	if targets := EligibleTargets(catalog, []model.ProviderModelID{id}, routes, h); len(targets) != 1 {
		t.Fatalf("explicitly allowed generic route targets = %#v", targets)
	}
	allowed = false
	if targets := EligibleTargets(catalog, []model.ProviderModelID{id}, routes, h); len(targets) != 0 {
		t.Fatalf("probe-disabled generic route targets = %#v", targets)
	}
}
