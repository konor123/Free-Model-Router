package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/konor123/Free-Model-Router/internal/model"
	"github.com/konor123/Free-Model-Router/internal/provider"
	"github.com/konor123/Free-Model-Router/internal/usage"
)

func TestUsageRecordsOneRequestAcrossFallbackAttempts(t *testing.T) {
	p := &fallbackProvider{models: []string{"a", "b"}}
	p.behavior = func(_ context.Context, route model.ProviderRoute, _ provider.NormalizedRequest) (provider.ChatStream, error) {
		if strings.HasSuffix(string(route.ID), "::a") {
			return nil, provider.NewHTTPFailureError(model.NewFailure(model.FailureNetwork, model.ScopeRoute), http.StatusBadGateway, errors.New("offline"))
		}
		return &scriptedStream{steps: []scriptedStep{{event: provider.StreamEvent{
			DeltaText: "ok", FinishReason: "stop",
			Usage: &provider.TokenUsage{Prompt: 4, Completion: 6, Total: 10},
		}}}}, nil
	}
	g, err := NewGatewayWithFailoverPolicy(context.Background(), p, FailoverPolicy{MaxAttempts: 4, Budget: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	store := usage.NewMemory()
	g.SetUsageSink(store)
	w := doFallbackRequest(t, g, `{"model":"fmr/auto","messages":[{"role":"user","content":"private prompt text"}]}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "ok") {
		t.Fatalf("fallback response = %d %s", w.Code, w.Body.String())
	}
	records, err := store.List(usage.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("request record count = %d, want one", len(records))
	}
	record := records[0]
	if !record.Fallback || len(record.Attempts) != 2 || record.Result != usage.ResultSuccess {
		t.Fatalf("fallback usage record = %+v", record)
	}
	if record.TotalTokens.Total != 10 || record.Attempts[0].HTTPStatus != http.StatusBadGateway || !record.Attempts[1].Committed {
		t.Fatalf("fallback usage accounting = %+v", record)
	}
	if strings.Contains(string(mustJSON(record)), "private prompt text") {
		t.Fatal("usage record retained request content")
	}
}

func TestUsageRecordsCommittedStreamingFailureAsPartial(t *testing.T) {
	p := &fallbackProvider{models: []string{"a", "b"}}
	p.behavior = func(_ context.Context, route model.ProviderRoute, _ provider.NormalizedRequest) (provider.ChatStream, error) {
		if strings.HasSuffix(string(route.ID), "::a") {
			return &scriptedStream{steps: []scriptedStep{
				{event: provider.StreamEvent{DeltaText: "partial"}},
				{err: errors.New("late failure")},
			}}, nil
		}
		return successFallbackStream("must-not-run"), nil
	}
	g, err := NewGatewayWithFailoverPolicy(context.Background(), p, FailoverPolicy{MaxAttempts: 4, Budget: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	store := usage.NewMemory()
	g.SetUsageSink(store)
	w := doFallbackRequest(t, g, `{"model":"fmr/auto","stream":true,"messages":[{"role":"user","content":"x"}]}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "partial") {
		t.Fatalf("stream response = %d %s", w.Code, w.Body.String())
	}
	records, err := store.List(usage.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Result != usage.ResultPartial || len(records[0].Attempts) != 1 || !records[0].Attempts[0].Committed {
		t.Fatalf("committed failure usage record = %+v", records)
	}
	if calls := p.Calls(); len(calls) != 1 {
		t.Fatalf("committed failure unexpectedly fell back: %v", calls)
	}
}

func mustJSON(v any) []byte {
	data, _ := json.Marshal(v)
	return data
}
