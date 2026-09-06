package catalog

import (
	"fmt"

	"github.com/konor123/Free-Model-Router/internal/model"
)

// Reconciler merges a fresh catalog snapshot into pool state without
// destroying user intent (PLAN_V7 §7).
type Reconciler struct{}

// NewReconciler builds a Reconciler.
func NewReconciler() *Reconciler { return &Reconciler{} }

// ReconcileResult summarizes what a reconciliation pass did.
type ReconcileResult struct {
	// Added models included into the pool.
	Added []model.ProviderModelID
	// Tombstoned models kept in selection but absent from the catalog.
	Tombstoned []model.ProviderModelID
	// Restored models that returned from tombstone.
	Restored []model.ProviderModelID
	// AccessChanged models whose access class changed.
	AccessChanged []model.ProviderModelID
	// Snapshot is the new immutable snapshot (same reference as input).
	Snapshot *model.CatalogSnapshot
}

// Apply merges the snapshot into the pool.
//
// Rules (PLAN_V7 §7):
//   - selected model disappears → tombstone (keep selection)
//   - model returns → restore previous selection
//   - access changes (Free → Paid) → keep selection; access policy excludes from auto routing
//   - revision conflict → error
func (r *Reconciler) Reconcile(p *PoolState, snap *model.CatalogSnapshot, routes map[model.ProviderModelID][]model.ProviderRoute) (*ReconcileResult, error) {
	if p == nil || p.Config == nil {
		return nil, fmt.Errorf("nil pool state")
	}
	if snap == nil {
		return nil, fmt.Errorf("nil snapshot")
	}
	if err := snap.Validate(); err != nil {
		return nil, fmt.Errorf("snapshot: %w", err)
	}

	res := &ReconcileResult{Snapshot: snap}

	// Tombstone handling: selected but missing from new catalog.
	for _, id := range p.Config.SelectedProviderModelIDs {
		if _, ok := snap.Models[id]; !ok {
			if !p.tombstones[id] {
				p.tombstones[id] = true
				res.Tombstoned = append(res.Tombstoned, id)
			}
			continue
		}
		if p.tombstones[id] {
			delete(p.tombstones, id)
			res.Restored = append(res.Restored, id)
		}
	}

	// Access change detection and automatic-mode inclusion.
	for id := range snap.Models {
		// Automatic mode: include models with an enabled free/free-tier route.
		if p.Config.Mode == ModeAutomatic && hasAutoRoutableRoute(routes[id]) && !p.Config.Contains(id) {
			p.Config.SelectedProviderModelIDs = append(p.Config.SelectedProviderModelIDs, id)
			res.Added = append(res.Added, id)
		}
		_ = id
	}

	// Access changes only tracked for selected models present in both old and new state.
	// The caller compares previous snapshot access externally; here we surface
	// current access so callers can diff if they retain prior snapshots.
	p.Config.Revision++
	return res, nil
}

// DetectAccessChanges compares two snapshots for the given selected ids and
// reports ids whose access class differs. Returns error on revision regression.
func (r *Reconciler) DetectAccessChanges(prev, next *model.CatalogSnapshot, prevRoutes, nextRoutes map[model.ProviderModelID][]model.ProviderRoute, selected []model.ProviderModelID) ([]model.ProviderModelID, error) {
	if prev != nil && next != nil && next.Revision < prev.Revision {
		return nil, fmt.Errorf("revision conflict: next %d < prev %d", next.Revision, prev.Revision)
	}
	var changed []model.ProviderModelID
	for _, id := range selected {
		_, okPrev := lookup(prev, id)
		_, okNext := lookup(next, id)
		if okPrev && okNext && routeAccessSignature(prevRoutes[id]) != routeAccessSignature(nextRoutes[id]) {
			changed = append(changed, id)
		}
	}
	return changed, nil
}

func hasAutoRoutableRoute(routes []model.ProviderRoute) bool {
	for _, route := range routes {
		if route.Enabled && route.EffectiveAccess().AutoRoutable() {
			return true
		}
	}
	return false
}

func routeAccessSignature(routes []model.ProviderRoute) string {
	var signature string
	for _, route := range routes {
		if route.Enabled {
			signature += string(route.ID) + ":" + string(route.EffectiveAccess()) + ";"
		}
	}
	return signature
}

func lookup(s *model.CatalogSnapshot, id model.ProviderModelID) (model.ProviderModel, bool) {
	if s == nil {
		return model.ProviderModel{}, false
	}
	pm, ok := s.Models[id]
	return pm, ok
}
