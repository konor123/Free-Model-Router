package usage_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/konor123/Free-Model-Router/internal/model"
	"github.com/konor123/Free-Model-Router/internal/usage"
)

func TestStoreSeparatesRequestAndAttemptsWithoutSensitiveFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.jsonl")
	store, err := usage.New(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	record := usage.RequestRecord{
		ID:          "req-1",
		StartedAt:   now,
		CompletedAt: now.Add(120 * time.Millisecond),
		FinalModel:  model.ProviderModelID("opencode/mimo-v2.5"),
		FinalRoute:  model.RouteID("opencode-public::mimo-v2.5"),
		Result:      usage.ResultSuccess,
		Attempts: []usage.AttemptRecord{
			{
				Index:          1,
				ProviderModel:  model.ProviderModelID("opencode/mimo-v2.5"),
				Route:          model.RouteID("opencode-public::mimo-v2.5"),
				FailureClass:   model.FailureNetwork,
				FailureScope:   model.ScopeRoute,
				TTFTMs:         10,
				TotalLatencyMs: 40,
				Committed:      false,
				HTTPStatus:     502,
			},
			{
				Index:          2,
				ProviderModel:  model.ProviderModelID("opencode/mimo-v2.5"),
				Route:          model.RouteID("opencode-zen::mimo-v2.5"),
				TTFTMs:         15,
				TotalLatencyMs: 80,
				Committed:      true,
				HTTPStatus:     200,
				Tokens:         usage.TokenUsage{Prompt: 4, Completion: 6, Total: 10},
			},
		},
		TotalTokens: usage.TokenUsage{Prompt: 4, Completion: 6, Total: 10},
		Fallback:    true,
	}
	if err := store.Append(record); err != nil {
		t.Fatal(err)
	}
	got, err := store.List(usage.Query{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(got[0].Attempts) != 2 || got[0].TotalTokens.Total != 10 {
		t.Fatalf("unexpected request/attempt records: %+v", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	serialized := string(data)
	for _, forbidden := range []string{"messages", "response", "authorization", "api-key", "super-secret", "prompt text"} {
		if strings.Contains(strings.ToLower(serialized), strings.ToLower(forbidden)) {
			t.Fatalf("usage log contains forbidden field %q: %s", forbidden, serialized)
		}
	}
}

func TestStoreRetentionByAgeAndSize(t *testing.T) {
	store := usage.NewMemory()
	now := time.Now().UTC()
	old := usage.RequestRecord{
		ID:          "old",
		StartedAt:   now.Add(-31 * 24 * time.Hour),
		CompletedAt: now.Add(-31 * 24 * time.Hour),
		Result:      usage.ResultSuccess,
	}
	fresh := usage.RequestRecord{
		ID:          "fresh",
		StartedAt:   now,
		CompletedAt: now,
		Result:      usage.ResultSuccess,
	}
	if err := store.Append(old); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(fresh); err != nil {
		t.Fatal(err)
	}
	if err := store.Prune(now); err != nil {
		t.Fatal(err)
	}
	got, err := store.List(usage.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "fresh" {
		t.Fatalf("age retention failed: %+v", got)
	}
	if usage.MaxBytes != 20*1024*1024 || usage.MaxAge != 30*24*time.Hour {
		t.Fatalf("retention contract changed: bytes=%d age=%s", usage.MaxBytes, usage.MaxAge)
	}
}

func TestStorePersistsAndReopens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.jsonl")
	store, err := usage.New(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(usage.RequestRecord{ID: "persisted", Result: usage.ResultSuccess}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := usage.New(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.List(usage.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "persisted" || got[0].Result != usage.ResultSuccess {
		t.Fatalf("reopened usage records = %+v", got)
	}
}

func TestQueryFiltersUsageRecords(t *testing.T) {
	store := usage.NewMemory()
	now := time.Now().UTC()
	for _, record := range []usage.RequestRecord{
		{ID: "a", StartedAt: now.Add(-2 * time.Second), CompletedAt: now.Add(-2 * time.Second), FinalModel: "gemini/gemini-2.5-flash", Result: usage.ResultSuccess},
		{ID: "b", StartedAt: now.Add(-1 * time.Second), CompletedAt: now.Add(-1 * time.Second), FinalModel: "xai/grok-3", Result: usage.ResultFailure, Fallback: true},
	} {
		if err := store.Append(record); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.List(usage.Query{Model: "xai/grok-3", Result: usage.ResultFailure, Fallback: boolPtr(true)})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "b" {
		t.Fatalf("query filter mismatch: %+v", got)
	}
}

func boolPtr(v bool) *bool { return &v }
