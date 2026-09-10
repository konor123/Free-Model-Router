package catalog

import (
	"testing"

	"github.com/konor123/Free-Model-Router/internal/model"
)

func snapWith(pms ...model.ProviderModel) *model.CatalogSnapshot {
	m := map[model.ProviderModelID]model.ProviderModel{}
	for _, pm := range pms {
		m[pm.ID] = pm
	}
	return &model.CatalogSnapshot{Revision: 1, Models: m}
}

func pm(id string) model.ProviderModel {
	pmid, _ := model.NewProviderModelID("opencode", id)
	return model.ProviderModel{ID: pmid, UpstreamID: id}
}

func routesFor(id model.ProviderModelID, access model.AccessClass, enabled bool) map[model.ProviderModelID][]model.ProviderRoute {
	return map[model.ProviderModelID][]model.ProviderRoute{
		id: {{ID: model.RouteID("opencode-public::" + string(id)), ModelID: id, Provider: "opencode", UpstreamModelID: "model", Access: access, Enabled: enabled}},
	}
}

func TestNewModelAppearsAutomaticMode(t *testing.T) {
	p := NewPoolState(&ModelPoolConfig{Mode: ModeAutomatic})
	r := NewReconciler()

	a := pm("mimo-v2.5")
	if _, err := r.Reconcile(p, snapWith(a), routesFor(a.ID, model.AccessFree, true)); err != nil {
		t.Fatal(err)
	}
	if !p.Config.Contains(a.ID) {
		t.Fatal("automatic mode should include free model")
	}

	// Another free model appears later.
	b := pm("glm-5.3-flash")
	if _, err := r.Reconcile(p, snapWith(a, b), map[model.ProviderModelID][]model.ProviderRoute{
		a.ID: routesFor(a.ID, model.AccessFree, true)[a.ID], b.ID: routesFor(b.ID, model.AccessFreeTier, true)[b.ID],
	}); err != nil {
		t.Fatal(err)
	}
	if !p.Config.Contains(b.ID) {
		t.Fatal("automatic mode should include new free-tier model")
	}

	// Paid models are not auto-included.
	c := pm("gpt-x")
	if _, err := r.Reconcile(p, snapWith(a, b, c), map[model.ProviderModelID][]model.ProviderRoute{
		a.ID: routesFor(a.ID, model.AccessFree, true)[a.ID], b.ID: routesFor(b.ID, model.AccessFreeTier, true)[b.ID], c.ID: routesFor(c.ID, model.AccessPaid, true)[c.ID],
	}); err != nil {
		t.Fatal(err)
	}
	if p.Config.Contains(c.ID) {
		t.Fatal("paid model must not be auto-included")
	}
}

