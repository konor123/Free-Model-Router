package control_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/konor123/Free-Model-Router/internal/catalog"
	"github.com/konor123/Free-Model-Router/internal/control"
	"github.com/konor123/Free-Model-Router/internal/gateway"
	"github.com/konor123/Free-Model-Router/internal/model"
	"github.com/konor123/Free-Model-Router/internal/usage"
)

type fakeBackend struct {
	snapshot gateway.ControlSnapshot
}

func (f *fakeBackend) ControlSnapshot() gateway.ControlSnapshot { return f.snapshot }
func (f *fakeBackend) UpdateModelPool(int64, gateway.PoolMutation) (catalog.ModelPoolConfig, error) {
	return f.snapshot.Pool, nil
}
func (f *fakeBackend) ReplaceModelPool(int64, []model.ProviderModelID, catalog.PoolMode) (catalog.ModelPoolConfig, error) {
	return f.snapshot.Pool, nil
}
func (f *fakeBackend) PinModel(int64, model.ProviderModelID) (catalog.ModelPoolConfig, error) {
	return f.snapshot.Pool, nil
}
func (f *fakeBackend) AutoSelect(int64) (catalog.ModelPoolConfig, error) {
	return f.snapshot.Pool, nil
}

type fakeLogs struct{ records []usage.RequestRecord }

func (f fakeLogs) List(usage.Query) ([]usage.RequestRecord, error) { return f.records, nil }

func TestControlAPIStatusAndBearerAuth(t *testing.T) {
	id, _ := model.NewProviderModelID("opencode", "mimo-v2.5")
	backend := &fakeBackend{snapshot: gateway.ControlSnapshot{
		CatalogRevision: 7,
		Pool:            catalog.ModelPoolConfig{Revision: 12, Mode: catalog.ModeManual, SelectedProviderModelIDs: []model.ProviderModelID{id}},
		Models:          []gateway.ControlModel{{Model: model.ProviderModel{ID: id, DisplayName: "MiMo"}, Selected: true}},
	}}
	server, err := control.NewServer(backend, control.Options{
		Token:        "management-secret",
		BuildVersion: "test-build",
		InstanceID:   "instance-test",
		Logs:         fakeLogs{},
	})
	if err != nil {
		t.Fatal(err)
	}

	unauthorized := httptest.NewRecorder()
	unauthorizedRequest := httptest.NewRequest(http.MethodGet, "/_fmr/status", nil)
	unauthorizedRequest.RemoteAddr = "127.0.0.1:1234"
	server.ServeHTTP(unauthorized, unauthorizedRequest)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, want 401", unauthorized.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/_fmr/status", nil)
	req.Header.Set("Authorization", "Bearer management-secret")
	req.RemoteAddr = "127.0.0.1:1234"
	response := httptest.NewRecorder()
	server.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("status response = %d: %s", response.Code, response.Body.String())
	}
	var status control.StatusResponse
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.APIVersion != "1" || status.BuildVersion != "test-build" || status.InstanceID != "instance-test" {
		t.Fatalf("status handshake = %+v", status)
	}
}

func TestControlAPIRejectsNonLoopbackAndKeepsModelIDsInBodies(t *testing.T) {
	id, _ := model.NewProviderModelID("opencode", "mimo-v2.5")
	backend := &fakeBackend{snapshot: gateway.ControlSnapshot{Pool: catalog.ModelPoolConfig{Revision: 12, Mode: catalog.ModeManual}}}
	server, err := control.NewServer(backend, control.Options{Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}

	remote := httptest.NewRequest(http.MethodGet, "/_fmr/models", nil)
	remote.RemoteAddr = "192.0.2.20:1000"
	remote.Header.Set("Authorization", "Bearer secret")
	remoteResponse := httptest.NewRecorder()
	server.ServeHTTP(remoteResponse, remote)
	if remoteResponse.Code != http.StatusForbidden {
		t.Fatalf("remote management response = %d, want 403", remoteResponse.Code)
	}

	requestBody := `{"providerModelId":"opencode/mimo-v2.5","revision":12}`
	request := httptest.NewRequest(http.MethodPost, "/_fmr/pin", strings.NewReader(requestBody))
	request.RemoteAddr = "127.0.0.1:1000"
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code == http.StatusNotFound {
		t.Fatal("pin endpoint must not require a model ID path parameter")
	}
	_ = id
}

func TestControlAPILogsExposeFiltersWithoutRequestContent(t *testing.T) {
	logs := fakeLogs{records: []usage.RequestRecord{{ID: "r1", Provider: "opencode", Result: usage.ResultSuccess, StartedAt: time.Now().UTC()}}}
	server, err := control.NewServer(&fakeBackend{}, control.Options{Token: "secret", Logs: logs})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/_fmr/logs?provider=opencode&limit=10", nil)
	req.RemoteAddr = "127.0.0.1:1000"
	req.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("logs response = %d: %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "messages") || strings.Contains(response.Body.String(), "authorization") {
		t.Fatal("control log response exposed sensitive fields")
	}
}
