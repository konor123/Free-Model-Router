package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net"
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

type fallbackProvider struct {
	models   []string
	behavior func(context.Context, model.ProviderRoute, provider.NormalizedRequest) (provider.ChatStream, error)

	mu    sync.Mutex
	calls []string
}

func (p *fallbackProvider) DiscoverModels(context.Context) (*model.CatalogSnapshot, error) {
	snap := &model.CatalogSnapshot{Models: map[model.ProviderModelID]model.ProviderModel{}}
	for _, name := range p.models {
		id, err := model.NewProviderModelID("opencode", name)
		if err != nil {
			return nil, err
		}
		snap.Models[id] = model.ProviderModel{
			ID: id, UpstreamID: name, Base: model.Capabilities{Streaming: true},
		}
	}
	return snap, nil
}

func (p *fallbackProvider) ChatCompletion(ctx context.Context, route model.ProviderRoute, req provider.NormalizedRequest) (provider.ChatStream, error) {
	p.mu.Lock()
	p.calls = append(p.calls, string(route.ID))
	p.mu.Unlock()
	return p.behavior(ctx, route, req)
}

func (p *fallbackProvider) Calls() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.calls...)
}

func successFallbackStream(content string) provider.ChatStream {
	return &scriptedStream{steps: []scriptedStep{{event: provider.StreamEvent{DeltaText: content, FinishReason: "stop"}}}}
}

type cancelingStream struct {
	cancel context.CancelFunc
}

func (s *cancelingStream) Next(context.Context) (provider.StreamEvent, error) {
	s.cancel()
	return provider.StreamEvent{}, context.Canceled
}

func (s *cancelingStream) Close() error { return nil }

func doFallbackRequest(t *testing.T, g *Gateway, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	g.ChatHandler(w, r)
	return w
}

