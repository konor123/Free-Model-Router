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

func TestProviderRejectsRouteFromAnotherInstance(t *testing.T) {
	p, err := New("local-one", "https://example.test/v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.ChatCompletion(context.Background(), model.ProviderRoute{Provider: "other"}, provider.NormalizedRequest{}); err == nil {
		t.Fatal("accepted foreign route")
	}
}
