package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/konor123/Free-Model-Router/internal/config"
	"github.com/konor123/Free-Model-Router/internal/control"
	"github.com/konor123/Free-Model-Router/internal/logging"
	"github.com/konor123/Free-Model-Router/internal/providers/opencode"
)

func TestRunWithProviderExposesAuthenticatedControlBoundary(t *testing.T) {
	t.Setenv(config.ManagementTokenKey, "test-management-token")
	t.Setenv(opencode.AuthRouteEnv, "")
	backend := newControlBackend(t)
	defer backend.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log, err := logging.New(logging.Error, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Bind: freeBindAddress(t), ManagementBind: freeBindAddress(t), LogLevel: "error"}
	provider := opencode.New(backend.URL)
	errCh := make(chan error, 1)
	go func() { errCh <- RunWithProvider(ctx, cfg, log, provider) }()

	client := control.NewClient(cfg.ManagementBind, "test-management-token")
	var status control.StatusResponse
	waitForControl(t, client, "/_fmr/status", &status)
	if status.APIVersion != control.APIVersion || status.InstanceID == "" {
		t.Fatalf("status handshake = %+v", status)
	}

	poolBody, err := client.Get(ctx, "/_fmr/model-pool")
	if err != nil {
		t.Fatal(err)
	}
	var pool control.PoolResponse
	if err := json.Unmarshal(poolBody, &pool); err != nil {
		t.Fatal(err)
	}
	if pool.Revision == 0 || len(pool.SelectedProviderModelIDs) == 0 {
		t.Fatalf("unexpected initial pool = %+v", pool)
	}

	modelID := pool.SelectedProviderModelIDs[0]
	updated, err := client.Patch(ctx, "/_fmr/model-pool", map[string]any{
		"revision": pool.Revision, "deselect": []string{modelID},
	})
	if err != nil {
		t.Fatal(err)
	}
	var updatedPool control.PoolResponse
	if err := json.Unmarshal(updated, &updatedPool); err != nil {
		t.Fatal(err)
	}
	if updatedPool.Revision <= pool.Revision || contains(updatedPool.SelectedProviderModelIDs, modelID) {
		t.Fatalf("pool mutation not applied: before=%+v after=%+v", pool, updatedPool)
	}

	_, err = client.Patch(ctx, "/_fmr/model-pool", map[string]any{
		"revision": pool.Revision, "select": []string{modelID},
	})
	var httpErr *control.HTTPError
	if !asHTTPError(err, &httpErr) || httpErr.StatusCode != http.StatusConflict {
		t.Fatalf("stale pool write error = %v, want HTTP 409", err)
	}

	inference, err := http.Get("http://" + cfg.Bind + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	inferenceBody, _ := io.ReadAll(inference.Body)
	_ = inference.Body.Close()
	if inference.StatusCode != http.StatusOK || !strings.Contains(string(inferenceBody), "fmr/auto") {
		t.Fatalf("inference boundary = %d %s", inference.StatusCode, inferenceBody)
	}

	cancel()
	select {
	case runErr := <-errCh:
		if runErr != nil {
			t.Fatalf("app shutdown error: %v", runErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("app did not stop")
	}
}

func newControlBackend(t *testing.T) *controlBackend {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"mimo-v2.5"}]}`))
			return
		}
		if r.URL.Path == "/chat/completions" {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"pong"},"finish_reason":"stop"}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	return &controlBackend{Server: server}
}

type controlBackend struct {
	*httptest.Server
}

func waitForControl(t *testing.T, client *control.Client, path string, dst any) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		body, err := client.Get(context.Background(), path)
		if err == nil {
			if decodeErr := json.Unmarshal(body, dst); decodeErr != nil {
				t.Fatal(decodeErr)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("control endpoint %s did not become ready", path)
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func asHTTPError(err error, target **control.HTTPError) bool {
	if err == nil {
		return false
	}
	value, ok := err.(*control.HTTPError)
	if ok && target != nil {
		*target = value
	}
	return ok
}
