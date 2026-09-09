package gateway

import (
	"errors"
	"fmt"
	"sort"

	"github.com/konor123/Free-Model-Router/internal/catalog"
	"github.com/konor123/Free-Model-Router/internal/health"
	"github.com/konor123/Free-Model-Router/internal/model"
	"github.com/konor123/Free-Model-Router/internal/scoring"
)

var (
	// ErrPoolRevisionConflict means a control-plane write used a stale pool
	// revision and was rejected without changing state.
	ErrPoolRevisionConflict = errors.New("model pool revision conflict")
	// ErrControlModelNotFound means a requested model is absent from the current
	// catalog or cannot be used as a pinned model.
	ErrControlModelNotFound = errors.New("control model not found")
)

// PoolMutation is the atomic PATCH operation accepted by the control API.
// Select and Deselect are independent provider-model IDs, never route IDs.
type PoolMutation struct {
	Select    []model.ProviderModelID
	Deselect  []model.ProviderModelID
	SelectAll bool
	ClearAll  bool
	Mode      *catalog.PoolMode
}

// ControlRoute is a defensive route view for the management API and desktop.
// It contains no credentials, request content, or secret values.
type ControlRoute struct {
	ID                   model.RouteID         `json:"id"`
	ModelID              model.ProviderModelID `json:"modelId"`
	Provider             string                `json:"provider"`
	UpstreamModelID      string                `json:"upstreamModelId"`
	CredentialID         string                `json:"credentialId,omitempty"`
	Access               model.AccessClass     `json:"access"`
	Enabled              bool                  `json:"enabled"`
	Capabilities         model.Capabilities    `json:"capabilities"`
	Health               health.RouteSnapshot  `json:"health"`
	TTFTMs               float64               `json:"ttftMs,omitempty"`
	TTFTKnown            bool                  `json:"ttftKnown"`
	Performance          float64               `json:"performance,omitempty"`
	PerformanceKnown     bool                  `json:"performanceKnown"`
	EffectivePerformance float64               `json:"effectivePerformance,omitempty"`
	Confidence           float64               `json:"confidence,omitempty"`
	LatencyScore         float64               `json:"latencyScore,omitempty"`
	RoutingScore         float64               `json:"routingScore,omitempty"`
	RoutingScoreKnown    bool                  `json:"routingScoreKnown"`
}

// ControlModel is a defensive model view with all route variants.
type ControlModel struct {
	Model             model.ProviderModel `json:"model"`
	Selected          bool                `json:"selected"`
	Pinned            bool                `json:"pinned"`
	RoutingScore      float64             `json:"routingScore,omitempty"`
	RoutingScoreKnown bool                `json:"routingScoreKnown"`
	Routes            []ControlRoute      `json:"routes"`
}

// ControlProvider summarizes provider participation in the current catalog.
type ControlProvider struct {
	ID      string `json:"id"`
	Models  int    `json:"models"`
	Routes  int    `json:"routes"`
	Enabled bool   `json:"enabled"`
}

// ControlSnapshot is the read-only state boundary consumed by control API and
// desktop clients.
type ControlSnapshot struct {
	CatalogRevision model.SnapshotRevision  `json:"catalogRevision"`
	Pool            catalog.ModelPoolConfig `json:"pool"`
	PinnedModel     model.ProviderModelID   `json:"pinnedModel,omitempty"`
	Models          []ControlModel          `json:"models"`
	Providers       []ControlProvider       `json:"providers"`
}