func TestModelDisappearsTombstonedAndReturnsRestored(t *testing.T) {
	p := NewPoolState(&ModelPoolConfig{Mode: ModeManual})
	p.Select([]model.ProviderModelID{pm("mimo-v2.5").ID})
	a := pm("mimo-v2.5")

	// Present initially.
	if _, err := r1(p, snapWith(a), nil); err != nil {
		t.Fatal(err)
	}

	// Disappears.
	res, err := r1(p, snapWith(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tombstoned) != 1 || res.Tombstoned[0] != a.ID {
		t.Fatalf("expected tombstone, got %+v", res)
	}
	if len(p.Config.SelectedProviderModelIDs) != 1 {
		t.Fatal("selection must be preserved on tombstone")
	}

	// Returns.
	res, err = r1(p, snapWith(a), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Restored) != 1 || res.Restored[0] != a.ID {
		t.Fatalf("expected restore, got %+v", res)
	}
	if len(res.Tombstoned) != 0 {
		t.Fatalf("no tombstone expected on return, got %+v", res)
	}
}

func TestManualModeDoesNotAutoAdd(t *testing.T) {
	p := NewPoolState(&ModelPoolConfig{Mode: ModeManual})
	r := NewReconciler()
	a, b := pm("a"), pm("b")
	if _, err := r.Reconcile(p, snapWith(a, b), map[model.ProviderModelID][]model.ProviderRoute{
		a.ID: routesFor(a.ID, model.AccessFree, true)[a.ID], b.ID: routesFor(b.ID, model.AccessFree, true)[b.ID],
	}); err != nil {
		t.Fatal(err)
	}
	if len(p.Config.SelectedProviderModelIDs) != 0 {
		t.Fatalf("manual mode must not auto-add, got %v", p.Config.SelectedProviderModelIDs)
	}
}

func TestManualModeAddsExplicitGenericDefaults(t *testing.T) {
	p := NewPoolState(&ModelPoolConfig{Mode: ModeManual})
	r := NewReconciler()
	a := pm("generic")
	selected := true
	routes := routesFor(a.ID, model.AccessUnknown, true)
	route := routes[a.ID][0]
	route.DefaultSelected = &selected
	routes[a.ID] = []model.ProviderRoute{route}
	if _, err := r.Reconcile(p, snapWith(a), routes); err != nil {
		t.Fatal(err)
	}
	if !p.Config.Contains(a.ID) || p.Config.Mode != ModeManual {
		t.Fatalf("generic defaults were not selected in Manual mode: %#v", p.Config)
	}
}

func TestSelectSwitchesToManual(t *testing.T) {
	p := NewPoolState(&ModelPoolConfig{Mode: ModeAutomatic})
	p.Select([]model.ProviderModelID{pm("x").ID})
	if p.Config.Mode != ModeManual {
		t.Fatalf("expected Manual after explicit select, got %q", p.Config.Mode)
	}
}

func TestDetectAccessChanges(t *testing.T) {
	prev := snapWith(pm("mimo-v2.5"))
	next := snapWith(pm("mimo-v2.5"))
	r := NewReconciler()
	id, _ := model.NewProviderModelID("opencode", "mimo-v2.5")

	changed, err := r.DetectAccessChanges(prev, next, routesFor(id, model.AccessFree, true), routesFor(id, model.AccessPaid, true), []model.ProviderModelID{id})
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || changed[0] != id {
		t.Fatalf("expected access change, got %v", changed)
	}

	// Selection preserved despite paid change.
	if !p1(id).Config.Contains(id) {
		t.Fatal("access change must not drop selection")
	}
}

func TestRevisionConflictDetected(t *testing.T) {
	prev := &model.CatalogSnapshot{Revision: 10, Models: map[model.ProviderModelID]model.ProviderModel{}}
	next := &model.CatalogSnapshot{Revision: 5, Models: map[model.ProviderModelID]model.ProviderModel{}}
	r := NewReconciler()
	if _, err := r.DetectAccessChanges(prev, next, nil, nil, nil); err == nil {
		t.Fatal("expected revision conflict error")
	}
}

func TestClearAllEmptyPool(t *testing.T) {
	p := NewPoolState(&ModelPoolConfig{Mode: ModeAutomatic})
	p.ClearAll()
	if len(p.Config.SelectedProviderModelIDs) != 0 || p.Config.Mode != ModeManual {
		t.Fatal("clear all should empty pool and switch to manual")
	}
}

func TestStoreAssignsMonotonicSnapshotRevisionsAndCopiesModels(t *testing.T) {
	store := NewStore(nil)
	id := pm("first")
	first, err := store.CommitSnapshot(&model.CatalogSnapshot{
		Revision: 9000,
		Models:   map[model.ProviderModelID]model.ProviderModel{id.ID: id},
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision != 1 || first.CreatedAt.IsZero() {
		t.Fatalf("first committed snapshot = %+v", first)
	}

	// Mutating the provider-owned input after commit must not mutate the store.
	delete(first.Models, id.ID)
	stored := store.CatalogSnapshot()
	if stored == nil || len(stored.Models) != 1 {
		t.Fatalf("store must retain a defensive copy: %+v", stored)
	}

	second, err := store.CommitSnapshot(&model.CatalogSnapshot{
		Revision: 1, // provider revisions are ignored by the store
		Models:   map[model.ProviderModelID]model.ProviderModel{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Revision != 2 {
		t.Fatalf("second committed revision = %d, want 2", second.Revision)
	}
}

func TestSelectAllImmediatelyAddsEligibleRoutes(t *testing.T) {
	free, paid := pm("free"), pm("paid")
	p := NewPoolState(&ModelPoolConfig{Mode: ModeManual})
	p.SelectAll(snapWith(free, paid), map[model.ProviderModelID][]model.ProviderRoute{
		free.ID: routesFor(free.ID, model.AccessFreeTier, true)[free.ID],
		paid.ID: routesFor(paid.ID, model.AccessPaid, true)[paid.ID],
	})
	if !p.Config.Contains(free.ID) || p.Config.Contains(paid.ID) {
		t.Fatalf("SelectAll should include only eligible models: %v", p.Config.SelectedProviderModelIDs)
	}
}

// helpers to keep tests terse.
func r1(p *PoolState, s *model.CatalogSnapshot, routes map[model.ProviderModelID][]model.ProviderRoute) (*ReconcileResult, error) {
	return NewReconciler().Reconcile(p, s, routes)
}

func p1(id model.ProviderModelID) *PoolState {
	p := NewPoolState(&ModelPoolConfig{Mode: ModeManual})
	p.Select([]model.ProviderModelID{id})
	return p
}
