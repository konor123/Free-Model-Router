// Package health tracks per-route health state: cooldown, quota exhaustion,
// and provider concurrency caps (PLAN_V7 §10).
package health

import (
	"sync"
	"time"
)

// DefaultCooldown is the base cooldown applied after a routable failure.
const DefaultCooldown = 30 * time.Second

// Manager owns route health states and provider concurrency slots.
type Manager struct {
	mu       sync.RWMutex
	routes   map[string]*RouteSnapshot
	provCap  map[string]int
	inFlight map[string]int
}

// RouteSnapshot is a read-only view of one route's health.
type RouteSnapshot struct {
	CoolingUntil        time.Time `json:"coolingUntil"`
	QuotaExhausted      bool      `json:"quotaExhausted"`
	ConsecutiveFailures int       `json:"consecutiveFailures"`
}

// New builds a Manager.
func New() *Manager {
	return &Manager{
		routes:   map[string]*RouteSnapshot{},
		provCap:  map[string]int{},
		inFlight: map[string]int{},
	}
}

// SetProviderCap registers a max concurrent request cap for a provider (0 = unlimited).
func (m *Manager) SetProviderCap(provider string, n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.provCap[provider] = n
}

// ProviderCap returns the configured cap.
func (m *Manager) ProviderCap(provider string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.provCap[provider]
}

// AcquireSlot blocks until a concurrency slot is available under the provider
// cap. A zero cap means unlimited. Always pair with ReleaseSlot.
func (m *Manager) AcquireSlot(provider string) bool {
	for {
		cap := m.ProviderCap(provider)
		if cap <= 0 {
			return true
		}
		m.mu.Lock()
		if m.inFlight[provider] < cap {
			m.inFlight[provider]++
			m.mu.Unlock()
			return true
		}
		m.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
}

// ReleaseSlot frees a provider concurrency slot.
func (m *Manager) ReleaseSlot(provider string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.inFlight[provider] > 0 {
		m.inFlight[provider]--
	}
}

// RouteFailure records a failure and starts a cooldown window.
func (m *Manager) RouteFailure(routeID string, cooldown time.Duration) {
	if cooldown <= 0 {
		cooldown = DefaultCooldown
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.stateLocked(routeID)
	s.ConsecutiveFailures++
	s.CoolingUntil = time.Now().Add(cooldown)
}

// RouteSuccess resets the failure counter and clears cooldown.
func (m *Manager) RouteSuccess(routeID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.stateLocked(routeID)
	s.ConsecutiveFailures = 0
	s.CoolingUntil = time.Time{}
}

// MarkQuotaExhausted flags a route until ResetQuota.
func (m *Manager) MarkQuotaExhausted(routeID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stateLocked(routeID).QuotaExhausted = true
}

// ResetQuota clears quota exhaustion.
func (m *Manager) ResetQuota(routeID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stateLocked(routeID).QuotaExhausted = false
}

// Available reports whether the route may be probed or routed now.
func (m *Manager) Available(routeID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.routes[routeID]
	if !ok {
		return true
	}
	if s.QuotaExhausted {
		return false
	}
	return s.CoolingUntil.IsZero() || time.Now().After(s.CoolingUntil)
}

// Snapshot returns a copy of the route's health state.
func (m *Manager) Snapshot(routeID string) RouteSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.routes[routeID]
	if !ok {
		return RouteSnapshot{}
	}
	return *s
}

// stateLocked returns or creates the state; caller must hold write lock.
func (m *Manager) stateLocked(routeID string) *RouteSnapshot {
	s, ok := m.routes[routeID]
	if !ok {
		s = &RouteSnapshot{}
		m.routes[routeID] = s
	}
	return s
}
