package catalog

import (
	"sync"

	"github.com/konor123/Free-Model-Router/internal/model"
)

// Store holds the authoritative pool state behind a lock.
// Reads return deep copies; writes run under the write lock so gateway
// traffic and UI edits are serialized safely (atomic swap semantics).
type Store struct {
	mu   sync.RWMutex
	pool *PoolState
}

// NewStore builds a Store with the given pool state.
func NewStore(p *PoolState) *Store {
	return &Store{pool: p}
}

// Snapshot returns a deep copy of the current pool config (atomic read).
func (s *Store) Snapshot() *ModelPoolConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.pool.Config.Clone()
}

// Mutate applies a mutation callback under the write lock.
// The callback receives the live pool state and must not retain it.
func (s *Store) Mutate(fn func(p *PoolState)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s.pool)
}

// Select adds ids and switches to Manual mode.
func (s *Store) Select(ids []model.ProviderModelID) {
	s.Mutate(func(p *PoolState) { p.Select(ids) })
}

// Deselect removes ids and switches to Manual mode.
func (s *Store) Deselect(ids []model.ProviderModelID) {
	s.Mutate(func(p *PoolState) { p.Deselect(ids) })
}

// SelectAll switches to Automatic mode and immediately applies the current
// route eligibility view.
func (s *Store) SelectAll(catalog *model.CatalogSnapshot, routes map[model.ProviderModelID][]model.ProviderRoute) {
	s.Mutate(func(p *PoolState) { p.SelectAll(catalog, routes) })
}

// ClearAll empties the pool.
func (s *Store) ClearAll() {
	s.Mutate(func(p *PoolState) { p.ClearAll() })
}
