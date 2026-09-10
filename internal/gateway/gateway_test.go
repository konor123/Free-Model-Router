package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/konor123/Free-Model-Router/internal/model"
	"github.com/konor123/Free-Model-Router/internal/provider"
	"github.com/konor123/Free-Model-Router/internal/providers/opencode"
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
		snap.Models[pmid] = model.ProviderModel{ID: pmid, UpstreamID: e.ID, Base: model.Capabilities{Streaming: true}}
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
	if !strings.Contains(w.Body.String(), `"fmr/auto"`) {
		t.Fatal("missing fmr/auto in /v1/models")
	}
	if !strings.Contains(w.Body.String(), `"owned_by":"fmr"`) {
		t.Fatalf("models must use FMR ownership metadata: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "opencode/mimo-v2.5") {
		t.Fatal("missing discovered model in /v1/models")
	}
}

func TestRefreshCatalogSelectsLexicographicallyFirstModelForAuto(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"zeta"},{"id":"alpha"}]}`))
	}))
	defer backend.Close()

	g, err := NewGateway(context.Background(), fakeProvider{base: backend.URL})
	if err != nil {
		t.Fatal(err)
	}
	want, err := model.NewProviderModelID("opencode", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if g.autoPick != want {
		t.Fatalf("auto model = %q, want %q", g.autoPick, want)
	}
	for i := 0; i < 10; i++ {
		if err := g.RefreshCatalog(context.Background()); err != nil {
			t.Fatal(err)
		}
		if g.autoPick != want {
			t.Fatalf("refresh %d auto model = %q, want %q", i, g.autoPick, want)
		}
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
	body := `{"model":"fmr/auto","messages":[{"role":"user","content":"ping"}]}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	g.ChatHandler(w, r)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "pong") {
		t.Fatalf("missing content: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "chatcmpl-fmr") {
		t.Fatalf("response must use the FMR completion id: %s", w.Body.String())
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

func TestRefreshCatalogProjectsRoutesAndAutomaticPool(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"mimo-v2.5"},{"id":"glm-5.3-flash"}]}`))
	}))
	defer backend.Close()
	g, err := NewGateway(context.Background(), fakeProvider{base: backend.URL})
	if err != nil {
		t.Fatal(err)
	}
	if len(g.routes) != 2 {
		t.Fatalf("projected route models = %d, want 2", len(g.routes))
	}
	for id, routes := range g.routes {
		if len(routes) == 0 || routes[0].Provider != "opencode" || routes[0].UpstreamModelID == "" {
			t.Fatalf("invalid projected route for %s: %+v", id, routes)
		}
		if !g.pool.Snapshot().Contains(id) {
			t.Fatalf("automatic pool missing %s", id)
		}
	}
}

func TestModelsListReflectsLivePool(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"first"},{"id":"second"}]}`))
	}))
	defer backend.Close()
	g, err := NewGateway(context.Background(), fakeProvider{base: backend.URL})
	if err != nil {
		t.Fatal(err)
	}
	second, _ := model.NewProviderModelID("opencode", "second")
	g.pool.Deselect([]model.ProviderModelID{second})
	w := httptest.NewRecorder()
	g.ModelsHandler(w, httptest.NewRequest("GET", "/v1/models", nil))
	if strings.Contains(w.Body.String(), string(second)) || !strings.Contains(w.Body.String(), "opencode/first") {
		t.Fatalf("models must reflect live pool: %s", w.Body.String())
	}
}

func TestRefreshCatalogKeepsAuthRouteFreeAndPublicIsolated(t *testing.T) {
	t.Setenv(opencode.AuthRouteEnv, "test-key")
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	defer backend.Close()
	g, err := NewGateway(context.Background(), fakeProvider{base: backend.URL})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := model.NewProviderModelID("opencode", "m")
	routes := g.routes[id]
	if len(routes) != 2 || routes[0].EffectiveAccess() != model.AccessFree || routes[1].EffectiveAccess() != model.AccessFree {
		t.Fatalf("expected isolated public/free and auth/free routes: %+v", routes)
	}
	g.health.RouteFailure(string(routes[0].ID), time.Minute)
	if !g.health.Available(string(routes[1].ID)) {
		t.Fatal("public failure must not affect auth route health")
	}
}

