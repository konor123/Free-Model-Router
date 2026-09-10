package registry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/konor123/Free-Model-Router/internal/model"
	"github.com/konor123/Free-Model-Router/internal/provider"
)

type fakeProvider struct {
	snapshot *model.CatalogSnapshot
	err      error
	calls    int
}

func (f *fakeProvider) DiscoverModels(context.Context) (*model.CatalogSnapshot, error) {
	return f.snapshot, f.err
}

func TestRegistryKeepsUsableProviderWhenAnotherDiscoveryFails(t *testing.T) {
	pm := testModel(t, "good", "model")
	good := &fakeProvider{snapshot: &model.CatalogSnapshot{Models: map[model.ProviderModelID]model.ProviderModel{pm.ID: pm}}}
	bad := &fakeProvider{err: provider.NewFailureError(model.NewFailure(model.FailureNetwork, model.ScopeProvider), errors.New("secret must not escape"))}
	builder := func(pm model.ProviderModel) ([]model.ProviderRoute, error) {
		owner, _, _ := pm.ID.Parse()
		rid, _ := model.NewRouteID(owner, pm.UpstreamID)
		return []model.ProviderRoute{{ID: rid, ModelID: pm.ID, Provider: owner, UpstreamModelID: pm.UpstreamID, Access: model.AccessFree, Enabled: true}}, nil
	}
	r, err := New([]Entry{{ID: "bad", Backend: bad, Routes: builder}, {ID: "good", Backend: good, Routes: builder}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := r.DiscoverRoutedCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Snapshot.Models) != 1 {
		t.Fatalf("models = %d", len(catalog.Snapshot.Models))
	}
	if len(catalog.Providers) != 2 || catalog.Providers[0].ID != "bad" || catalog.Providers[0].Message == "secret must not escape" {
		t.Fatalf("providers = %#v", catalog.Providers)
	}
}

func TestRegistryReportsAllProvidersUnavailableWithEmptyCatalog(t *testing.T) {
	failure := provider.NewFailureError(model.NewFailure(model.FailureNetwork, model.ScopeProvider), errors.New("offline"))
	r, err := New([]Entry{{ID: "bad", Backend: &fakeProvider{err: failure}, Routes: func(model.ProviderModel) ([]model.ProviderRoute, error) { return nil, nil }}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := r.DiscoverRoutedCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Snapshot.Models) != 0 || len(catalog.Providers) != 1 || catalog.Providers[0].Available {
		t.Fatalf("catalog = %#v", catalog)
	}
}

func TestRegistryDoesNotPublishPartialCatalogAfterCancellation(t *testing.T) {
	pm := testModel(t, "good", "model")
	good := &fakeProvider{snapshot: &model.CatalogSnapshot{Models: map[model.ProviderModelID]model.ProviderModel{pm.ID: pm}}}
	canceled := &fakeProvider{err: provider.NewFailureError(model.NewFailure(model.FailureNetwork, model.ScopeProvider), context.Canceled)}
	builder := func(pm model.ProviderModel) ([]model.ProviderRoute, error) {
		owner, _, _ := pm.ID.Parse()
		rid, _ := model.NewRouteID(owner, pm.UpstreamID)
		return []model.ProviderRoute{{ID: rid, ModelID: pm.ID, Provider: owner, UpstreamModelID: pm.UpstreamID, Enabled: true}}, nil
	}
	r, err := New([]Entry{{ID: "good", Backend: good, Routes: builder}, {ID: "later", Backend: canceled, Routes: builder}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.DiscoverRoutedCatalog(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}
func (f *fakeProvider) ChatCompletion(context.Context, model.ProviderRoute, provider.NormalizedRequest) (provider.ChatStream, error) {
	f.calls++
	return nil, nil
}

func testModel(t *testing.T, owner, upstream string) model.ProviderModel {
	t.Helper()
	id, err := model.NewProviderModelID(owner, upstream)
	if err != nil {
		t.Fatal(err)
	}
	key, err := model.NewCanonicalModelKey(upstream)
	if err != nil {
		t.Fatal(err)
	}
	return model.ProviderModel{ID: id, CanonicalKey: key, DisplayName: upstream, UpstreamID: upstream}
}

func TestRegistryKeepsSameUpstreamModelsSeparated(t *testing.T) {
	one, two := &fakeProvider{}, &fakeProvider{}
	one.snapshot = &model.CatalogSnapshot{CreatedAt: time.Now(), Models: map[model.ProviderModelID]model.ProviderModel{}}
	two.snapshot = &model.CatalogSnapshot{CreatedAt: time.Now(), Models: map[model.ProviderModelID]model.ProviderModel{}}
	for _, item := range []struct {
		owner  string
		target *fakeProvider
	}{{"one", one}, {"two", two}} {
		pm := testModel(t, item.owner, "shared")
		item.target.snapshot.Models[pm.ID] = pm
	}
	builder := func(pm model.ProviderModel) ([]model.ProviderRoute, error) {
		owner, _, _ := pm.ID.Parse()
		rid, _ := model.NewRouteID(owner, pm.UpstreamID)
		return []model.ProviderRoute{{ID: rid, ModelID: pm.ID, Provider: owner, UpstreamModelID: pm.UpstreamID, Access: model.AccessFree, Enabled: true}}, nil
	}
	r, err := New([]Entry{{ID: "one", Backend: one, Routes: builder}, {ID: "two", Backend: two, Routes: builder}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := r.DiscoverRoutedCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Snapshot.Models) != 2 {
		t.Fatalf("models = %d", len(catalog.Snapshot.Models))
	}
	for modelID, routes := range catalog.Routes {
		if _, err := r.ChatCompletion(context.Background(), routes[0], provider.NormalizedRequest{}); err != nil {
			t.Fatalf("dispatch %s: %v", modelID, err)
		}
	}
	if one.calls != 1 || two.calls != 1 {
		t.Fatalf("calls one=%d two=%d", one.calls, two.calls)
	}
}

func TestRegistryRejectsCrossProviderRoute(t *testing.T) {
	pm := testModel(t, "one", "model")
	fake := &fakeProvider{snapshot: &model.CatalogSnapshot{Models: map[model.ProviderModelID]model.ProviderModel{pm.ID: pm}}}
	r, err := New([]Entry{{ID: "one", Backend: fake, Routes: func(model.ProviderModel) ([]model.ProviderRoute, error) {
		return []model.ProviderRoute{{ModelID: pm.ID, Provider: "two"}}, nil
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.DiscoverRoutedCatalog(context.Background()); err == nil {
		t.Fatal("accepted cross-provider route")
	}
}

func TestRegistryRejectsMismatchedModelMapKey(t *testing.T) {
	mapKey := testModel(t, "one", "map-key")
	modelValue := testModel(t, "one", "model-value")
	fake := &fakeProvider{snapshot: &model.CatalogSnapshot{Models: map[model.ProviderModelID]model.ProviderModel{
		mapKey.ID: modelValue,
	}}}
	r, err := New([]Entry{{ID: "one", Backend: fake, Routes: func(model.ProviderModel) ([]model.ProviderRoute, error) {
		return []model.ProviderRoute{{ModelID: mapKey.ID, Provider: "one"}}, nil
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.DiscoverRoutedCatalog(context.Background()); err == nil {
		t.Fatal("accepted model whose ID differs from its map key")
	}
}

func TestRegistryRejectsEmptyRoutes(t *testing.T) {
	pm := testModel(t, "one", "model")
	fake := &fakeProvider{snapshot: &model.CatalogSnapshot{Models: map[model.ProviderModelID]model.ProviderModel{pm.ID: pm}}}
	r, err := New([]Entry{{ID: "one", Backend: fake, Routes: func(model.ProviderModel) ([]model.ProviderRoute, error) {
		return nil, nil
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.DiscoverRoutedCatalog(context.Background()); err == nil {
		t.Fatal("accepted published model with no routes")
	}
}
