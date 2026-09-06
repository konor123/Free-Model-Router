package gateway

import (
	"errors"
	"time"

	"github.com/konor123/Free-Model-Router/internal/matcher"
	"github.com/konor123/Free-Model-Router/internal/model"
	"github.com/konor123/Free-Model-Router/internal/router"
	"github.com/konor123/Free-Model-Router/internal/scoring"
)

// SetBenchmarkSnapshot installs a validated benchmark snapshot for runtime
// candidate ranking. Callers may pass the result of scoring.Cache.Resolve;
// an empty snapshot remains the neutral, latency-only routing mode.
func (g *Gateway) SetBenchmarkSnapshot(snapshot scoring.Snapshot) error {
	if g == nil {
		return errors.New("gateway must not be nil")
	}
	if err := snapshot.Validate(); err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.benchmarkSnapshot = snapshot.Clone()
	g.rebuildBenchmarkBindingsLocked()
	return nil
}

// ResolveBenchmarkSnapshot refreshes benchmark data through the cache's
// live-then-last-known-good policy and installs the resulting snapshot into
// runtime ranking. When neither source exists, ranking returns to TTFT-only
// behavior instead of retaining an obsolete score snapshot.
func (g *Gateway) ResolveBenchmarkSnapshot(cache *scoring.Cache, fetch func() (scoring.Snapshot, error), now time.Time) scoring.Resolution {
	resolution := cache.Resolve(fetch, now)
	switch resolution.Source {
	case scoring.SourceLive, scoring.SourceCache:
		if err := g.SetBenchmarkSnapshot(resolution.Snapshot); err != nil {
			resolution.CacheError = err
		}
	default:
		g.clearBenchmarkSnapshot()
	}
	return resolution
}

func (g *Gateway) clearBenchmarkSnapshot() {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.benchmarkSnapshot = scoring.Snapshot{}
	g.benchmarkBindings = make(map[model.ProviderModelID]matcher.BenchmarkBinding)
}

func (g *Gateway) rebuildBenchmarkBindingsLocked() {
	bindings := make(map[model.ProviderModelID]matcher.BenchmarkBinding)
	if g.catalog == nil || len(g.benchmarkSnapshot.Benchmarks) == 0 {
		g.benchmarkBindings = bindings
		return
	}
	for id, providerModel := range g.catalog.Models {
		binding := matcher.Match(providerModel, g.benchmarkSnapshot.Benchmarks)
		if binding.MatchMethod != matcher.MatchNone {
			bindings[id] = binding
		}
	}
	g.benchmarkBindings = bindings
}

func (g *Gateway) rankCandidates(candidates []router.Candidate) []router.Candidate {
	if !g.hasPerformanceData(candidates) {
		return router.Rank(candidates, g.latency.EffectiveTTFT)
	}

	latencies := make([]float64, 0, len(candidates))
	for _, candidate := range candidates {
		if ms, ok := g.latency.EffectiveTTFT(string(candidate.Route.ID)); ok {
			latencies = append(latencies, ms)
		}
	}
	score := func(candidate router.Candidate) (float64, bool) {
		latencyScore := scoring.UnknownScore
		if ms, ok := g.latency.EffectiveTTFT(string(candidate.Route.ID)); ok {
			latencyScore = scoring.Percentile(ms, latencies, false)
		}
		binding := g.benchmarkBindings[candidate.Model.ID]
		return scoring.ScoreBinding(g.benchmarkSnapshot, binding, latencyScore).RoutingScore, true
	}
	return router.RankWithScore(candidates, g.latency.EffectiveTTFT, score)
}

func (g *Gateway) hasPerformanceData(candidates []router.Candidate) bool {
	for _, candidate := range candidates {
		binding, ok := g.benchmarkBindings[candidate.Model.ID]
		if !ok {
			continue
		}
		metrics, ok := g.benchmarkSnapshot.Models[binding.SourceModelID]
		if ok && scoring.HasPerformanceData(metrics) {
			return true
		}
	}
	return false
}

func (g *Gateway) rankedUnique(candidates []router.Candidate, existing []router.Candidate) []router.Candidate {
	seen := make(map[model.RouteID]bool, len(existing)+len(candidates))
	for _, candidate := range existing {
		seen[candidate.Route.ID] = true
	}
	result := make([]router.Candidate, 0, len(candidates))
	for _, candidate := range g.rankCandidates(candidates) {
		if seen[candidate.Route.ID] {
			continue
		}
		seen[candidate.Route.ID] = true
		result = append(result, candidate)
	}
	return result
}
