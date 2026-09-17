package gateway

import (
	"context"
	"errors"
	"testing"

	"github.com/konor123/Free-Model-Router/internal/catalog"
	"github.com/konor123/Free-Model-Router/internal/model"
	"github.com/konor123/Free-Model-Router/internal/provider"
)

func TestControlSnapshotAndOptimisticPoolMutations(t *testing.T) {
	p := &fallbackProvider{models: []string{"a", "b"}}
	p.behavior = func(context.Context, model.ProviderRoute, provider.NormalizedRequest) (provider.ChatStream, error) {
		return nil, errors.New("not used")
	}
	g, err := NewGateway(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	initial := g.ControlSnapshot()
	if len(initial.Models) != 2 || len(initial.Providers) != 1 || initial.Pool.Revision == 0 {
		t.Fatalf("initial control snapshot = %+v", initial)
	}
	if initial.Providers[0].Models != 2 || initial.Providers[0].Routes != 2 {
		t.Fatalf("provider summary = %+v", initial.Providers)
	}
	if route := initial.Models[0].Routes[0]; route.PerformanceReason != "no_snapshot" || route.LatencyReason != "no_sample" || route.ScoreReason != "no_performance_and_latency" {
		t.Fatalf("metric reasons = %+v", route)
	}

	id := initial.Models[0].Model.ID
	updated, err := g.UpdateModelPool(initial.Pool.Revision, PoolMutation{Deselect: []model.ProviderModelID{id}})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision <= initial.Pool.Revision || updated.Contains(id) {
		t.Fatalf("pool update = %+v", updated)
	}
	beforeStale := g.ControlSnapshot().Pool
	if _, err := g.UpdateModelPool(initial.Pool.Revision, PoolMutation{Select: []model.ProviderModelID{id}}); !errors.Is(err, ErrPoolRevisionConflict) {
		t.Fatalf("stale update error = %v", err)
	}
	if after := g.ControlSnapshot().Pool; after.Revision != beforeStale.Revision || after.Contains(id) {
		t.Fatalf("stale update changed pool: before=%+v after=%+v", beforeStale, after)
	}

	selected, err := g.UpdateModelPool(beforeStale.Revision, PoolMutation{Select: []model.ProviderModelID{id}})
	if err != nil {
		t.Fatal(err)
	}
	if !selected.Contains(id) {
		t.Fatalf("reselect failed: %+v", selected)
	}
	pinned, err := g.PinModel(selected.Revision, id)
	if err != nil {
		t.Fatal(err)
	}
	if pinned.Revision <= selected.Revision || g.ControlSnapshot().PinnedModel != id {
		t.Fatalf("pin failed: pool=%+v snapshot=%+v", pinned, g.ControlSnapshot())
	}
	if _, err := g.AutoSelect(pinned.Revision); err != nil {
		t.Fatal(err)
	}
	if got := g.ControlSnapshot().PinnedModel; got != "" {
		t.Fatalf("auto select retained pin %q", got)
	}
	if g.ControlSnapshot().Pool.Mode != catalog.ModeAutomatic {
		t.Fatalf("auto select mode = %q", g.ControlSnapshot().Pool.Mode)
	}
}

func TestControlSnapshotSeparatesFreshAndHistoricalTTFT(t *testing.T) {
	p := &fallbackProvider{models: []string{"a"}}
	p.behavior = func(context.Context, model.ProviderRoute, provider.NormalizedRequest) (provider.ChatStream, error) {
		return nil, errors.New("not used")
	}
	g, err := NewGateway(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := g.ControlSnapshot()
	if len(snapshot.Models) != 1 || len(snapshot.Models[0].Routes) != 1 {
		t.Fatalf("unexpected snapshot shape: %+v", snapshot)
	}
	routeID := string(snapshot.Models[0].Routes[0].ID)

	// A fresh probe sample is the routing value and the historical value.
	g.latency.RecordProbe(routeID, 120)
	view := g.ControlSnapshot().Models[0].Routes[0]
	if !view.TTFTKnown || view.TTFTMs != 120 || view.LastKnownTTFTMs != 120 || !view.TTFTKnownEver || view.TTFTSource != "probe" || view.ProbeOutcome != "success" {
		t.Fatalf("fresh view = %+v", view)
	}

	// After the routing window the historical value remains display-only.
	// Age the probe timestamp directly to bypass wall-clock waits.
	g.latency.AgeProbe(routeID, 2*routingTTFTMaxAge)
	view = g.ControlSnapshot().Models[0].Routes[0]
	if view.TTFTKnown || view.TTFTMs != 0 {
		t.Fatalf("expired sample still routed: %+v", view)
	}
	if !view.TTFTKnownEver || view.LastKnownTTFTMs != 120 || view.TTFTMeasuredAt.IsZero() || view.TTFTSource != "probe" {
		t.Fatalf("historical view = %+v", view)
	}

	// A failed probe attempt records diagnostics without touching the sample.
	g.latency.MarkProbeFailure(routeID, "timeout")
	view = g.ControlSnapshot().Models[0].Routes[0]
	if view.ProbeOutcome != "timeout" || !view.TTFTKnownEver || view.LastKnownTTFTMs != 120 {
		t.Fatalf("failure view = %+v", view)
	}

	// A fresh request sample takes precedence for routing and history.
	g.latency.RecordRequest(routeID, 250, 400)
	view = g.ControlSnapshot().Models[0].Routes[0]
	if !view.TTFTKnown || view.TTFTMs != 250 || view.TTFTSource != "request" || view.LastKnownTTFTMs != 250 {
		t.Fatalf("request view = %+v", view)
	}
}

func TestControlSnapshotNeverMarksExpiredFresh(t *testing.T) {
	p := &fallbackProvider{models: []string{"a"}}
	p.behavior = func(context.Context, model.ProviderRoute, provider.NormalizedRequest) (provider.ChatStream, error) {
		return nil, errors.New("not used")
	}
	g, err := NewGateway(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := g.ControlSnapshot()
	routeID := string(snapshot.Models[0].Routes[0].ID)
	g.latency.RecordProbe(routeID, 99)
	g.latency.AgeProbe(routeID, 2*routingTTFTMaxAge)
	view := g.ControlSnapshot().Models[0].Routes[0]
	if view.TTFTKnown || view.TTFTMs != 0 || !view.TTFTKnownEver || view.LastKnownTTFTMs != 99 || view.TTFTMeasuredAt.IsZero() {
		t.Fatalf("expired probe view = %+v", view)
	}
	if view.LatencyReason != "stale" {
		t.Fatalf("latency reason = %q", view.LatencyReason)
	}
}
