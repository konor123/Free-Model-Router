package app

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/konor123/Free-Model-Router/internal/config"
	"github.com/konor123/Free-Model-Router/internal/logging"
	"github.com/konor123/Free-Model-Router/internal/providers/opencode"
)

func freeBindAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func TestRunWithProviderWiresRealHTTPAppPath(t *testing.T) {
	t.Setenv(opencode.AuthRouteEnv, "")
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"mimo-v2.5"}]}`))
			return
		}
		if r.URL.Path != "/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var payload struct {
			Stream bool `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode chat payload: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if payload.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"probe\"}}]}\n\ndata: [DONE]\n\n"))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"pong"},"finish_reason":"stop"}]}`))
	}))
	defer backend.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log, err := logging.New(logging.Error, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Bind: freeBindAddress(t), LogLevel: "error"}
	provider := opencode.New(backend.URL)

	errCh := make(chan error, 1)
	go func() {
		errCh <- RunWithProvider(ctx, cfg, log, provider)
	}()

	baseURL := "http://" + cfg.Bind
	var models []byte
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, requestErr := http.Get(baseURL + "/v1/models")
		if requestErr == nil {
			models, _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(string(models), `"afm/auto"`) || !strings.Contains(string(models), "opencode/mimo-v2.5") {
		t.Fatalf("app did not expose the live catalog: %s", models)
	}

	resp, err := http.Post(baseURL+"/v1/chat/completions", "application/json", strings.NewReader(
		`{"model":"afm/auto","messages":[{"role":"user","content":"ping"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "pong") {
		t.Fatalf("app request path = %d %s", resp.StatusCode, body)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("app shutdown error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("app did not stop after context cancellation")
	}
}
