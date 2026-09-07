// Package catalog manages immutable catalog snapshots, the Model Pool,
// and reconciliation between them (PLAN_V7 §7).
//
// Key invariants:
//   - Catalog snapshots are immutable; a new snapshot is built fully then atomically swapped.
//   - Catalog refresh never destroys user selection (tombstone/restore).
//   - Pool mode: Automatic keeps free/free-tier models in sync; Manual keeps
//     only explicit selections. Direct checkbox edits switch to Manual.
package catalog

import (
	"fmt"

	"github.com/konor123/Free-Model-Router/internal/model"
)

// PoolMode controls how the Model Pool tracks the catalog.
type PoolMode string

const (
	// ModeAutomatic auto-includes new free/free-tier models.
	ModeAutomatic PoolMode = "Automatic"
	// ModeManual includes only user-selected models.
	ModeManual PoolMode = "Manual"
)

// ModelPoolConfig is the persisted pool selection (source of truth).
type ModelPoolConfig struct {
	// Revision guards optimistic concurrency between UI and gateway.
	Revision int64 `json:"revision"`

	// Mode: Automatic or Manual.
	Mode PoolMode `json:"mode"`

	// SelectedProviderModelIDs is the source of truth for pool membership.
	SelectedProviderModelIDs []model.ProviderModelID `json:"selectedProviderModelIds"`
}

// Clone returns a deep copy.
func (c *ModelPoolConfig) Clone() *ModelPoolConfig {
	out := &ModelPoolConfig{
		Revision: c.Revision,
		Mode:     c.Mode,
	}
	if c.SelectedProviderModelIDs != nil {
		out.SelectedProviderModelIDs = make([]model.ProviderModelID, len(c.SelectedProviderModelIDs))
		copy(out.SelectedProviderModelIDs, c.SelectedProviderModelIDs)
	}
	return out
}

// Validate checks basic invariants.
func (c *ModelPoolConfig) Validate() error {
	if c.Revision < 0 {
		return fmt.Errorf("negative pool revision")
	}
	switch c.Mode {
	case ModeAutomatic, ModeManual:
	default:
		return fmt.Errorf("invalid pool mode %q", c.Mode)
	}
	return nil
}

// Contains reports whether the id is selected.
func (c *ModelPoolConfig) Contains(id model.ProviderModelID) bool {
	for _, s := range c.SelectedProviderModelIDs {
		if s == id {
			return true
		}
	}
	return false
}

// PoolState combines the pool config with runtime tombstones.
type PoolState struct {
	Config *ModelPoolConfig

	// tombstones mark selected models absent from the latest catalog.
	tombstones map[model.ProviderModelID]bool
}

// Clone returns an independent pool state including runtime tombstones.
func (p *PoolState) Clone() *PoolState {
	if p == nil {
		return NewPoolState(nil)
	}
	out := NewPoolState(p.Config.Clone())
	for id, value := range p.tombstones {
		out.tombstones[id] = value
	}
	return out
}

// NewPoolState builds a pool state from a config.
func NewPoolState(cfg *ModelPoolConfig) *PoolState {
	if cfg == nil {
		cfg = &ModelPoolConfig{Mode: ModeAutomatic}
	}
	return &PoolState{Config: cfg, tombstones: map[model.ProviderModelID]bool{}}
}

// Select adds ids to the selection and switches to Manual mode.
func (p *PoolState) Select(ids []model.ProviderModelID) {
	for _, id := range ids {
		if !p.Config.Contains(id) {
			p.Config.SelectedProviderModelIDs = append(p.Config.SelectedProviderModelIDs, id)
		}
		delete(p.tombstones, id)
	}
	p.Config.Mode = ModeManual
	p.Config.Revision++
}

// Deselect removes ids and switches to Manual mode.
func (p *PoolState) Deselect(ids []model.ProviderModelID) {
	var kept []model.ProviderModelID
	for _, s := range p.Config.SelectedProviderModelIDs {
		remove := false
		for _, id := range ids {
			if s == id {
				remove = true
				break
			}
		}
		if !remove {
			kept = append(kept, s)
		}
	}
	p.Config.SelectedProviderModelIDs = kept
	p.Config.Mode = ModeManual
	p.Config.Revision++
}

// SelectAll switches to Automatic mode and immediately includes every model
// with an enabled free/free-tier route in the current snapshot.
func (p *PoolState) SelectAll(catalog *model.CatalogSnapshot, routes map[model.ProviderModelID][]model.ProviderRoute) {
	p.Config.Mode = ModeAutomatic
	if catalog != nil {
		for id := range catalog.Models {
			if !p.Config.Contains(id) && hasAutoRoutableRoute(routes[id]) {
				p.Config.SelectedProviderModelIDs = append(p.Config.SelectedProviderModelIDs, id)
			}
		}
	}
	p.Config.Revision++
	p.tombstones = map[model.ProviderModelID]bool{}
}

// ClearAll empties the selection (Manual mode, empty pool).
func (p *PoolState) ClearAll() {
	p.Config.SelectedProviderModelIDs = nil
	p.Config.Mode = ModeManual
	p.Config.Revision++
	p.tombstones = map[model.ProviderModelID]bool{}
}

// Replace atomically replaces the user-visible pool selection and mode. The
// caller is responsible for optimistic revision checking in the owning store.
// The selection is copied so callers cannot mutate pool state after the update.
func (p *PoolState) Replace(selected []model.ProviderModelID, mode PoolMode) error {
	if p == nil || p.Config == nil {
		return fmt.Errorf("nil pool state")
	}
	if mode == "" {
		mode = ModeAutomatic
	}
	if mode != ModeAutomatic && mode != ModeManual {
		return fmt.Errorf("invalid pool mode %q", mode)
	}
	p.Config.SelectedProviderModelIDs = append([]model.ProviderModelID(nil), selected...)
	p.Config.Mode = mode
	p.Config.Revision++
	p.tombstones = map[model.ProviderModelID]bool{}
	return nil
}