func TestResolveCandidateFiltersToolsAndVision(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"tools"},{"id":"vision"}]}`))
	}))
	defer backend.Close()
	g, err := NewGateway(context.Background(), fakeProvider{base: backend.URL})
	if err != nil {
		t.Fatal(err)
	}
	toolsID, _ := model.NewProviderModelID("opencode", "tools")
	visionID, _ := model.NewProviderModelID("opencode", "vision")
	tools := g.catalog.Models[toolsID]
	tools.Base = model.Capabilities{Tools: true}
	g.catalog.Models[toolsID] = tools
	vision := g.catalog.Models[visionID]
	vision.Base = model.Capabilities{Vision: true}
	g.catalog.Models[visionID] = vision
	if pick, _, ok := g.resolveCandidate(chatCompletionRequest{Tools: []gatewayTool{{}}}); !ok || pick != toolsID {
		t.Fatalf("tools request selected %q, want %q", pick, toolsID)
	}
	if pick, _, ok := g.resolveCandidate(chatCompletionRequest{Messages: []provider.Message{{Role: "user", Content: []any{map[string]any{"type": "image_url"}}}}}); !ok || pick != visionID {
		t.Fatalf("vision request selected %q, want %q", pick, visionID)
	}
}

func TestChatCompletionRejectsUnknownParameter(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"mimo-v2.5"}]}`))
	}))
	defer backend.Close()
	g, err := NewGateway(context.Background(), fakeProvider{base: backend.URL})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	g.ChatHandler(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"messages":[{"role":"user","content":"x"}],"unknown_knob":true}`)))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "unsupported parameter unknown_knob") {
		t.Fatalf("unknown parameter should be explicitly rejected: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"type":"fmr_error"`) {
		t.Fatalf("error response must use the FMR error type: %s", w.Body.String())
	}
}

