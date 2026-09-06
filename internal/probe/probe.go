// Package probe implements periodic TTFT probing for eligible pool routes.
package probe

import (
	"context"
	"math/rand"
	"sync"
	"time"

	"github.com/konor123/Free-Model-Router/internal/health"
	"github.com/konor123/Free-Model-Router/internal/latency"
	"github.com/konor123/Free-Model-Router/internal/model"
	"github.com/konor123/Free-Model-Router/internal/provider"
)

// Target is one probeable route.
type Target struct {
	Route model.ProviderRoute
	Model model.ProviderModel
}

// Scheduler probes eligible routes periodically.
type Scheduler struct {
	Reg    *latency.Registry
	Health *health.Manager

	interval time.Duration
	timeout  time.Duration

	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	running bool
	runID   uint64
}

// DefaultProbeTimeout bounds both slot acquisition and one upstream probe.
const DefaultProbeTimeout = 10 * time.Second

// Snapshot is an immutable, coherent probe view loaded once per cycle.
type Snapshot struct {
	Catalog  *model.CatalogSnapshot
	Pool     []model.ProviderModelID
	Routes   map[model.ProviderModelID][]model.ProviderRoute
	Provider provider.Provider
}

// SnapshotSource supplies the current immutable probe view.
type SnapshotSource func() *Snapshot

// New builds a Scheduler.
func New(reg *latency.Registry, h *health.Manager, interval time.Duration) *Scheduler {
	return &Scheduler{Reg: reg, Health: h, interval: interval, timeout: DefaultProbeTimeout}
}

// EligibleTargets filters routes down to probeable candidates (PLAN_V7 §10):
// selected pool ∩ enabled route ∩ Free/Free-tier ∩ not cooling down.
// Paid/Unknown routes are never auto-probed.
func EligibleTargets(catalog *model.CatalogSnapshot, pool []model.ProviderModelID, routes map[model.ProviderModelID][]model.ProviderRoute, h *health.Manager) []Target {
	var out []Target
	if catalog == nil {
		return out
	}
	seen := map[model.ProviderModelID]bool{}
	for _, id := range pool {
		if seen[id] {
			continue
		}
		seen[id] = true
		pm, ok := catalog.Models[id]
		if !ok {
			continue
		}
		for _, route := range routes[id] {
			if !route.Enabled {
				continue
			}
			if !route.EffectiveAccess().AutoRoutable() {
				continue // Paid/Unknown never probed
			}
			if h != nil && !h.Available(string(route.ID)) {
				continue // cooling down or quota exhausted
			}
			out = append(out, Target{Route: route, Model: pm})
		}
	}
	return out
}

// Start begins the periodic probe loop using a fresh coherent snapshot per cycle.
func (s *Scheduler) Start(ctx context.Context, source SnapshotSource) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	ctx, s.cancel = context.WithCancel(ctx)
	s.done = make(chan struct{})
	s.runID++
	myRunID := s.runID
	done := s.done
	s.running = true
	s.mu.Unlock()

	go func() {
		defer close(done)
		s.runOnce(ctx, source)
		for {
			delay := s.jitteredInterval()
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				// Clear running only if we still own the active run.
				s.mu.Lock()
				if s.runID == myRunID {
					s.running = false
					s.cancel = nil
					s.done = nil
				}
				s.mu.Unlock()
				return
			case <-timer.C:
				s.runOnce(ctx, source)
			}
		}
	}()
}

// RunOnce executes a single probe cycle using a freshly loaded coherent view.
// The source is evaluated exactly once, so pool/catalog changes become visible
// on the next cycle without retaining stale targets.
func (s *Scheduler) RunOnce(ctx context.Context, source SnapshotSource) {
	s.runOnce(ctx, source)
}

func (s *Scheduler) runOnce(ctx context.Context, source SnapshotSource) {
	if source == nil {
		return
	}
	snap := source()
	if snap == nil || snap.Provider == nil {
		return
	}
	targets := EligibleTargets(snap.Catalog, snap.Pool, snap.Routes, s.Health)
	for _, target := range targets {
		s.probeOnce(ctx, target, snap.Provider)
	}
}

func (s *Scheduler) jitteredInterval() time.Duration {
	if s.interval <= 0 {
		return time.Millisecond
	}
	// ±10% avoids synchronized bursts while retaining predictable cadence.
	delta := s.interval / 10
	if delta == 0 {
		return s.interval
	}
	return s.interval - delta + time.Duration(rand.Int63n(int64(2*delta)+1))
}

// Stop halts the scheduler.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

// ProbeOnce sends one minimal probe request and records its TTFT.
// Uses a tiny 1-token request to keep quota consumption low.
func (s *Scheduler) ProbeOnce(ctx context.Context, t Target, prov provider.Provider) {
	s.probeOnce(ctx, t, prov)
}

func (s *Scheduler) probeOnce(ctx context.Context, t Target, prov provider.Provider) {
	probeCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	release, err := s.Health.AcquireSlot(probeCtx, t.Route.Provider)
	if err != nil {
		return
	}
	defer release()

	req := provider.NormalizedRequest{
		Messages:  []provider.Message{{Role: "user", Content: "ping"}},
		MaxTokens: 1,
		Stream:    true,
	}
	timer := latency.Start()
	stream, err := prov.ChatCompletion(probeCtx, t.Route, req)
	if err != nil {
		if ctx.Err() == nil {
			s.Health.RouteFailure(string(t.Route.ID), 0)
		}
		return
	}
	defer stream.Close()

	recorded := false
	for {
		ev, err := stream.Next(probeCtx)
		if err != nil {
			if !recorded && ctx.Err() == nil {
				s.Health.RouteFailure(string(t.Route.ID), 0)
			}
			return
		}
		if ev.Semantic() {
			if d, ok := timer.OnSemanticEvent(); ok {
				s.Reg.RecordProbe(string(t.Route.ID), float64(d.Milliseconds()))
				recorded = true
				s.Health.RouteSuccess(string(t.Route.ID))
			}
		}
		if ev.FinishReason != "" {
			if !recorded && ctx.Err() == nil {
				s.Health.RouteFailure(string(t.Route.ID), 0)
			}
			return
		}
	}
}
