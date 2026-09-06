// Package latency maintains TTFT EWMA statistics per route (PLAN_V7 §10).
//
// TTFT = Time To First Semantic Event. HTTP response header time is never used.
package latency

import (
	"sync"
	"time"
)

// EWMA factor from PLAN_V7: 0.25 new sample / 0.75 old value.
const (
	Alpha     = 0.25
	UnknownMs = 0 // unknown samples are ignored, not folded in
)

// EWMA is an exponentially weighted moving average in milliseconds.
type EWMA struct {
	mu   sync.Mutex
	val  float64
	n    int // number of samples folded in
}

// New builds an empty EWMA.
func New() *EWMA { return &EWMA{} }

// Add folds one sample.
func (e *EWMA) Add(sampleMs float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.n == 0 {
		e.val = sampleMs
	} else {
		e.val = Alpha*sampleMs + (1-Alpha)*e.val
	}
	e.n++
}

// Value returns the current average and whether any sample exists.
func (e *EWMA) Value() (ms float64, ok bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.n == 0 {
		return 0, false
	}
	return e.val, true
}

// Samples returns the sample count.
func (e *EWMA) Samples() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.n
}

// Stats bundles the three maintained averages for one route.
type Stats struct {
	ProbeTTFT   *EWMA // from probe requests
	RequestTTFT *EWMA // from real client requests
	RequestTotal *EWMA // total latency of real requests
}

// NewStats builds empty stats.
func NewStats() *Stats {
	return &Stats{ProbeTTFT: New(), RequestTTFT: New(), RequestTotal: New()}
}

// Registry keeps per-route stats, keyed by RouteID.
type Registry struct {
	mu     sync.RWMutex
	routes map[modelRouteID]*Stats
}

// modelRouteID aliases the model type to avoid import cycles in docs.
type modelRouteID = string

// NewRegistry builds an empty registry.
func NewRegistry() *Registry {
	return &Registry{routes: map[modelRouteID]*Stats{}}
}

// For returns (creating if needed) the stats for a route.
func (r *Registry) For(routeID string) *Stats {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.routes[routeID]
	if !ok {
		s = NewStats()
		r.routes[routeID] = s
	}
	return s
}

// RecordProbe records a probe TTFT sample for the route.
func (r *Registry) RecordProbe(routeID string, ttftMs float64) {
	r.For(routeID).ProbeTTFT.Add(ttftMs)
}

// RecordRequest records a real request's TTFT and total latency.
func (r *Registry) RecordRequest(routeID string, ttftMs, totalMs float64) {
	s := r.For(routeID)
	s.RequestTTFT.Add(ttftMs)
	s.RequestTotal.Add(totalMs)
}

// EffectiveTTFT returns the best available routing estimate: probes are
// primary because they are controlled and comparable; request samples are a
// fallback when no probe is available.
func (r *Registry) EffectiveTTFT(routeID string) (ms float64, ok bool) {
	r.mu.RLock()
	s, exists := r.routes[routeID]
	r.mu.RUnlock()
	if !exists {
		return 0, false
	}
	if v, ok := s.ProbeTTFT.Value(); ok {
		return v, true
	}
	return s.RequestTTFT.Value()
}

// Timer measures TTFT against the first semantic event.
type Timer struct {
	start    time.Time
	recorded bool
	ttft     time.Duration
}

// StartTimer begins timing at upstream request start.
func Start() *Timer { return &Timer{start: time.Now()} }

// OnSemanticEvent records TTFT on the first semantic event only.
// Heartbeats/comments/empty deltas never set the timer (PLAN_V7 §10).
// Returns true if this call recorded the TTFT.
func (t *Timer) OnSemanticEvent() (time.Duration, bool) {
	if t.recorded {
		return 0, false
	}
	t.recorded = true
	return time.Since(t.start), true
}

// Recorded reports whether TTFT was captured.
func (t *Timer) Recorded() bool { return t.recorded }
