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
	mu  sync.Mutex
	val float64
	n   int // number of samples folded in
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
	ProbeTTFT          *EWMA // from probe requests
	RequestTTFT        *EWMA // from real client requests
	RequestTotal       *EWMA // total latency of real requests
	mu                 sync.RWMutex
	LastProbeAt        time.Time
	LastRequestAt      time.Time
	LastProbeOutcome   string
	LastProbeAttemptAt time.Time
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
	if ttftMs > UnknownMs {
		s := r.For(routeID)
		s.mu.Lock()
		s.ProbeTTFT.Add(ttftMs)
		s.LastProbeAt = time.Now()
		s.LastProbeOutcome = "success"
		s.mu.Unlock()
	}
}

// AgeProbe shifts the probe timestamp backward for tests that need an
// expired sample without waiting on wall-clock time.
func (r *Registry) AgeProbe(routeID string, delta time.Duration) {
	s := r.For(routeID)
	s.mu.Lock()
	if !s.LastProbeAt.IsZero() {
		s.LastProbeAt = s.LastProbeAt.Add(-delta)
	}
	s.mu.Unlock()
}

// BeginProbe records an attempt without changing the successful sample.
func (r *Registry) BeginProbe(routeID string) {
	s := r.For(routeID)
	s.mu.Lock()
	s.LastProbeAttemptAt = time.Now()
	s.LastProbeOutcome = "probing"
	s.mu.Unlock()
}

// MarkProbeFailure records why the latest probe produced no sample. It never
// refreshes the previous success timestamp, so fresh-success semantics are
// preserved for routing while diagnostics retain the failure reason.
func (r *Registry) MarkProbeFailure(routeID, outcome string) {
	s := r.For(routeID)
	s.mu.Lock()
	s.LastProbeOutcome = outcome
	s.mu.Unlock()
}

// RecordRequest records a real request's TTFT and total latency.
func (r *Registry) RecordRequest(routeID string, ttftMs, totalMs float64) {
	s := r.For(routeID)
	if ttftMs > UnknownMs {
		s.mu.Lock()
		s.RequestTTFT.Add(ttftMs)
		s.LastRequestAt = time.Now()
		s.mu.Unlock()
	}
	if totalMs > UnknownMs {
		s.RequestTotal.Add(totalMs)
	}
}

// FreshTTFT prefers a fresh controlled probe, then a fresh real request.
// Older samples remain available through EffectiveTTFT for diagnostics but do
// not participate in routing decisions.
func (r *Registry) FreshTTFT(routeID string, now time.Time, maxAge time.Duration) (float64, bool) {
	r.mu.RLock()
	s, exists := r.routes[routeID]
	r.mu.RUnlock()
	if !exists {
		return 0, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	probeAt, requestAt := s.LastProbeAt, s.LastRequestAt
	if maxAge > 0 && !probeAt.IsZero() && now.Sub(probeAt) <= maxAge {
		if value, ok := s.ProbeTTFT.Value(); ok {
			return value, true
		}
	}
	if maxAge > 0 && !requestAt.IsZero() && now.Sub(requestAt) <= maxAge {
		return s.RequestTTFT.Value()
	}
	return 0, false
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

// SampleView is a read-only latency view separating the fresh routing value
// from the last known measurement for display.
type SampleView struct {
	Known      bool      // any measurement exists (fresh or historical)
	Fresh      bool      // measurement is within maxAge for routing
	ValueMs    float64   // the displayed/routing value
	Source     string    // "probe" or "request"
	MeasuredAt time.Time // timestamp of the reported value
	// ProbeOutcome is "success" after a measured sample, a diagnostic outcome
	// after a failed attempt, and empty when never probed.
	ProbeOutcome   string
	ProbeAttemptAt time.Time
}

// Snapshot returns the best available view for a route: fresh probe, fresh
// request, then the historical value for display. Failed attempts never
// refresh a previous success timestamp.
func (r *Registry) Snapshot(routeID string, now time.Time, maxAge time.Duration) SampleView {
	r.mu.RLock()
	s, exists := r.routes[routeID]
	r.mu.RUnlock()
	if !exists {
		return SampleView{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	probe, probeKnown := s.ProbeTTFT.Value()
	request, requestKnown := s.RequestTTFT.Value()
	fresh := func(at time.Time) bool { return maxAge > 0 && !at.IsZero() && now.Sub(at) <= maxAge }
	view := SampleView{ProbeOutcome: s.LastProbeOutcome, ProbeAttemptAt: s.LastProbeAttemptAt}
	switch {
	case probeKnown && fresh(s.LastProbeAt):
		view.Known, view.Fresh, view.ValueMs, view.Source, view.MeasuredAt = true, true, probe, "probe", s.LastProbeAt
	case requestKnown && fresh(s.LastRequestAt):
		view.Known, view.Fresh, view.ValueMs, view.Source, view.MeasuredAt = true, true, request, "request", s.LastRequestAt
	case requestKnown && (!probeKnown || s.LastRequestAt.After(s.LastProbeAt)):
		view.Known, view.ValueMs, view.Source, view.MeasuredAt = true, request, "request", s.LastRequestAt
	case probeKnown:
		view.Known, view.ValueMs, view.Source, view.MeasuredAt = true, probe, "probe", s.LastProbeAt
	}
	return view
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

// Elapsed returns the duration since the timer started.
func (t *Timer) Elapsed() time.Duration { return time.Since(t.start) }

// Recorded reports whether TTFT was captured.
func (t *Timer) Recorded() bool { return t.recorded }
