package openaicompat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/konor123/Free-Model-Router/internal/model"
	"github.com/konor123/Free-Model-Router/internal/provider"
)

func TestProviderNamespacesCatalogAndInjectsCredential(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		if r.URL.Path != "/models" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"vendor/model"}]}`))
	}))
	defer server.Close()
	p, err := New("local-one", server.URL, func() (string, error) { return "secret", nil })
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := p.DiscoverModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snapshot.Models["local-one/b64-dmVuZG9yL21vZGVs"]; !ok {
		t.Fatalf("models = %#v", snapshot.Models)
	}
}

func TestProviderAcceptsVendorSpecificCatalogMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"vendor/reasoning-model","reasoning":{"effort":"high"},"context_length":"unknown"},{"id":"vendor/plain-model","reasoning":true}]}`))
	}))
	defer server.Close()
	p, err := New("vendor", server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := p.DiscoverModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Models) != 2 {
		t.Fatalf("model count = %d", len(snapshot.Models))
	}
}

func TestOpenRouterCatalogKeepsOnlyFreeModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"openrouter/free","pricing":{"prompt":"1","completion":"1"}},{"id":"vendor/model:free","pricing":{"prompt":"1"}},{"id":"vendor/zero-price","pricing":{"prompt":"0","completion":0}},{"id":"vendor/paid","pricing":{"prompt":"0.1","completion":"0"}},{"id":"vendor/nested","pricing":{"prompt":"0","overrides":{"context":1}}},{"id":"vendor/null-price","pricing":{"prompt":null}},{"id":"vendor/unknown"}]}`))
	}))
	defer server.Close()
	p, err := New("openrouter-free", "https://openrouter.ai/api/v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	p.backend.BaseURL, p.backend.HTTP = server.URL, server.Client()
	snapshot, err := p.DiscoverModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Models) != 3 {
		t.Fatalf("model count = %d; models = %#v", len(snapshot.Models), snapshot.Models)
	}
}

func TestOpenRouterDetectionRequiresExactHost(t *testing.T) {
	if !isOpenRouterBaseURL("https://openrouter.ai/api/v1") {
		t.Fatal("OpenRouter URL was not detected")
	}
	if isOpenRouterBaseURL("https://openrouter.ai.example.com/v1") {
		t.Fatal("lookalike host was detected as OpenRouter")
	}
}

func TestProviderRejectsRouteFromAnotherInstance(t *testing.T) {
	p, err := New("local-one", "https://example.test/v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.ChatCompletion(context.Background(), model.ProviderRoute{Provider: "other"}, provider.NormalizedRequest{}); err == nil {
		t.Fatal("accepted foreign route")
	}
}
