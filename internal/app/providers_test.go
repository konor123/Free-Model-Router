package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/konor123/Free-Model-Router/internal/config"
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

func TestConfiguredProviderRejectsMissingExplicitCredential(t *testing.T) {
	prov, err := configuredProvider(&config.Config{Providers: []config.ProviderConfig{{ID: "local", Name: "Local", Protocol: config.OpenAICompatibleProtocol, BaseURL: "https://example.test/v1", Enabled: true, CredentialRef: "missing"}}}, nil)
	if err != nil {
		t.Fatal("provider construction must defer credential resolution")
	}
	if _, err := prov.DiscoverModels(context.Background()); err == nil {
		t.Fatal("accepted a missing explicit credential")
	}
}
