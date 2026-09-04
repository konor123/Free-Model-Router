// Package probe implements periodic TTFT probing for eligible pool routes.
package probe

import (
	"context"
	"sync"
	"time"

	"github.com/konor123/Free-Model-Router/internal/health"
	"github.com/konor123/Free-Model-Router/internal/latency"
	"github.com/konor123/Free-Model-Router/internal/model"
	"github.com/konor123/Free-Model-Router/internal/provider"
)

// Target is one probeable route.
type Target struct {
	Route   model.ProviderRoute
	Model   model.ProviderModel
}

// Scheduler probes eligible routes periodically.
type Scheduler struct {
	Reg   *latency.Registry
	Health *health.Manager

	interval time.Duration

	mu      sync.Mutex
	cancel  context.CancelFunc
	running bool
}

// New builds a Scheduler.
func New(reg *latency.Registry, h *health.Manager, interval time.Duration) *Scheduler {
	return &Scheduler{Reg: reg, Health: h, interval: interval}
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
			if !route.Access.AutoRoutable() {
				continue // Paid/Unknown never probed
			}
			if !h.Available(string(route.ID)) {
				continue // cooling down or quota exhausted
			}
			out = append(out, Target{Route: route, Model: pm})
		}
	}
	return out
}

// Start begins the periodic probe loop.
func (s *Scheduler) Start(ctx context.Context, catalog *model.CatalogSnapshot, pool []model.ProviderModelID, routes map[model.ProviderModelID][]model.ProviderRoute, prov provider.Provider) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	ctx, s.cancel = context.WithCancel(ctx)
	s.running = true
	s.mu.Unlock()

	go func() {
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				targets := EligibleTargets(catalog, pool, routes, s.Health)
				for _, t := range targets {
					s.probeOnce(ctx, t, prov)
				}
			}
		}
	}()
}

// Stop halts the scheduler.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
	s.running = false
}

// ProbeOnce sends one minimal probe request and records its TTFT.
// Uses a tiny 1-token request to keep quota consumption low.
func (s *Scheduler) ProbeOnce(ctx context.Context, t Target, prov provider.Provider) {
	s.probeOnce(ctx, t, prov)
}

func (s *Scheduler) probeOnce(ctx context.Context, t Target, prov provider.Provider) {
	if !s.Health.AcquireSlot("opencode") {
		return
	}
	defer s.Health.ReleaseSlot("opencode")

	req := provider.NormalizedRequest{
		Messages:  []provider.Message{{Role: "user", Content: "ping"}},
		MaxTokens: 1,
		Stream:    true,
	}
	timer := latency.Start()
	stream, err := prov.ChatCompletion(ctx, t.Route, req)
	if err != nil {
		s.Health.RouteFailure(string(t.Route.ID), 0)
		return
	}
	defer stream.Close()

	recorded := false
	for {
		ev, err := stream.Next(ctx)
		if err != nil {
			if !recorded {
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
			return
		}
	}
}
