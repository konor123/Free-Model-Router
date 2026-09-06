package gemini

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

// newMockServer builds a test Gemini OpenAI-compatible endpoint.
func newMockServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *Provider) {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	p := New(ts.URL)
	return ts, p
}

func TestDiscoverModelsCatalog(t *testing.T) {
	t.Setenv(EnvKey, "test-key")
	_, p := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("missing bearer auth: %q", got)
		}
		streaming := true
		tools := true
		vision := true
		json.NewEncoder(w).Encode(catalogResponse{Data: []catalogEntry{
			{ID: "gemini-3.8-flash", ContextLength: 1048576, Streaming: &streaming, Tools: &tools, Vision: &vision},
			{ID: "gemini-2.5-pro"},
		}})
	})
	snap, err := p.DiscoverModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(snap.Models))
	}
	if snap.CreatedAt.IsZero() {
		t.Fatal("discovered catalog must record its creation time")
	}
	id := mustID(t, "gemini", "gemini-3.8-flash")
	pm, ok := snap.Models[id]
	if !ok {
		t.Fatalf("missing %q in catalog", id)
	}
	if pm.Base.Tools != true || pm.Base.Streaming != true || pm.Base.Vision != true {
		t.Fatalf("capabilities not normalized: %+v", pm.Base)
	}
	if pm.Base.ContextLength != 1048576 {
		t.Fatalf("context length not normalized: %d", pm.Base.ContextLength)
	}
	if !pm.Base.IsUnknown(model.CapStructuredOutput) || !pm.Base.IsUnknown(model.CapReasoning) {
		t.Fatalf("omitted feature metadata must remain unknown: %+v", pm.Base)
	}
	if pm.UpstreamID != "gemini-3.8-flash" {
		t.Fatalf("upstream ID mismatch: %q", pm.UpstreamID)
	}
}

func TestDiscoverModelsMalformed(t *testing.T) {
	t.Setenv(EnvKey, "test-key")
	_, p := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	})
	if _, err := p.DiscoverModels(context.Background()); err == nil {
		t.Fatal("expected protocol failure for malformed catalog")
	}
}

func TestDiscoverModelsServerError(t *testing.T) {
	t.Setenv(EnvKey, "test-key")
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
	t.Setenv(EnvKey, "test-key")
	_, p := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("missing bearer auth: %q", got)
		}
		json.NewEncoder(w).Encode(chatResponse{
			Choices: []chatChoice{
				{Message: chatMessage{Content: "hello world"}, FinishReason: "stop"},
			},
		})
	})
	route := AIRoute(mustID(t, "gemini", "gemini-3.8-flash"), "gemini-3.8-flash", model.Capabilities{})
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
	t.Setenv(EnvKey, "test-key")
	sse := "data: {\"choices\":[{\"delta\":{\"content\":\"hel\"}}]}\n\n" +
		": heartbeat\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n" +
		"data: [DONE]\n\n"
	_, p := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(sse))
	})
	route := AIRoute(mustID(t, "gemini", "gemini-3.8-flash"), "gemini-3.8-flash", model.Capabilities{})
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
	if count != 2 {
		t.Fatalf("expected 2 events (2 text deltas), got %d", count)
	}
}

func TestChatCompletionRateLimited(t *testing.T) {
	t.Setenv(EnvKey, "test-key")
	_, p := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
	})
	route := AIRoute(mustID(t, "gemini", "gemini-3.8-flash"), "gemini-3.8-flash", model.Capabilities{})
	_, err := p.ChatCompletion(context.Background(), route, provider.NormalizedRequest{
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
	})
	var fe *provider.FailureError
	if !errors.As(err, &fe) || fe.Failure.Class != model.FailureRateLimited {
		t.Fatalf("expected RateLimited, got %v", err)
	}
}

func TestChatCompletionAuthFailure(t *testing.T) {
	t.Setenv(EnvKey, "test-key")
	_, p := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	})
	route := AIRoute(mustID(t, "gemini", "gemini-3.8-flash"), "gemini-3.8-flash", model.Capabilities{})
	_, err := p.ChatCompletion(context.Background(), route, provider.NormalizedRequest{
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
	})
	var fe *provider.FailureError
	if !errors.As(err, &fe) || fe.Failure.Class != model.FailureAuth || fe.Failure.Scope != model.ScopeCredential {
		t.Fatalf("expected Auth/Credential, got %v", err)
	}
}

func TestChatCompletionClientCancel(t *testing.T) {
	t.Setenv(EnvKey, "test-key")
	_, p := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	route := AIRoute(mustID(t, "gemini", "gemini-3.8-flash"), "gemini-3.8-flash", model.Capabilities{})
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
	t.Setenv(EnvKey, "test-key")
	_, p := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("request should not reach upstream")
	})
	route := AIRoute(mustID(t, "gemini", "gemini-3.8-flash"), "gemini-3.8-flash", model.Capabilities{})
	_, err := p.ChatCompletion(context.Background(), route, provider.NormalizedRequest{
		Messages:   []provider.Message{{Role: "user", Content: "hi"}},
		Extensions: map[string]provider.Extension{"some_knob": {Name: "some_knob", Value: 1}},
	})
	var ue *provider.UnsupportedError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnsupportedError, got %v", err)
	}
}

func TestSSEPreservesToolCallsAndReasoning(t *testing.T) {
	t.Setenv(EnvKey, "test-key")
	sse := "data: {\"choices\":[{\"index\":2,\"delta\":{\"reasoning_content\":\"think\",\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"lookup\",\"arguments\":\"{\\\"q\\\":1}\"}}]}}]}\n\n"
	_, p := newMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sse))
	})
	stream, err := p.ChatCompletion(context.Background(), AIRoute(mustID(t, "gemini", "m"), "m", model.Capabilities{}), provider.NormalizedRequest{Stream: true, Messages: []provider.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	ev, err := stream.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ev.ChoiceIndex != 2 || ev.ReasoningContent != "think" || len(ev.ToolCalls) != 1 || !strings.Contains(string(ev.ToolCalls[0]), "call_1") {
		t.Fatalf("tool/reasoning delta not preserved: %+v", ev)
	}
}