// ControlSnapshot returns a fully defensive snapshot. Callers may freely sort
// or mutate the result without racing gateway state.
func (g *Gateway) ControlSnapshot() ControlSnapshot {
	if g == nil {
		return ControlSnapshot{}
	}
	g.mu.RLock()
	defer g.mu.RUnlock()

	out := ControlSnapshot{PinnedModel: g.pinnedModel}
	if g.pool != nil {
		if pool := g.pool.Snapshot(); pool != nil {
			out.Pool = *pool
			out.Pool.SelectedProviderModelIDs = append([]model.ProviderModelID(nil), pool.SelectedProviderModelIDs...)
		}
	}
	if g.catalog == nil {
		return out
	}
	out.CatalogRevision = g.catalog.Revision
	selected := make(map[model.ProviderModelID]bool, len(out.Pool.SelectedProviderModelIDs))
	for _, id := range out.Pool.SelectedProviderModelIDs {
		selected[id] = true
	}
	ids := make([]model.ProviderModelID, 0, len(g.catalog.Models))
	for id := range g.catalog.Models {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return string(ids[i]) < string(ids[j]) })
	providers := map[string]*ControlProvider{}
	latencyPopulation := make([]float64, 0)
	for _, routes := range g.routes {
		for _, route := range routes {
			if g.latency == nil {
				continue
			}
			if value, known := g.latency.EffectiveTTFT(string(route.ID)); known {
				latencyPopulation = append(latencyPopulation, value)
			}
		}
	}
	for _, id := range ids {
		pm := g.catalog.Models[id]
		view := ControlModel{Model: pm, Selected: selected[id], Pinned: id == g.pinnedModel}
		for _, route := range g.routes[id] {
			caps := route.EffectiveCapabilities(pm.Base)
			ttft, known := 0.0, false
			if g.latency != nil {
				ttft, known = g.latency.EffectiveTTFT(string(route.ID))
			}
			routeHealth := health.RouteSnapshot{}
			if g.health != nil {
				routeHealth = g.health.Snapshot(string(route.ID))
			}
			latencyScore := scoring.UnknownScore
			if known {
				latencyScore = scoring.Percentile(ttft, latencyPopulation, false)
			}
			routeScore := scoring.ScoreBinding(g.benchmarkSnapshot, g.benchmarkBindings[id], latencyScore)
			if !view.RoutingScoreKnown || routeScore.RoutingScore > view.RoutingScore {
				view.RoutingScore = routeScore.RoutingScore
				view.RoutingScoreKnown = routeScore.HasPerformanceData || known
			}
			view.Routes = append(view.Routes, ControlRoute{
				ID:                   route.ID,
				ModelID:              route.ModelID,
				Provider:             route.Provider,
				UpstreamModelID:      route.UpstreamModelID,
				CredentialID:         route.CredentialID,
				Access:               route.EffectiveAccess(),
				Enabled:              route.Enabled,
				Capabilities:         caps,
				Health:               routeHealth,
				TTFTMs:               ttft,
				TTFTKnown:            known,
				Performance:          routeScore.Performance,
				PerformanceKnown:     routeScore.HasPerformanceData,
				EffectivePerformance: routeScore.EffectivePerformance,
				Confidence:           routeScore.Confidence,
				LatencyScore:         routeScore.Latency,
				RoutingScore:         routeScore.RoutingScore,
				RoutingScoreKnown:    routeScore.HasPerformanceData || known,
			})
			providerView := providers[route.Provider]
			if providerView == nil {
				providerView = &ControlProvider{ID: route.Provider}
				providers[route.Provider] = providerView
			}
			providerView.Routes++
			if route.Enabled {
				providerView.Enabled = true
			}
		}
		out.Models = append(out.Models, view)
	}
	for _, providerView := range providers {
		out.Providers = append(out.Providers, *providerView)
	}
	for i := range out.Providers {
		for _, modelView := range out.Models {
			seen := false
			for _, route := range modelView.Routes {
				if route.Provider == out.Providers[i].ID {
					seen = true
					break
				}
			}
			if seen {
				out.Providers[i].Models++
			}
		}
	}
	sort.Slice(out.Providers, func(i, j int) bool { return out.Providers[i].ID < out.Providers[j].ID })
	return out
}

