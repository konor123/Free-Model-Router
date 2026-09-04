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

func pm(id, access string) model.ProviderModel {
	pmid, _ := model.NewProviderModelID("opencode", id)
	return model.ProviderModel{ID: pmid, Access: model.AccessClass(access)}
}

func TestNewModelAppearsAutomaticMode(t *testing.T) {
	p := NewPoolState(&ModelPoolConfig{Mode: ModeAutomatic})
	r := NewReconciler()

	a := pm("mimo-v2.5", "Free")
	if _, err := r.Reconcile(p, snapWith(a)); err != nil {
		t.Fatal(err)
	}
	if !p.Config.Contains(a.ID) {
		t.Fatal("automatic mode should include free model")
	}

	// Another free model appears later.
	b := pm("glm-5.3-flash", "Free-tier")
	if _, err := r.Reconcile(p, snapWith(a, b)); err != nil {
		t.Fatal(err)
	}
	if !p.Config.Contains(b.ID) {
		t.Fatal("automatic mode should include new free-tier model")
	}

	// Paid models are not auto-included.
	c := pm("gpt-x", "Paid")
	if _, err := r.Reconcile(p, snapWith(a, b, c)); err != nil {
		t.Fatal(err)
	}
	if p.Config.Contains(c.ID) {
		t.Fatal("paid model must not be auto-included")
	}
}

func TestModelDisappearsTombstonedAndReturnsRestored(t *testing.T) {
	p := NewPoolState(&ModelPoolConfig{Mode: ModeManual})
	p.Select([]model.ProviderModelID{pm("mimo-v2.5", "Free").ID})
	a := pm("mimo-v2.5", "Free")

	// Present initially.
	if _, err := r1(p, snapWith(a)); err != nil {
		t.Fatal(err)
	}

	// Disappears.
	res, err := r1(p, snapWith())
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
	res, err = r1(p, snapWith(a))
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
	if _, err := r.Reconcile(p, snapWith(pm("a", "Free"), pm("b", "Free"))); err != nil {
		t.Fatal(err)
	}
	if len(p.Config.SelectedProviderModelIDs) != 0 {
		t.Fatalf("manual mode must not auto-add, got %v", p.Config.SelectedProviderModelIDs)
	}
}

func TestSelectSwitchesToManual(t *testing.T) {
	p := NewPoolState(&ModelPoolConfig{Mode: ModeAutomatic})
	p.Select([]model.ProviderModelID{pm("x", "Free").ID})
	if p.Config.Mode != ModeManual {
		t.Fatalf("expected Manual after explicit select, got %q", p.Config.Mode)
	}
}

func TestDetectAccessChanges(t *testing.T) {
	prev := snapWith(pm("mimo-v2.5", "Free"))
	next := snapWith(pm("mimo-v2.5", "Paid"))
	r := NewReconciler()
	id, _ := model.NewProviderModelID("opencode", "mimo-v2.5")

	changed, err := r.DetectAccessChanges(prev, next, []model.ProviderModelID{id})
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
	if _, err := r.DetectAccessChanges(prev, next, nil); err == nil {
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

// helpers to keep tests terse.
func r1(p *PoolState, s *model.CatalogSnapshot) (*ReconcileResult, error) {
	return NewReconciler().Reconcile(p, s)
}

func p1(id model.ProviderModelID) *PoolState {
	p := NewPoolState(&ModelPoolConfig{Mode: ModeManual})
	p.Select([]model.ProviderModelID{id})
	return p
}
