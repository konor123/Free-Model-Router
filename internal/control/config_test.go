package control_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/konor123/Free-Model-Router/internal/control"
)

type refreshBackend struct {
	fakeBackend
	refreshed bool
}

func (b *refreshBackend) RefreshCatalog(context.Context) error { b.refreshed = true; return nil }

func TestConfigUpdateIsAuthenticatedAndWriteOnly(t *testing.T) {
	called := false
	server, err := control.NewServer(&fakeBackend{}, control.Options{Token: "secret", Config: func() control.ConfigResponse {
		return control.ConfigResponse{Bind: "127.0.0.1:8787"}
	}, UpdateConfig: func(request control.ConfigUpdateRequest) (control.ConfigUpdateResponse, error) {
		called = true
		if request.Providers[0].APIKey == nil || *request.Providers[0].APIKey != "top-secret" {
			t.Fatal("write-only key was not decoded")
		}
		return control.ConfigUpdateResponse{Config: control.ConfigResponse{Bind: request.Bind, Providers: []control.ProviderConfigResponse{{ID: "local", HasCredential: true}}}, RestartRequired: true}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"revision":0,"bind":"127.0.0.1:9999","providers":[{"id":"local","name":"Local","protocol":"openai-compatible","baseUrl":"https://example.test/v1","enabled":true,"apiKey":"top-secret"}]}`
	request := httptest.NewRequest(http.MethodPut, "/_fmr/config", strings.NewReader(body))
	request.RemoteAddr = "127.0.0.1:1234"
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !called {
		t.Fatalf("status=%d called=%v body=%s", response.Code, called, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "top-secret") {
		t.Fatal("response exposed write-only key")
	}
}

func TestCatalogRefreshUsesOptionalBackendCapability(t *testing.T) {
	backend := &refreshBackend{}
	server, err := control.NewServer(backend, control.Options{Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/_fmr/catalog/refresh", strings.NewReader(`{}`))
	request.RemoteAddr = "127.0.0.1:1234"
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !backend.refreshed {
		t.Fatalf("status=%d refreshed=%v", response.Code, backend.refreshed)
	}
}
