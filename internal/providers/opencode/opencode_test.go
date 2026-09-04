package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/konor123/Free-Model-Router/internal/model"
	"github.com/konor123/Free-Model-Router/internal/provider"
)

// newMockServer builds a test OpenCode-compatible endpoint.
func newMockServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *Provider) {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	p := New(ts.URL)
	return ts, p
}

func TestDiscoverModelsCatalog(t *testing.T) {
	_, p := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		streaming := true
		tools := true
		json.NewEncoder(w).Encode(catalogResponse{Data: []catalogEntry{
			{ID: "mimo-v2.5", ContextLength: 128000, Streaming: &streaming, Tools: &tools},
			{ID: "glm-5.3-flash:free"},
		}})
	})
	snap, err := p.DiscoverModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(snap.Models))
	}
	id, err := model.NewProviderModelID("opencode", "mimo-v2.5")
	if err != nil {
		t.Fatal(err)
	}
	pm, ok := snap.Models[id]
	if !ok {
		t.Fatalf("missing %q in catalog", id)
	}
	if pm.Base.Tools != true || pm.Base.Streaming != true {
		t.Fatalf("capabilities not normalized: %+v", pm.Base)
	}
	if pm.Access != model.AccessFree {
		t.Fatalf("public route access should be Free, got %q", pm.Access)
	}
}

func TestDiscoverModelsMalformed(t *testing.T) {
	_, p := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	})
	if _, err := p.DiscoverModels(context.Background()); err == nil {
		t.Fatal("expected protocol failure for malformed catalog")
	}
}

func TestDiscoverModelsServerError(t *testing.T) {
	_, p := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	})
	_, err := p.DiscoverModels(context.Background())
	var fe *provider.FailureError
	if !errors.As(err, &fe) || fe.Failure.Class != model.FailureServerError {
		t.Fatalf("expected ServerError failure, got %v", err)
	}
}

func TestChatCompletionNonStreaming(t *testing.T) {
	_, p := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(chatResponse{
			Choices: []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
				Delta        string `json:"delta"`
				FinishReason string `json:"finish_reason"`
			}{
				{Delta: "hello world", FinishReason: "stop"},
			},
		})
	})
	route := PublicRoute(mustID(t, "opencode", "mimo-v2.5"), model.Capabilities{}, model.AccessFree)
	stream, err := p.ChatCompletion(context.Background(), route, provider.NormalizedRequest{
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	for {
		ev, err := stream.Next(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		sb.WriteString(ev.DeltaText)
	}
	if sb.String() != "hello world" {
		t.Fatalf("content mismatch: %q", sb.String())
	}
}

func TestChatCompletionStreamingSSE(t *testing.T) {
	sse := "data: {\"choices\":[{\"delta\":{\"content\":\"hel\"}}]}\n\n" +
		": heartbeat\n" + // comment: not semantic
		"data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n" +
		"data: [DONE]\n\n"
	_, p := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(sse))
	})
	route := PublicRoute(mustID(t, "opencode", "mimo-v2.5"), model.Capabilities{}, model.AccessFree)
	stream, err := p.ChatCompletion(context.Background(), route, provider.NormalizedRequest{
		Messages: []provider.Message{{Role: "user", Content: "hi"}}, Stream: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	var got strings.Builder
	count := 0
	for {
		ev, err := stream.Next(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got.WriteString(ev.DeltaText)
		count++
	}
	if got.String() != "hello" {
		t.Fatalf("stream content mismatch: %q", got.String())
	}
	if count != 3 { // 2 text deltas + final [DONE] event
		t.Fatalf("expected 3 events (2 text + done), got %d", count)
	}
}

func TestChatCompletionRateLimited(t *testing.T) {
	_, p := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
	})
	route := PublicRoute(mustID(t, "opencode", "mimo-v2.5"), model.Capabilities{}, model.AccessFree)
	_, err := p.ChatCompletion(context.Background(), route, provider.NormalizedRequest{
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
	})
	var fe *provider.FailureError
	if !errors.As(err, &fe) || fe.Failure.Class != model.FailureRateLimited {
		t.Fatalf("expected RateLimited, got %v", err)
	}
}

func TestChatCompletionClientCancel(t *testing.T) {
	_, p := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	route := PublicRoute(mustID(t, "opencode", "mimo-v2.5"), model.Capabilities{}, model.AccessFree)
	_, err := p.ChatCompletion(ctx, route, provider.NormalizedRequest{
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected failure on canceled context")
	}
	var fe *provider.FailureError
	if !errors.As(err, &fe) || fe.Failure.Class != model.FailureCanceled {
		t.Fatalf("expected Canceled, got %v", err)
	}
}

func TestUnsupportedExtensionExplicitError(t *testing.T) {
	_, p := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("request should not reach upstream")
	})
	route := PublicRoute(mustID(t, "opencode", "mimo-v2.5"), model.Capabilities{}, model.AccessFree)
	_, err := p.ChatCompletion(context.Background(), route, provider.NormalizedRequest{
		Messages:   []provider.Message{{Role: "user", Content: "hi"}},
		Extensions: map[string]provider.Extension{"some_knob": {Name: "some_knob", Value: 1}},
	})
	var ue *provider.UnsupportedError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnsupportedError, got %v", err)
	}
}

func TestPublicRouteIdentity(t *testing.T) {
	pmid := mustID(t, "opencode", "mimo-v2.5")
	r := PublicRoute(pmid, model.Capabilities{}, model.AccessFree)
	if r.ID != model.RouteID("opencode-public::mimo-v2.5") {
		t.Fatalf("unexpected RouteID %q", r.ID)
	}
	if r.ModelID != pmid {
		t.Fatal("route must reference the ProviderModel")
	}
	if !r.Enabled {
		t.Fatal("route should be enabled")
	}
}

func mustID(t *testing.T, prov, mdl string) model.ProviderModelID {
	t.Helper()
	id, err := model.NewProviderModelID(prov, mdl)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