// UpdateModelPool applies one atomic optimistic PATCH operation.
func (g *Gateway) UpdateModelPool(expectedRevision int64, mutation PoolMutation) (catalog.ModelPoolConfig, error) {
	if g == nil || g.pool == nil {
		return catalog.ModelPoolConfig{}, errors.New("gateway pool is unavailable")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	current := g.pool.Snapshot()
	if current == nil || current.Revision != expectedRevision {
		return catalog.ModelPoolConfig{}, ErrPoolRevisionConflict
	}
	if mutation.SelectAll && mutation.ClearAll {
		return catalog.ModelPoolConfig{}, errors.New("selectAll and clearAll are mutually exclusive")
	}
	if (mutation.SelectAll || mutation.ClearAll) && (len(mutation.Select) > 0 || len(mutation.Deselect) > 0) {
		return catalog.ModelPoolConfig{}, errors.New("bulk and item pool operations cannot be combined")
	}
	var updateErr error
	g.pool.Mutate(func(p *catalog.PoolState) {
		switch {
		case mutation.SelectAll:
			p.SelectAll(g.catalog, g.routes)
		case mutation.ClearAll:
			p.ClearAll()
		default:
			selected := append([]model.ProviderModelID(nil), p.Config.SelectedProviderModelIDs...)
			selected = addUniqueIDs(selected, mutation.Select)
			selected = removeIDs(selected, mutation.Deselect)
			mode := p.Config.Mode
			if len(mutation.Select) > 0 || len(mutation.Deselect) > 0 {
				mode = catalog.ModeManual
			}
			if mutation.Mode != nil {
				mode = *mutation.Mode
			}
			updateErr = p.Replace(selected, mode)
		}
	})
	if updateErr != nil {
		return catalog.ModelPoolConfig{}, updateErr
	}
	if g.pinnedModel != "" && !g.pool.Snapshot().Contains(g.pinnedModel) {
		g.pinnedModel = ""
	}
	return *g.pool.Snapshot(), nil
}

// ReplaceModelPool replaces the full selection using one optimistic revision.
func (g *Gateway) ReplaceModelPool(expectedRevision int64, selected []model.ProviderModelID, mode catalog.PoolMode) (catalog.ModelPoolConfig, error) {
	if g == nil || g.pool == nil {
		return catalog.ModelPoolConfig{}, errors.New("gateway pool is unavailable")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	current := g.pool.Snapshot()
	if current == nil || current.Revision != expectedRevision {
		return catalog.ModelPoolConfig{}, ErrPoolRevisionConflict
	}
	var updateErr error
	g.pool.Mutate(func(p *catalog.PoolState) { updateErr = p.Replace(selected, mode) })
	if updateErr != nil {
		return catalog.ModelPoolConfig{}, updateErr
	}
	if g.pinnedModel != "" && !g.pool.Snapshot().Contains(g.pinnedModel) {
		g.pinnedModel = ""
	}
	return *g.pool.Snapshot(), nil
}

// PinModel sets the preferred model after validating catalog and pool membership.
func (g *Gateway) PinModel(expectedRevision int64, id model.ProviderModelID) (catalog.ModelPoolConfig, error) {
	if g == nil || g.pool == nil {
		return catalog.ModelPoolConfig{}, errors.New("gateway pool is unavailable")
	}
	if _, _, err := id.Parse(); err != nil {
		return catalog.ModelPoolConfig{}, fmt.Errorf("%w: %v", ErrControlModelNotFound, err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	current := g.pool.Snapshot()
	if current == nil || current.Revision != expectedRevision {
		return catalog.ModelPoolConfig{}, ErrPoolRevisionConflict
	}
	if g.catalog == nil {
		return catalog.ModelPoolConfig{}, ErrControlModelNotFound
	}
	if _, ok := g.catalog.Models[id]; !ok || !current.Contains(id) {
		return catalog.ModelPoolConfig{}, ErrControlModelNotFound
	}
	g.pinnedModel = id
	g.pool.Mutate(func(p *catalog.PoolState) { p.Config.Revision++ })
	return *g.pool.Snapshot(), nil
}

// AutoSelect clears an explicit pin and returns to automatic pool selection.
func (g *Gateway) AutoSelect(expectedRevision int64) (catalog.ModelPoolConfig, error) {
	if g == nil || g.pool == nil {
		return catalog.ModelPoolConfig{}, errors.New("gateway pool is unavailable")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	current := g.pool.Snapshot()
	if current == nil || current.Revision != expectedRevision {
		return catalog.ModelPoolConfig{}, ErrPoolRevisionConflict
	}
	g.pinnedModel = ""
	var updateErr error
	g.pool.Mutate(func(p *catalog.PoolState) {
		if g.catalog != nil {
			p.SelectAll(g.catalog, g.routes)
		} else {
			updateErr = p.Replace(p.Config.SelectedProviderModelIDs, catalog.ModeAutomatic)
		}
	})
	if updateErr != nil {
		return catalog.ModelPoolConfig{}, updateErr
	}
	return *g.pool.Snapshot(), nil
}

// RestoreControlState applies persisted pool and pin state after discovery.
func (g *Gateway) RestoreControlState(selected []model.ProviderModelID, mode string, pinned model.ProviderModelID) error {
	if g == nil || g.pool == nil {
		return errors.New("gateway pool is unavailable")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(selected) > 0 || mode == string(catalog.ModeManual) {
		poolMode := catalog.PoolMode(mode)
		if poolMode == "" {
			poolMode = catalog.ModeManual
		}
		var updateErr error
		g.pool.Mutate(func(p *catalog.PoolState) { updateErr = p.Replace(selected, poolMode) })
		if updateErr != nil {
			return updateErr
		}
	}
	if pinned != "" {
		pool := g.pool.Snapshot()
		if g.catalog == nil || pool == nil || !pool.Contains(pinned) {
			return ErrControlModelNotFound
		}
		if _, ok := g.catalog.Models[pinned]; !ok {
			return ErrControlModelNotFound
		}
		g.pinnedModel = pinned
	}
	return nil
}

func addUniqueIDs(base, additions []model.ProviderModelID) []model.ProviderModelID {
	seen := make(map[model.ProviderModelID]bool, len(base)+len(additions))
	for _, id := range base {
		seen[id] = true
	}
	for _, id := range additions {
		if id != "" && !seen[id] {
			base = append(base, id)
			seen[id] = true
		}
	}
	return base
}

func removeIDs(base, removals []model.ProviderModelID) []model.ProviderModelID {
	if len(removals) == 0 {
		return base
	}
	remove := make(map[model.ProviderModelID]bool, len(removals))
	for _, id := range removals {
		remove[id] = true
	}
	kept := base[:0]
	for _, id := range base {
		if !remove[id] {
			kept = append(kept, id)
		}
	}
	return kept
}
