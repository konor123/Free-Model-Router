package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/konor123/Free-Model-Router/internal/model"
	"github.com/konor123/Free-Model-Router/internal/provider"
)

// mockOpenCode is a controllable OpenCode-compatible backend.
type mockOpenCode struct {
	models      string
	chatHandler http.HandlerFunc
}

func (m *mockOpenCode) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/models" {
		w.Write([]byte(m.models))
		return
	}
	if m.chatHandler != nil {
		m.chatHandler(w, r)
		return
	}
	w.WriteHeader(500)
}

func newTestGateway(t *testing.T, backend *httptest.Server) (*Gateway, error) {
	return NewGateway(context.Background(), fakeProvider{base: backend.URL})
}

// fakeProvider adapts the mock backend into a provider.Provider.
type fakeProvider struct {
	base string
}

func backendOf(backend *httptest.Server) *httptest.Server { return backend }

func backendURL(t *testing.T, s *httptest.Server) string { return s.URL }

func (f fakeProvider) DiscoverModels(ctx context.Context) (*model.CatalogSnapshot, error) {
	resp, err := http.Get(f.base + "/models")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var raw struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	snap := &model.CatalogSnapshot{Models: map[model.ProviderModelID]model.ProviderModel{}}
	for _, e := range raw.Data {
		pmid, err := model.NewProviderModelID("opencode", e.ID)
		if err != nil {
			continue
		}
		snap.Models[pmid] = model.ProviderModel{ID: pmid, Access: model.AccessFree}
	}
	return snap, nil
}

func (f fakeProvider) ChatCompletion(ctx context.Context, route model.ProviderRoute, req provider.NormalizedRequest) (provider.ChatStream, error) {
	resp, err := http.Post(f.base+"/chat/completions", "application/json", strings.NewReader("{}"))
	if err != nil {
		if ctx.Err() != nil {
			return nil, provider.NewFailureError(model.NewFailure(model.FailureCanceled, model.ScopeRequest), ctx.Err())
		}
		return nil, provider.NewFailureError(model.NewFailure(model.FailureNetwork, model.ScopeRoute), err)
	}
	defer resp.Body.Close()
	var raw struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			Delta string `json:"delta"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, provider.NewFailureError(model.NewFailure(model.FailureProtocol, model.ScopeRoute), err)
	}
	content := ""
	if len(raw.Choices) > 0 {
		content = raw.Choices[0].Delta
		if content == "" {
			content = raw.Choices[0].Message.Content
		}
	}
	return &oneShotStream{content: content}, nil
}

type oneShotStream struct {
	content string
	done    bool
}

func (o *oneShotStream) Next(context.Context) (provider.StreamEvent, error) {
	if o.done {
		return provider.StreamEvent{}, io.EOF
	}
	o.done = true
	return provider.StreamEvent{DeltaText: o.content, FinishReason: "stop"}, nil
}
func (o *oneShotStream) Close() error { return nil }

func TestModelsListContainsAutoAndDiscovered(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"mimo-v2.5"}]}`))
	}))
	defer backend.Close()

	g, err := NewGateway(context.Background(), fakeProvider{base: backend.URL})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	g.ModelsHandler(w, httptest.NewRequest("GET", "/v1/models", nil))
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"afm/auto"`) {
		t.Fatal("missing afm/auto in /v1/models")
	}
	if !strings.Contains(w.Body.String(), "opencode/mimo-v2.5") {
		t.Fatal("missing discovered model in /v1/models")
	}
}

func TestChatCompletionEndToEndNonStreaming(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Write([]byte(`{"data":[{"id":"mimo-v2.5"}]}`))
			return
		}
		w.Write([]byte(`{"choices":[{"delta":"pong","finish_reason":"stop"}]}`))
	}))
	defer backend.Close()

	g, err := NewGateway(context.Background(), fakeProvider{base: backend.URL})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"model":"afm/auto","messages":[{"role":"user","content":"ping"}]}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	g.ChatHandler(w, r)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "pong") {
		t.Fatalf("missing content: %s", w.Body.String())
	}
}

func TestChatCompletionUnknownModelRejected(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"mimo-v2.5"}]}`))
	}))
	defer backend.Close()

	g, err := NewGateway(context.Background(), fakeProvider{base: backend.URL})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"nope","messages":[{"role":"user","content":"x"}]}`))
	g.ChatHandler(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestChatCompletionStreaming(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Write([]byte(`{"data":[{"id":"mimo-v2.5"}]}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"he\"}}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{\"content\":\"y\"}}]}\n\n" +
			"data: [DONE]\n\n"))
	}))
	defer backend.Close()

	g, err := NewGateway(context.Background(), streamingFakeProvider{base: backend.URL})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"afm/auto","stream":true,"messages":[{"role":"user","content":"x"}]}`))
	g.ChatHandler(w, r)

	if !strings.Contains(w.Body.String(), `"content":"he"`) {
		t.Fatalf("missing delta: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "[DONE]") {
		t.Fatal("missing [DONE]")
	}
}

func TestEmptyCatalogFails(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[]}`))
	}))
	defer backend.Close()
	if _, err := NewGateway(context.Background(), fakeProvider{base: backend.URL}); err == nil {
		t.Fatal("expected error on empty catalog")
	}
}

// streamingFakeProvider returns an SSE-based ChatStream.
type streamingFakeProvider struct{ base string }

func (f streamingFakeProvider) DiscoverModels(ctx context.Context) (*model.CatalogSnapshot, error) {
	return fakeProvider{base: f.base}.DiscoverModels(ctx)
}

type sseFakeStream struct {
	rdr *bufio.Reader
}

func (s *sseFakeStream) Next(ctx context.Context) (provider.StreamEvent, error) {
	line, err := s.rdr.ReadString('\n')
	if err != nil {
		if err == io.EOF {
			return provider.StreamEvent{}, io.EOF
		}
		return provider.StreamEvent{}, err
	}
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "data: ") {
		return s.Next(ctx)
	}
	data := strings.TrimPrefix(line, "data: ")
	if data == "[DONE]" {
		return provider.StreamEvent{FinishReason: "stop"}, nil
	}
	var raw struct {
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		return provider.StreamEvent{}, err
	}
	if len(raw.Choices) == 0 || raw.Choices[0].Delta.Content == "" {
		return s.Next(ctx)
	}
	return provider.StreamEvent{DeltaText: raw.Choices[0].Delta.Content}, nil
}
func (s *sseFakeStream) Close() error { return nil }

func (f streamingFakeProvider) ChatCompletion(ctx context.Context, route model.ProviderRoute, req provider.NormalizedRequest) (provider.ChatStream, error) {
	resp, err := http.Post(f.base+"/chat/completions", "application/json", nil)
	if err != nil {
		return nil, err
	}
	return &sseFakeStream{rdr: bufio.NewReader(resp.Body)}, nil
}
