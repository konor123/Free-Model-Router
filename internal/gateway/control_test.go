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
