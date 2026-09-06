package control

import (
	"bufio"
	"context"
	"errors"
	"io"
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
	controlServer, err := NewServer(eventBackend{}, Options{Token: "secret", Events: source})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handlerDone := make(chan struct{})
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerDone)
		controlServer.ServeHTTP(w, r)
	}))
	defer httpServer.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, httpServer.URL+"/_fmr/logs/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret")
	responseCh := make(chan *http.Response, 1)
	errCh := make(chan error, 1)
	go func() {
		response, err := httpServer.Client().Do(req)
		if err != nil {
			errCh <- err
			return
		}
		responseCh <- response
	}()
	var response *http.Response
	select {
	case response = <-responseCh:
	case err := <-errCh:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("SSE request did not receive response headers")
	}
	defer response.Body.Close()
	bodyDone := make(chan struct{})
	var body strings.Builder
	eventSeen := make(chan string, 1)
	go func() {
		defer close(bodyDone)
		scanner := bufio.NewScanner(response.Body)
		for scanner.Scan() {
			body.WriteString(scanner.Text())
			body.WriteByte('\n')
			if strings.Contains(body.String(), "request-3") {
				select {
				case eventSeen <- body.String():
				default:
				}
			}
		}
		if err := scanner.Err(); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, io.EOF) {
			t.Errorf("read SSE body: %v", err)
		}
	}()
	source.broadcaster.Publish(usage.Event{Type: usage.EventCompleted, RequestID: "request-3", Record: &usage.RequestRecord{ID: "request-3", Result: usage.ResultSuccess}})
	select {
	case <-eventSeen:
	case <-time.After(time.Second):
		t.Fatal("SSE event was not delivered")
	}
	cancel()
	select {
	case <-bodyDone:
	case <-time.After(time.Second):
		t.Fatal("SSE response did not stop after cancellation")
	}
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("SSE handler did not stop after cancellation")
	}
	if !strings.Contains(body.String(), "event: completed") || strings.Contains(body.String(), "messages") || strings.Contains(body.String(), "authorization") {
		t.Fatalf("SSE body = %q", body.String())
	}
}
