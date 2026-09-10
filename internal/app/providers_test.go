package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/konor123/Free-Model-Router/internal/config"
	"github.com/konor123/Free-Model-Router/internal/gateway"
	"github.com/konor123/Free-Model-Router/internal/model"
	"github.com/konor123/Free-Model-Router/internal/provider"
)

func TestConfiguredProviderBuildsEnabledCompatibleInstance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"test-model"}]}`))
	}))
	defer server.Close()
	prov, err := configuredProvider(&config.Config{Providers: []config.ProviderConfig{{ID: "local", Name: "Local", Protocol: config.OpenAICompatibleProtocol, BaseURL: server.URL, Enabled: true}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	routed, ok := prov.(provider.RoutedCatalogProvider)
	if !ok {
		t.Fatal("configured provider lacks routed catalog")
	}
	catalog, err := routed.DiscoverRoutedCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := catalog.Snapshot.Models["local/test-model"]; !ok {
		t.Fatalf("models = %#v", catalog.Snapshot.Models)
	}
}

func TestConfiguredProviderRejectsNoEnabledProviders(t *testing.T) {
	if _, err := configuredProvider(&config.Config{}, nil); err == nil {
		t.Fatal("accepted empty provider set")
	}
}

func TestConfiguredProviderReportsMissingExplicitCredentialAsUnavailable(t *testing.T) {
	prov, err := configuredProvider(&config.Config{Providers: []config.ProviderConfig{{ID: "local", Name: "Local", Protocol: config.OpenAICompatibleProtocol, BaseURL: "https://example.test/v1", Enabled: true, CredentialRef: "missing"}}}, nil)
	if err != nil {
		t.Fatal("provider construction must defer credential resolution")
	}
	routed := prov.(provider.RoutedCatalogProvider)
	catalog, err := routed.DiscoverRoutedCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Snapshot.Models) != 0 || len(catalog.Providers) != 1 || catalog.Providers[0].Available {
		t.Fatalf("catalog = %#v", catalog)
	}
}

func TestGenericRoutesDefaultAllowAndExactExclusion(t *testing.T) {
	pm := model.ProviderModel{ID: "local/vendor-model", UpstreamID: "vendor-model"}
	routes, err := genericRoutes(config.ProviderConfig{ID: "local"})(pm)
	if err != nil || len(routes) != 1 || !routes[0].Enabled || !routes[0].AllowsAutomaticRouting() || !routes[0].AllowsProbe() || !routes[0].SelectedByDefault() {
		t.Fatalf("default routes=%#v err=%v", routes, err)
	}
	probe := true
	routes, err = genericRoutes(config.ProviderConfig{ID: "local", AutoProbe: &probe, ExcludedModelIDs: []string{"vendor-model"}})(pm)
	if err != nil || routes[0].Enabled || routes[0].AllowsAutomaticRouting() || routes[0].AllowsProbe() || routes[0].SelectedByDefault() {
		t.Fatalf("excluded routes=%#v err=%v", routes, err)
	}
}

func TestGenericModelsDefaultSelectedInManualAndExcludedRemainVisible(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"included"},{"id":"excluded"}]}`))
	}))
	defer server.Close()
	cfg := &config.Config{Providers: []config.ProviderConfig{{ID: "custom", Name: "Custom", Protocol: config.OpenAICompatibleProtocol, BaseURL: server.URL, Enabled: true, ExcludedModelIDs: []string{"excluded"}}}}
	prov, err := configuredProvider(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	gw, err := gateway.NewGateway(context.Background(), prov)
	if err != nil {
		t.Fatal(err)
	}
	if err := gw.RestoreControlState(nil, "Manual", ""); err != nil {
		t.Fatal(err)
	}
	snapshot := gw.ControlSnapshot()
	if len(snapshot.Models) != 2 {
		t.Fatalf("discovered models = %d", len(snapshot.Models))
	}
	selected := map[string]bool{}
	for _, item := range snapshot.Models {
		selected[string(item.Model.ID)] = item.Selected
	}
	if !selected["custom/included"] || selected["custom/excluded"] {
		t.Fatalf("selection = %#v", selected)
	}
}