func TestFallbackRateLimitPrefersSameModelAlternateRoute(t *testing.T) {
	t.Setenv(opencode.AuthRouteEnv, "test-key")
	p := &fallbackProvider{models: []string{"m"}}
	p.behavior = func(_ context.Context, route model.ProviderRoute, _ provider.NormalizedRequest) (provider.ChatStream, error) {
		if strings.HasPrefix(string(route.ID), opencode.PublicRouteName+"::") {
			return nil, provider.NewFailureError(model.NewFailure(model.FailureRateLimited, model.ScopeRoute), errors.New("public quota"))
		}
		return successFallbackStream("auth-ok"), nil
	}
	g, err := NewGatewayWithFailoverPolicy(context.Background(), p, FailoverPolicy{MaxAttempts: 4, Budget: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	w := doFallbackRequest(t, g, `{"model":"fmr/auto","messages":[{"role":"user","content":"x"}]}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "auth-ok") {
		t.Fatalf("rate-limit fallback response = %d %s", w.Code, w.Body.String())
	}
	calls := p.Calls()
	want := []string{"opencode-public::m", "opencode-zen::m"}
	if len(calls) != len(want) || calls[0] != want[0] || calls[1] != want[1] {
		t.Fatalf("rate-limit fallback calls = %v, want %v", calls, want)
	}
}

func TestFallbackUnsupportedSkipsSameModel(t *testing.T) {
	p := &fallbackProvider{models: []string{"a", "b"}}
	p.behavior = func(_ context.Context, route model.ProviderRoute, _ provider.NormalizedRequest) (provider.ChatStream, error) {
		if strings.HasSuffix(string(route.ID), "::a") {
			return nil, &provider.UnsupportedError{Parameter: "response_format", Reason: "model does not support it"}
		}
		return successFallbackStream("model-b"), nil
	}
	g, err := NewGatewayWithFailoverPolicy(context.Background(), p, FailoverPolicy{MaxAttempts: 4, Budget: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	w := doFallbackRequest(t, g, `{"model":"fmr/auto","messages":[{"role":"user","content":"x"}]}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "model-b") {
		t.Fatalf("unsupported fallback response = %d %s", w.Code, w.Body.String())
	}
	calls := p.Calls()
	if len(calls) != 2 || calls[0] != "opencode-public::a" || calls[1] != "opencode-public::b" {
		t.Fatalf("unsupported fallback calls = %v", calls)
	}
}

func TestFallbackAuthFailureSkipsCredential(t *testing.T) {
	t.Setenv(opencode.AuthRouteEnv, "test-key")
	p := &fallbackProvider{models: []string{"a", "b"}}
	p.behavior = func(_ context.Context, route model.ProviderRoute, _ provider.NormalizedRequest) (provider.ChatStream, error) {
		switch {
		case string(route.ID) == "opencode-public::a":
			return nil, provider.NewFailureError(model.NewFailure(model.FailureRateLimited, model.ScopeRoute), errors.New("public quota"))
		case string(route.ID) == "opencode-zen::a":
			return nil, provider.NewFailureError(model.NewFailure(model.FailureAuth, model.ScopeCredential), errors.New("bad key"))
		default:
			return successFallbackStream("public-b"), nil
		}
	}
	g, err := NewGatewayWithFailoverPolicy(context.Background(), p, FailoverPolicy{MaxAttempts: 4, Budget: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	w := doFallbackRequest(t, g, `{"model":"fmr/auto","messages":[{"role":"user","content":"x"}]}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "public-b") {
		t.Fatalf("credential-scope fallback response = %d %s", w.Code, w.Body.String())
	}
	calls := p.Calls()
	want := []string{"opencode-public::a", "opencode-zen::a", "opencode-public::b"}
	if len(calls) != len(want) {
		t.Fatalf("credential-scope fallback calls = %v, want %v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("credential-scope fallback calls = %v, want %v", calls, want)
		}
	}
}

func TestFallbackProtocolSkipsSameModelAlternate(t *testing.T) {
	t.Setenv(opencode.AuthRouteEnv, "test-key")
	p := &fallbackProvider{models: []string{"a", "b"}}
	p.behavior = func(_ context.Context, route model.ProviderRoute, _ provider.NormalizedRequest) (provider.ChatStream, error) {
		if route.ID == "opencode-public::a" {
			return nil, provider.NewFailureError(model.NewFailure(model.FailureProtocol, model.ScopeRoute), errors.New("malformed response"))
		}
		return successFallbackStream("model-b"), nil
	}
	g, err := NewGatewayWithFailoverPolicy(context.Background(), p, FailoverPolicy{MaxAttempts: 4, Budget: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	w := doFallbackRequest(t, g, `{"model":"fmr/auto","messages":[{"role":"user","content":"x"}]}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "model-b") {
		t.Fatalf("protocol fallback response = %d %s", w.Code, w.Body.String())
	}
	calls := p.Calls()
	if len(calls) != 2 || calls[0] != "opencode-public::a" || calls[1] != "opencode-public::b" {
		t.Fatalf("protocol fallback calls = %v", calls)
	}
}

func TestFallbackServerErrorAdvancesToNextProviderModel(t *testing.T) {
	p := &fallbackProvider{models: []string{"a", "b"}}
	p.behavior = func(_ context.Context, route model.ProviderRoute, _ provider.NormalizedRequest) (provider.ChatStream, error) {
		if strings.HasSuffix(string(route.ID), "::a") {
			return nil, provider.NewFailureError(model.NewFailure(model.FailureServerError, model.ScopeProvider), errors.New("upstream 500"))
		}
		return successFallbackStream("model-b"), nil
	}
	g, err := NewGatewayWithFailoverPolicy(context.Background(), p, FailoverPolicy{MaxAttempts: 4, Budget: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	w := doFallbackRequest(t, g, `{"model":"fmr/auto","messages":[{"role":"user","content":"x"}]}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "model-b") {
		t.Fatalf("server-error fallback response = %d %s", w.Code, w.Body.String())
	}
	calls := p.Calls()
	want := []string{"opencode-public::a", "opencode-public::b"}
	if len(calls) != len(want) || calls[0] != want[0] || calls[1] != want[1] {
		t.Fatalf("server-error fallback calls = %v, want %v", calls, want)
	}
}

func TestFallbackRespectsMaxAttempts(t *testing.T) {
	p := &fallbackProvider{models: []string{"a", "b", "c"}}
	p.behavior = func(_ context.Context, _ model.ProviderRoute, _ provider.NormalizedRequest) (provider.ChatStream, error) {
		return nil, provider.NewFailureError(model.NewFailure(model.FailureNetwork, model.ScopeRoute), errors.New("offline"))
	}
	g, err := NewGatewayWithFailoverPolicy(context.Background(), p, FailoverPolicy{MaxAttempts: 2, Budget: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	w := doFallbackRequest(t, g, `{"model":"fmr/auto","messages":[{"role":"user","content":"x"}]}`)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("max-attempt response = %d %s", w.Code, w.Body.String())
	}
	if calls := p.Calls(); len(calls) != 2 {
		t.Fatalf("max-attempt calls = %v, want 2", calls)
	}
}

func TestFallbackBudgetStopsAdditionalAttempts(t *testing.T) {
	p := &fallbackProvider{models: []string{"a", "b"}}
	p.behavior = func(_ context.Context, route model.ProviderRoute, _ provider.NormalizedRequest) (provider.ChatStream, error) {
		if route.ID == "opencode-public::a" {
			time.Sleep(20 * time.Millisecond)
		}
		return nil, provider.NewFailureError(model.NewFailure(model.FailureNetwork, model.ScopeRoute), errors.New("offline"))
	}
	g, err := NewGatewayWithFailoverPolicy(context.Background(), p, FailoverPolicy{MaxAttempts: 4, Budget: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	w := doFallbackRequest(t, g, `{"model":"fmr/auto","messages":[{"role":"user","content":"x"}]}`)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("budget response = %d %s", w.Code, w.Body.String())
	}
	if calls := p.Calls(); len(calls) != 1 {
		t.Fatalf("budget calls = %v, want one attempt", calls)
	}
}

func TestCanceledFailureDoesNotFallback(t *testing.T) {
	p := &fallbackProvider{models: []string{"a", "b"}}
	p.behavior = func(_ context.Context, _ model.ProviderRoute, _ provider.NormalizedRequest) (provider.ChatStream, error) {
		return nil, provider.NewFailureError(model.NewFailure(model.FailureCanceled, model.ScopeRequest), context.Canceled)
	}
	g, err := NewGatewayWithFailoverPolicy(context.Background(), p, FailoverPolicy{MaxAttempts: 4, Budget: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	w := doFallbackRequest(t, g, `{"model":"fmr/auto","messages":[{"role":"user","content":"x"}]}`)
	if calls := p.Calls(); len(calls) != 1 {
		t.Fatalf("canceled calls = %v, want one attempt", calls)
	}
	if w.Body.Len() != 0 {
		t.Fatalf("canceled request must not write a fallback response: %s", w.Body.String())
	}
}

func TestStreamingPreSemanticFailureFallsBack(t *testing.T) {
	p := &fallbackProvider{models: []string{"a", "b"}}
	p.behavior = func(_ context.Context, route model.ProviderRoute, _ provider.NormalizedRequest) (provider.ChatStream, error) {
		if route.ID == "opencode-public::a" {
			return &scriptedStream{steps: []scriptedStep{{err: provider.NewFailureError(model.NewFailure(model.FailureTimeout, model.ScopeRoute), errors.New("timeout"))}}}, nil
		}
		return successFallbackStream("stream-b"), nil
	}
	g, err := NewGatewayWithFailoverPolicy(context.Background(), p, FailoverPolicy{MaxAttempts: 4, Budget: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	w := doFallbackRequest(t, g, `{"model":"fmr/auto","stream":true,"messages":[{"role":"user","content":"x"}]}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "stream-b") {
		t.Fatalf("stream fallback response = %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "timeout") {
		t.Fatalf("pre-semantic failure must not leak into SSE body: %s", w.Body.String())
	}
	calls := p.Calls()
	if len(calls) != 2 || calls[0] != "opencode-public::a" || calls[1] != "opencode-public::b" {
		t.Fatalf("stream fallback calls = %v", calls)
	}
}

func TestStreamingPostTextFailureDoesNotFallback(t *testing.T) {
	p := &fallbackProvider{models: []string{"a", "b"}}
	p.behavior = func(_ context.Context, route model.ProviderRoute, _ provider.NormalizedRequest) (provider.ChatStream, error) {
		if strings.HasSuffix(string(route.ID), "::a") {
			return &scriptedStream{steps: []scriptedStep{
				{event: provider.StreamEvent{DeltaText: "partial"}},
				{err: errors.New("late text failure")},
			}}, nil
		}
		return successFallbackStream("fallback"), nil
	}
	g, err := NewGatewayWithFailoverPolicy(context.Background(), p, FailoverPolicy{MaxAttempts: 4, Budget: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	w := doFallbackRequest(t, g, `{"model":"fmr/auto","stream":true,"messages":[{"role":"user","content":"x"}]}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "partial") || !strings.Contains(w.Body.String(), "late text failure") {
		t.Fatalf("post-text failure response = %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "fallback") {
		t.Fatalf("post-text failure must not mix a fallback stream: %s", w.Body.String())
	}
	if calls := p.Calls(); len(calls) != 1 || calls[0] != "opencode-public::a" {
		t.Fatalf("post-text failure calls = %v, want only model a", calls)
	}
}

func TestStreamingPostToolCallFailureDoesNotFallback(t *testing.T) {
	p := &fallbackProvider{models: []string{"a", "b"}}
	p.behavior = func(_ context.Context, route model.ProviderRoute, _ provider.NormalizedRequest) (provider.ChatStream, error) {
		if strings.HasSuffix(string(route.ID), "::a") {
			return &scriptedStream{steps: []scriptedStep{
				{event: provider.StreamEvent{ToolCalls: []json.RawMessage{json.RawMessage(`{"id":"call-1","type":"function"}`)}}},
				{err: errors.New("late tool failure")},
			}}, nil
		}
		return successFallbackStream("fallback"), nil
	}
	g, err := NewGatewayWithFailoverPolicy(context.Background(), p, FailoverPolicy{MaxAttempts: 4, Budget: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	w := doFallbackRequest(t, g, `{"model":"fmr/auto","stream":true,"messages":[{"role":"user","content":"x"}]}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "call-1") || !strings.Contains(w.Body.String(), "late tool failure") {
		t.Fatalf("post-tool failure response = %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "fallback") {
		t.Fatalf("post-tool failure must not mix a fallback stream: %s", w.Body.String())
	}
	if calls := p.Calls(); len(calls) != 1 || calls[0] != "opencode-public::a" {
		t.Fatalf("post-tool failure calls = %v, want only model a", calls)
	}
}

func TestStreamingHeadersOnlyFailureFallsBack(t *testing.T) {
	p := &fallbackProvider{models: []string{"a", "b"}}
	p.behavior = func(_ context.Context, route model.ProviderRoute, _ provider.NormalizedRequest) (provider.ChatStream, error) {
		if strings.HasSuffix(string(route.ID), "::a") {
			return &scriptedStream{steps: []scriptedStep{
				{event: provider.StreamEvent{}},
				{err: provider.NewFailureError(model.NewFailure(model.FailureTimeout, model.ScopeRoute), errors.New("headers timeout"))},
			}}, nil
		}
		return successFallbackStream("fallback"), nil
	}
	g, err := NewGatewayWithFailoverPolicy(context.Background(), p, FailoverPolicy{MaxAttempts: 4, Budget: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	w := doFallbackRequest(t, g, `{"model":"fmr/auto","stream":true,"messages":[{"role":"user","content":"x"}]}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "fallback") {
		t.Fatalf("headers-only fallback response = %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "headers timeout") {
		t.Fatalf("headers-only failure must not leak upstream error: %s", w.Body.String())
	}
	calls := p.Calls()
	if len(calls) != 2 || calls[0] != "opencode-public::a" || calls[1] != "opencode-public::b" {
		t.Fatalf("headers-only fallback calls = %v", calls)
	}
}

func TestStreamingClientDisconnectDoesNotFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &fallbackProvider{models: []string{"a", "b"}}
	p.behavior = func(_ context.Context, route model.ProviderRoute, _ provider.NormalizedRequest) (provider.ChatStream, error) {
		if strings.HasSuffix(string(route.ID), "::a") {
			return &cancelingStream{cancel: cancel}, nil
		}
		return successFallbackStream("fallback"), nil
	}
	g, err := NewGatewayWithFailoverPolicy(context.Background(), p, FailoverPolicy{MaxAttempts: 4, Budget: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`{"model":"fmr/auto","stream":true,"messages":[{"role":"user","content":"x"}]}`)).WithContext(ctx)
	g.ChatHandler(w, r)

	if calls := p.Calls(); len(calls) != 1 || calls[0] != "opencode-public::a" {
		t.Fatalf("client disconnect calls = %v, want only model a", calls)
	}
	if w.Body.Len() != 0 {
		t.Fatalf("client disconnect must not write a fallback response: %s", w.Body.String())
	}
}

func TestGatewayRanksCandidatesByProbeTTFT(t *testing.T) {
	p := &fallbackProvider{models: []string{"a", "b"}}
	p.behavior = func(_ context.Context, _ model.ProviderRoute, _ provider.NormalizedRequest) (provider.ChatStream, error) {
		return successFallbackStream("ranked"), nil
	}
	g, err := NewGatewayWithFailoverPolicy(context.Background(), p, FailoverPolicy{MaxAttempts: 1, Budget: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	g.latency.RecordProbe("opencode-public::a", 200)
	g.latency.RecordProbe("opencode-public::b", 25)
	w := doFallbackRequest(t, g, `{"model":"fmr/auto","messages":[{"role":"user","content":"x"}]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("ranked response = %d %s", w.Code, w.Body.String())
	}
	calls := p.Calls()
	if len(calls) != 1 || calls[0] != "opencode-public::b" {
		t.Fatalf("ranked first call = %v, want b", calls)
	}
}

func TestFallbackDecisionStopsOnUnclassifiedError(t *testing.T) {
	failure, action := fallbackDecision(errors.New("ambiguous provider error"))
	if failure.Class != model.FailureUnknown || failure.Scope != model.ScopeRequest || action != model.ActionStop {
		t.Fatalf("unclassified error decision = %+v, %s", failure, action)
	}
}

func TestFallbackDecisionRetriesExplicitNetworkError(t *testing.T) {
	failure, action := fallbackDecision(&net.DNSError{Err: "connection refused", Name: "upstream"})
	if failure.Class != model.FailureNetwork || failure.Scope != model.ScopeRoute || action != model.ActionNextRoute {
		t.Fatalf("network error decision = %+v, %s", failure, action)
	}
}

func TestAttemptBudgetCommitReportsWhetherItWonTheDeadlineRace(t *testing.T) {
	budget := newAttemptBudget(context.Background(), time.Now().Add(time.Second))
	defer budget.Close()
	if !budget.Commit() {
		t.Fatal("commit should win before the deadline")
	}
	if budget.Expired() || budget.ctx.Err() != nil {
		t.Fatal("committed budget must remain usable")
	}
}
