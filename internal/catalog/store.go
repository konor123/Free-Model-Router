package catalog

import (
	"fmt"
	"sync"
	"time"

	"github.com/konor123/Free-Model-Router/internal/model"
)

// Store holds the authoritative pool state behind a lock.
// Reads return deep copies; writes run under the write lock so gateway
// traffic and UI edits are serialized safely (atomic swap semantics).
type Store struct {
	mu               sync.RWMutex
	pool             *PoolState
	catalog          *model.CatalogSnapshot
	snapshotRevision model.SnapshotRevision
}

// NewStore builds a Store with the given pool state.
func NewStore(p *PoolState) *Store {
	if p == nil {
		p = NewPoolState(nil)
	}
	return &Store{pool: p}
}

// CommitSnapshot stores a defensive copy of a discovered catalog and assigns
// the next process-local monotonic revision. Provider wall-clock timestamps
// are intentionally not trusted as concurrency revisions.
func (s *Store) CommitSnapshot(snap *model.CatalogSnapshot) (*model.CatalogSnapshot, error) {
	if snap == nil {
		return nil, fmt.Errorf("nil catalog snapshot")
	}
	if err := snap.Validate(); err != nil {
		return nil, fmt.Errorf("catalog snapshot: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshotRevision++
	committed := snap.Clone()
	committed.Revision = s.snapshotRevision
	if committed.CreatedAt.IsZero() {
		committed.CreatedAt = time.Now().UTC()
	}
	s.catalog = committed
	return committed.Clone(), nil
}

// CommitCatalogSnapshot is a descriptive alias for CommitSnapshot.
func (s *Store) CommitCatalogSnapshot(snap *model.CatalogSnapshot) (*model.CatalogSnapshot, error) {
	return s.CommitSnapshot(snap)
}

// CatalogSnapshot returns the latest committed catalog view.
func (s *Store) CatalogSnapshot() *model.CatalogSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.catalog.Clone()
}

// Snapshot returns a deep copy of the current pool config (atomic read).
func (s *Store) Snapshot() *ModelPoolConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.pool.Config.Clone()
}

// StateSnapshot returns a defensive copy for staging reconciliation.
func (s *Store) StateSnapshot() *PoolState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.pool.Clone()
}

// ReplaceState publishes a previously staged pool state.
func (s *Store) ReplaceState(pool *PoolState) {
	if pool == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pool = pool.Clone()
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