func TestResolveCandidateFiltersMaxOutput(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"a-small"},{"id":"b-large"}]}`))
	}))
	defer backend.Close()
	g, err := NewGateway(context.Background(), fakeProvider{base: backend.URL})
	if err != nil {
		t.Fatal(err)
	}
	smallID, _ := model.NewProviderModelID("opencode", "a-small")
	largeID, _ := model.NewProviderModelID("opencode", "b-large")
	small := g.catalog.Models[smallID]
	small.Base = model.Capabilities{Streaming: true, MaxOutput: 32}
	g.catalog.Models[smallID] = small
	large := g.catalog.Models[largeID]
	large.Base = model.Capabilities{Streaming: true, MaxOutput: 256}
	g.catalog.Models[largeID] = large

	pick, _, ok := g.resolveCandidate(chatCompletionRequest{MaxTokens: 128})
	if !ok || pick != largeID {
		t.Fatalf("max-output request selected %q, want %q", pick, largeID)
	}
}

func TestChatCompletionStreaming(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Write([]byte(`{"data":[{"id":"mimo-v2.5"}]}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"index\":4,\"delta\":{\"content\":\"he\"}}]}\n\n" +
			"data: {\"choices\":[{\"index\":4,\"delta\":{\"content\":\"y\"}}]}\n\n" +
			"data: [DONE]\n\n"))
	}))
	defer backend.Close()

	g, err := NewGateway(context.Background(), streamingFakeProvider{base: backend.URL})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"fmr/auto","stream":true,"messages":[{"role":"user","content":"x"}]}`))
	g.ChatHandler(w, r)

	if !strings.Contains(w.Body.String(), `"content":"he"`) {
		t.Fatalf("missing delta: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"id":"chatcmpl-fmr"`) {
		t.Fatalf("stream must use the FMR completion id: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"index":4`) {
		t.Fatalf("missing choice index: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "[DONE]") {
		t.Fatal("missing [DONE]")
	}
}

func TestEmptyCatalogKeepsManagementStateAvailable(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[]}`))
	}))
	defer backend.Close()
	gateway, err := NewGateway(context.Background(), fakeProvider{base: backend.URL})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot := gateway.ControlSnapshot(); len(snapshot.Models) != 0 {
		t.Fatalf("snapshot = %#v", snapshot)
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
			Index int `json:"index"`
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
	return provider.StreamEvent{ChoiceIndex: raw.Choices[0].Index, DeltaText: raw.Choices[0].Delta.Content}, nil
}
func (s *sseFakeStream) Close() error { return nil }

func (f streamingFakeProvider) ChatCompletion(ctx context.Context, route model.ProviderRoute, req provider.NormalizedRequest) (provider.ChatStream, error) {
	resp, err := http.Post(f.base+"/chat/completions", "application/json", nil)
	if err != nil {
		return nil, err
	}
	return &sseFakeStream{rdr: bufio.NewReader(resp.Body)}, nil
}

type scriptedStep struct {
	event provider.StreamEvent
	err   error
}

type scriptedStream struct {
	steps []scriptedStep
	index int
	delay time.Duration
}

func (s *scriptedStream) Next(context.Context) (provider.StreamEvent, error) {
	if s.index >= len(s.steps) {
		return provider.StreamEvent{}, io.EOF
	}
	step := s.steps[s.index]
	s.index++
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	return step.event, step.err
}

func (s *scriptedStream) Close() error { return nil }

type scriptedProvider struct {
	streamFactory func() provider.ChatStream
}

func (p scriptedProvider) DiscoverModels(context.Context) (*model.CatalogSnapshot, error) {
	id, err := model.NewProviderModelID("opencode", "scripted")
	if err != nil {
		return nil, err
	}
	return &model.CatalogSnapshot{
		Models: map[model.ProviderModelID]model.ProviderModel{
			id: {ID: id, UpstreamID: "scripted", Base: model.Capabilities{Streaming: true}},
		},
	}, nil
}

func (p scriptedProvider) ChatCompletion(context.Context, model.ProviderRoute, provider.NormalizedRequest) (provider.ChatStream, error) {
	return p.streamFactory(), nil
}

func newScriptedGateway(t *testing.T, factory func() provider.ChatStream) *Gateway {
	t.Helper()
	g, err := NewGateway(context.Background(), scriptedProvider{streamFactory: factory})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestStreamingFailureBeforeSemanticDoesNotCommit(t *testing.T) {
	g := newScriptedGateway(t, func() provider.ChatStream {
		return &scriptedStream{steps: []scriptedStep{{err: provider.NewFailureError(
			model.NewFailure(model.FailureTimeout, model.ScopeRoute), errors.New("upstream timeout"))}}}
	})
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(
		`{"model":"fmr/auto","stream":true,"messages":[{"role":"user","content":"x"}]}`))
	g.ChatHandler(w, r)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("pre-semantic failure status = %d, body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatal("pre-semantic failure must not commit SSE headers")
	}
	state := g.health.Snapshot("opencode-public::scripted")
	if state.ConsecutiveFailures != 1 {
		t.Fatalf("expected route failure accounting, got %+v", state)
	}
}

func TestStreamingFailureAfterSemanticCommitsAndRecordsMetrics(t *testing.T) {
	g := newScriptedGateway(t, func() provider.ChatStream {
		return &scriptedStream{steps: []scriptedStep{
			{event: provider.StreamEvent{DeltaText: "hello"}},
			{err: errors.New("connection reset")},
		}, delay: time.Millisecond}
	})
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(
		`{"model":"fmr/auto","stream":true,"messages":[{"role":"user","content":"x"}]}`))
	g.ChatHandler(w, r)

	if w.Code != http.StatusOK || !strings.Contains(w.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("committed stream status/headers = %d/%q", w.Code, w.Header().Get("Content-Type"))
	}
	if !strings.Contains(w.Body.String(), `"content":"hello"`) || !strings.Contains(w.Body.String(), "connection reset") {
		t.Fatalf("committed stream must preserve data and terminal error: %s", w.Body.String())
	}
	state := g.health.Snapshot("opencode-public::scripted")
	if state.ConsecutiveFailures != 1 {
		t.Fatalf("expected post-commit route failure accounting, got %+v", state)
	}
	stats := g.latency.For("opencode-public::scripted")
	if stats.RequestTTFT.Samples() != 1 || stats.RequestTotal.Samples() != 1 {
		t.Fatalf("expected streaming TTFT and total metrics, got ttft=%d total=%d", stats.RequestTTFT.Samples(), stats.RequestTotal.Samples())
	}
}

func TestNonStreamingRecordsTTFTAndTotalMetrics(t *testing.T) {
	g := newScriptedGateway(t, func() provider.ChatStream {
		return &scriptedStream{steps: []scriptedStep{
			{event: provider.StreamEvent{DeltaText: "ok", FinishReason: "stop"}},
		}, delay: time.Millisecond}
	})
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(
		`{"model":"fmr/auto","messages":[{"role":"user","content":"x"}]}`))
	g.ChatHandler(w, r)

	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "ok") {
		t.Fatalf("non-streaming response = %d %s", w.Code, w.Body.String())
	}
	stats := g.latency.For("opencode-public::scripted")
	if stats.RequestTTFT.Samples() != 1 || stats.RequestTotal.Samples() != 1 {
		t.Fatalf("expected non-streaming TTFT and total metrics, got ttft=%d total=%d", stats.RequestTTFT.Samples(), stats.RequestTotal.Samples())
	}
	if state := g.health.Snapshot("opencode-public::scripted"); state.ConsecutiveFailures != 0 {
		t.Fatalf("successful request must clear route failures, got %+v", state)
	}
}

func TestProbeCycleUsesLivePoolAndHTTPProvider(t *testing.T) {
	t.Setenv(opencode.AuthRouteEnv, "")
	var mu sync.Mutex
	counts := map[string]int{}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"first"},{"id":"second"}]}`))
		case "/chat/completions":
			var payload struct {
				Model  string `json:"model"`
				Stream bool   `json:"stream"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("decode probe payload: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			mu.Lock()
			counts[payload.Model]++
			mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"pong\"}}]}\n\ndata: [DONE]\n\n"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer backend.Close()

	g, err := NewGateway(context.Background(), opencode.New(backend.URL))
	if err != nil {
		t.Fatal(err)
	}
	g.ProbeOnce(context.Background())
	mu.Lock()
	firstCount := counts["first"]
	secondCount := counts["second"]
	mu.Unlock()
	if firstCount != 1 || secondCount != 1 {
		t.Fatalf("initial probe targets = first:%d second:%d", firstCount, secondCount)
	}
	secondID, _ := model.NewProviderModelID("opencode", "second")
	g.pool.Deselect([]model.ProviderModelID{secondID})
	g.ProbeOnce(context.Background())
	mu.Lock()
	firstCount = counts["first"]
	secondCount = counts["second"]
	mu.Unlock()
	if firstCount != 2 || secondCount != 1 {
		t.Fatalf("probe target did not follow live pool: first:%d second:%d", firstCount, secondCount)
	}
}
