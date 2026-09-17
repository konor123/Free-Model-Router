// Package probe implements periodic TTFT probing for eligible pool routes.
package probe

import (
	"context"
	"errors"
	"io"
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

	backoffMu sync.Mutex
	backoffs  map[string]probeBackoff
	inFlight  map[string]bool
}

type probeBackoff struct {
	delay  time.Duration
	nextAt time.Time
}

// DefaultProbeTimeout bounds one upstream probe after a provider slot is held.
const DefaultProbeTimeout = 10 * time.Second
const maxConcurrentProbes = 4

// Retry backoff for probes that failed with an upstream error. Failed probes
// are retried with exponential backoff capped at maxProbeBackoff so a broken
// route does not consume quota on every cycle.
const (
	baseProbeBackoff = 30 * time.Second
	maxProbeBackoff  = 10 * time.Minute
)

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
	return &Scheduler{Reg: reg, Health: h, interval: interval, timeout: DefaultProbeTimeout, backoffs: map[string]probeBackoff{}, inFlight: map[string]bool{}}
}

// EligibleTargets filters routes down to probeable candidates (PLAN_V7 §10):
// selected pool ∩ enabled route ∩ probe policy ∩ not cooling down.
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
			if !route.AllowsProbe() {
				continue
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
	sem := make(chan struct{}, maxConcurrentProbes)
	var workers sync.WaitGroup
	for _, target := range targets {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		workers.Add(1)
		go func(target Target) {
			defer workers.Done()
			defer func() { <-sem }()
			s.probeOnce(ctx, target, snap.Provider)
		}(target)
	}
	workers.Wait()
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

// claimProbe suppresses concurrent attempts and attempts still backing off.
func (s *Scheduler) claimProbe(id string, now time.Time) bool {
	s.backoffMu.Lock()
	defer s.backoffMu.Unlock()
	if s.inFlight[id] || now.Before(s.backoffs[id].nextAt) {
		return false
	}
	if s.inFlight == nil {
		s.inFlight = make(map[string]bool)
	}
	s.inFlight[id] = true
	return true
}

func (s *Scheduler) finishProbe(id string) {
	s.backoffMu.Lock()
	delete(s.inFlight, id)
	s.backoffMu.Unlock()
}

func (s *Scheduler) failProbe(id, outcome string) {
	s.Reg.MarkProbeFailure(id, outcome)
	if outcome == "canceled" {
		return
	}
	s.backoffMu.Lock()
	state := s.backoffs[id]
	if state.delay == 0 {
		state.delay = baseProbeBackoff
	} else {
		state.delay = min(state.delay*2, maxProbeBackoff)
	}
	state.nextAt = time.Now().Add(state.delay)
	if s.backoffs == nil {
		s.backoffs = make(map[string]probeBackoff)
	}
	s.backoffs[id] = state
	s.backoffMu.Unlock()
	s.Health.RouteFailure(id, 0)
}

func (s *Scheduler) probeOnce(ctx context.Context, t Target, prov provider.Provider) {
	id := string(t.Route.ID)
	if ctx.Err() != nil || !s.claimProbe(id, time.Now()) {
		return
	}
	defer s.finishProbe(id)
	// Slot contention is not an upstream failure and does not begin a probe.
	slotCtx, cancelSlot := context.WithTimeout(ctx, 2*time.Second)
	release, err := s.Health.AcquireSlot(slotCtx, t.Route.Provider)
	cancelSlot()
	if err != nil {
		return
	}
	defer release()
	if ctx.Err() != nil {
		return
	}
	probeCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	s.Reg.BeginProbe(id)
	failed := func(err error) {
		outcome := "error"
		switch {
		case ctx.Err() != nil:
			outcome = "canceled"
		case probeCtx.Err() == context.DeadlineExceeded || errors.Is(err, context.DeadlineExceeded):
			outcome = "timeout"
		case errors.Is(err, io.EOF):
			outcome = "no_semantic"
		}
		s.failProbe(id, outcome)
	}
	req := provider.NormalizedRequest{
		Messages:  []provider.Message{{Role: "user", Content: "ping"}},
		MaxTokens: 1,
		Stream:    true,
	}
	timer := latency.Start()
	stream, err := prov.ChatCompletion(probeCtx, t.Route, req)
	if err != nil {
		failed(err)
		return
	}
	defer stream.Close()
	for {
		ev, err := stream.Next(probeCtx)
		if err != nil {
			failed(err)
			return
		}
		// A finish-only event is not a token. Text, reasoning and tool deltas
		// all qualify; do not drain a stream after the first measured delta.
		if ev.DeltaText != "" || ev.ReasoningContent != "" || len(ev.ToolCalls) > 0 {
			if d, ok := timer.OnSemanticEvent(); ok {
				// Zero means unknown in the registry. A measured delta below
				// clock resolution needs a positive sentinel, not claimed precision.
				if d == 0 {
					d = time.Nanosecond
				}
				s.Reg.RecordProbe(id, float64(d)/float64(time.Millisecond))
				s.backoffMu.Lock()
				delete(s.backoffs, id)
				s.backoffMu.Unlock()
				s.Health.RouteSuccess(id)
			}
			return
		}
		if ev.FinishReason != "" {
			failed(io.EOF)
			return
		}
	}
}
