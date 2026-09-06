package control

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/konor123/Free-Model-Router/internal/catalog"
	"github.com/konor123/Free-Model-Router/internal/gateway"
	"github.com/konor123/Free-Model-Router/internal/model"
	"github.com/konor123/Free-Model-Router/internal/usage"
)

type testEventSource struct{ broadcaster *usage.Broadcaster }

type eventBackend struct{}

func (eventBackend) ControlSnapshot() gateway.ControlSnapshot { return gateway.ControlSnapshot{} }
func (eventBackend) UpdateModelPool(int64, gateway.PoolMutation) (catalog.ModelPoolConfig, error) {
	return catalog.ModelPoolConfig{}, nil
}
func (eventBackend) ReplaceModelPool(int64, []model.ProviderModelID, catalog.PoolMode) (catalog.ModelPoolConfig, error) {
	return catalog.ModelPoolConfig{}, nil
}
func (eventBackend) PinModel(int64, model.ProviderModelID) (catalog.ModelPoolConfig, error) {
	return catalog.ModelPoolConfig{}, nil
}
func (eventBackend) AutoSelect(int64) (catalog.ModelPoolConfig, error) {
	return catalog.ModelPoolConfig{}, nil
}

func (s testEventSource) Subscribe() (<-chan usage.Event, func()) { return s.broadcaster.Subscribe() }

func TestControlLogsEventsStreamsSafeLiveUsage(t *testing.T) {
	source := testEventSource{broadcaster: usage.NewBroadcaster(4)}
	server, err := NewServer(eventBackend{}, Options{Token: "secret", Events: source})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/_fmr/logs/events", nil).WithContext(ctx)
	req.RemoteAddr = "127.0.0.1:1000"
	req.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		server.ServeHTTP(response, req)
		close(done)
	}()
	time.Sleep(10 * time.Millisecond)
	source.broadcaster.Publish(usage.Event{Type: usage.EventCompleted, RequestID: "request-3", Record: &usage.RequestRecord{ID: "request-3", Result: usage.ResultSuccess}})
	deadline := time.Now().Add(time.Second)
	for !strings.Contains(response.Body.String(), "request-3") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("SSE handler did not stop after cancellation")
	}
	body := response.Body.String()
	if !strings.Contains(body, "event: completed") || strings.Contains(body, "messages") || strings.Contains(body, "authorization") {
		t.Fatalf("SSE body = %q", body)
	}
}
